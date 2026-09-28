package kb

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stone/kb/internal/gitx"
	"github.com/stone/kb/internal/hashx"
	"github.com/stone/kb/internal/model"
	"github.com/stone/kb/internal/pathmap"
)

// EvalResult 单条目时效评估结果。
type EvalResult struct {
	Status      model.Status `json:"status"`
	Reason      string       `json:"reason,omitempty"`
	CurrentHash string       `json:"current_hash,omitempty"`
}

// evaluateLocked 计算条目时效。调用方必须已持有读锁或写锁。
func (kb *KB) evaluateLocked(entry *model.Entry) EvalResult {
	if entry.Anchor == nil {
		return EvalResult{Status: model.StatusFresh}
	}
	repo := kb.Config.Repo(entry.Anchor.Repo)
	if repo == nil {
		return EvalResult{Status: entry.Status, Reason: model.ReasonRepoNotConfigured}
	}
	abs := filepath.Join(repo.AbsPath(), filepath.FromSlash(entry.Anchor.Path))
	current, exists, _ := hashx.File(abs)
	if !exists {
		return EvalResult{Status: model.StatusStale, Reason: model.ReasonFileMissing}
	}
	if entry.Anchor.Hash != "" && current != entry.Anchor.Hash {
		return EvalResult{Status: model.StatusStale, Reason: model.ReasonContentChanged, CurrentHash: current}
	}
	return EvalResult{Status: model.StatusFresh, CurrentHash: current}
}

// refreshLocked 按当前代码状态刷新条目 status。返回 (eval, 是否变化)。
func (kb *KB) refreshLocked(entry *model.Entry) (EvalResult, bool) {
	result := kb.evaluateLocked(entry)
	if result.Status == model.StatusStale {
		reason := result.Reason
		if reason == "" {
			reason = model.ReasonContentChanged
		}
		return result, entry.MarkStale(reason)
	}
	return result, entry.MarkFresh("")
}

// ---------------------------------------------------------------- 变更检测

// EntryRef 条目在报告中的轻量表示。
type EntryRef struct {
	ID      string `json:"id"`
	Symptom string `json:"symptom"`
	DocPath string `json:"doc_path"`
	Path    string `json:"path,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

func refOf(entry *model.Entry, reason string) EntryRef {
	ref := EntryRef{ID: entry.ID, Symptom: entry.Symptom, DocPath: entry.DocPath, Reason: reason}
	if entry.Anchor != nil {
		ref.Path = entry.Anchor.Path
	}
	return ref
}

// ChangeReport 一次变更检测的结果。
type ChangeReport struct {
	Repo         string     `json:"repo"`
	Commit       string     `json:"commit,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	ScannedFiles int        `json:"scanned_files"`
	Matched      int        `json:"matched"`
	Stale        []EntryRef `json:"stale"`
	Recovered    []EntryRef `json:"recovered,omitempty"`
}

// ApplyChange 对一批变更文件执行哈希比对并标记 stale（需求 FR-3.3 主流程）。
//
//	收到变更 → 对每个变更文件：
//	  1. 计算当前内容哈希
//	  2. 查 manifest，找到锚定到该文件的知识条目
//	  3. 哈希不一致 → 标记该条目为 stale
//	  4. 写入 stale 列表 + 通知
//
// 反向同样成立：哈希重新一致（代码回滚 / 人工确认）时恢复 fresh。
func (kb *KB) ApplyChange(repoName string, files []string) (*ChangeReport, error) {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	repo := kb.Config.Repo(repoName)
	if repo == nil {
		return nil, fmt.Errorf("仓库 %s 未在配置中，忽略本次变更", repoName)
	}

	report := &ChangeReport{
		Repo:         repoName,
		ScannedFiles: len(files),
		Stale:        []EntryRef{},
		Recovered:    []EntryRef{},
	}
	if gitx.IsRepo(repo.AbsPath()) {
		report.Commit = gitx.HeadCommit(repo.AbsPath())
		report.Branch = gitx.CurrentBranch(repo.AbsPath())
	}

	seen := map[string]bool{}
	var targets []*model.Entry
	for _, file := range files {
		for _, entry := range kb.Manifest.ByCodePath(repoName, file) {
			if seen[entry.ID] {
				continue
			}
			seen[entry.ID] = true
			targets = append(targets, entry)
		}
	}
	report.Matched = len(targets)

	dirty := false
	for _, entry := range targets {
		result, changed := kb.refreshLocked(entry)
		if !changed {
			continue
		}
		dirty = true
		if result.Status == model.StatusStale {
			report.Stale = append(report.Stale, refOf(entry, result.Reason))
		} else {
			report.Recovered = append(report.Recovered, refOf(entry, ""))
		}
		if err := kb.WriteDoc(entry, kb.readBodyLocked(entry)); err != nil {
			return nil, err
		}
	}

	if dirty {
		if _, err := kb.saveLocked("stale", repoName); err != nil {
			return nil, err
		}
	}
	sortRefs(report.Stale)
	sortRefs(report.Recovered)
	return report, nil
}

func sortRefs(refs []EntryRef) {
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
}

// ---------------------------------------------------------------- 状态

// EntryStatus 条目 + 时效评估。
type EntryStatus struct {
	Entry *model.Entry `json:"entry"`
	Eval  EvalResult   `json:"eval"`
}

// Detail 条目完整信息（供 UI / MCP 展示）。
type Detail struct {
	Entry *model.Entry `json:"entry"`
	Body  string       `json:"body"`
	Eval  EvalResult   `json:"eval"`
}

// Detail 取条目详情。
func (kb *KB) Detail(id string) (*Detail, error) {
	kb.mu.RLock()
	defer kb.mu.RUnlock()

	entry := kb.Manifest.Get(id)
	if entry == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	body, err := kb.ReadBody(entry)
	if err != nil {
		return nil, err
	}
	return &Detail{
		Entry: entry.Clone(),
		Body:  body,
		Eval:  kb.evaluateLocked(entry),
	}, nil
}

// Stats 全局统计。
type Stats struct {
	Root         string         `json:"root"`
	Total        int            `json:"total"`
	Anchored     int            `json:"anchored"`
	Global       int            `json:"global"`
	Stale        int            `json:"stale"`
	ByType       map[string]int `json:"by_type"`
	ByRepo       map[string]int `json:"by_repo"`
	ByConfidence map[string]int `json:"by_confidence"`
	Orphans      []string       `json:"orphans,omitempty"`
	LastCommit   string         `json:"last_commit,omitempty"`
}

// StatusReport 全局状态 + stale 列表。
type StatusReport struct {
	Stats Stats         `json:"stats"`
	Stale []EntryStatus `json:"stale"`
}

// Status 计算全局状态（需求 FR-3.4 的 kb status）。
func (kb *KB) Status(repoFilter string) *StatusReport {
	kb.mu.RLock()
	defer kb.mu.RUnlock()

	stats := Stats{
		Root:         kb.Config.Root,
		ByType:       map[string]int{},
		ByRepo:       map[string]int{},
		ByConfidence: map[string]int{},
	}
	report := &StatusReport{Stats: stats}

	for _, entry := range kb.Manifest.Entries {
		if repoFilter != "" && (entry.Anchor == nil || entry.Anchor.Repo != repoFilter) {
			continue
		}
		report.Stats.Total++
		report.Stats.ByType[string(entry.Type)]++
		report.Stats.ByConfidence[string(entry.Confidence)]++

		if entry.Anchor == nil {
			report.Stats.Global++
			continue
		}
		report.Stats.Anchored++
		report.Stats.ByRepo[entry.Anchor.Repo]++
		if kb.Config.Repo(entry.Anchor.Repo) == nil {
			report.Stats.Orphans = append(report.Stats.Orphans, entry.ID)
		}

		eval := kb.evaluateLocked(entry)
		if eval.Status == model.StatusStale {
			report.Stats.Stale++
			report.Stale = append(report.Stale, EntryStatus{Entry: entry.Clone(), Eval: eval})
		}
	}

	if gitx.IsRepo(kb.Config.Root) {
		report.Stats.LastCommit = gitx.HeadCommit(kb.Config.Root)
	}
	sort.Strings(report.Stats.Orphans)
	sort.SliceStable(report.Stale, func(i, j int) bool {
		return report.Stale[i].Entry.ID < report.Stale[j].Entry.ID
	})
	return report
}

// RefreshEntry 重新评估单个条目并落盘（用于人工点「已确认」之外的批量刷新）。
func (kb *KB) RefreshEntry(id string) (*EntryStatus, error) {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	entry := kb.Manifest.Get(id)
	if entry == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	result, changed := kb.refreshLocked(entry)
	if changed {
		if err := kb.WriteDoc(entry, kb.readBodyLocked(entry)); err != nil {
			return nil, err
		}
		if _, err := kb.saveLocked("refresh", entry.ID); err != nil {
			return nil, err
		}
	}
	return &EntryStatus{Entry: entry.Clone(), Eval: result}, nil
}

// RefreshAll 重新评估全部条目并一次性落盘。
func (kb *KB) RefreshAll() (int, error) {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	changedCount := 0
	for _, entry := range kb.Manifest.Entries {
		if entry.Anchor == nil {
			continue
		}
		_, changed := kb.refreshLocked(entry)
		if !changed {
			continue
		}
		changedCount++
		if err := kb.WriteDoc(entry, kb.readBodyLocked(entry)); err != nil {
			return changedCount, err
		}
	}
	if changedCount > 0 {
		if _, err := kb.saveLocked("refresh", fmt.Sprintf("%d-entries", changedCount)); err != nil {
			return changedCount, err
		}
	}
	return changedCount, nil
}

// ---------------------------------------------------------------- 漂移

// DriftFinding 一个路径漂移候选。
type DriftFinding struct {
	ID      string `json:"id"`
	Symptom string `json:"symptom"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	DocPath string `json:"doc_path"`
}

// findDriftedLocked 锚点文件丢失时，按内容哈希在仓库里找回同名内容的文件。
func (kb *KB) findDriftedLocked(entry *model.Entry) string {
	if entry.Anchor == nil || entry.Anchor.Hash == "" {
		return ""
	}
	repo := kb.Config.Repo(entry.Anchor.Repo)
	if repo == nil || !dirExists(repo.AbsPath()) {
		return ""
	}
	if fileExists(filepath.Join(repo.AbsPath(), filepath.FromSlash(entry.Anchor.Path))) {
		return ""
	}

	root := repo.AbsPath()
	var found string
	_ = filepath.WalkDir(root, func(path string, item fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if item.IsDir() {
			if skipDirs[item.Name()] || strings.HasSuffix(item.Name(), pathmap.AssetsSuffix) {
				return fs.SkipDir
			}
			return nil
		}
		info, infoErr := item.Info()
		if infoErr != nil || info.Size() > maxDriftScanBytes {
			return nil
		}
		if hash, ok, _ := hashx.File(path); ok && hash == entry.Anchor.Hash {
			if rel, relErr := filepath.Rel(root, path); relErr == nil {
				found = filepath.ToSlash(rel)
			}
			return fs.SkipAll
		}
		return nil
	})
	return found
}

// DriftScan 批量查找漂移候选（只报告，不自动改动）。
func (kb *KB) DriftScan(repoName string) []DriftFinding {
	kb.mu.RLock()
	defer kb.mu.RUnlock()

	var findings []DriftFinding
	for _, entry := range kb.Manifest.Entries {
		if entry.Anchor == nil {
			continue
		}
		if repoName != "" && entry.Anchor.Repo != repoName {
			continue
		}
		newPath := kb.findDriftedLocked(entry)
		if newPath != "" && newPath != entry.Anchor.Path {
			findings = append(findings, DriftFinding{
				ID:      entry.ID,
				Symptom: entry.Symptom,
				OldPath: entry.Anchor.Path,
				NewPath: newPath,
				DocPath: entry.DocPath,
			})
		}
	}
	sort.SliceStable(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	return findings
}

// MigrateResult 单条迁移结果。
type MigrateResult struct {
	ID          string `json:"id"`
	OldDocPath  string `json:"old_doc_path"`
	NewDocPath  string `json:"new_doc_path"`
	NewCodePath string `json:"new_code_path"`
}

// MigrateEntry 把条目锚点迁移到新路径，文档文件随路径镜像一起搬（需求 FR-2.2）。
func (kb *KB) MigrateEntry(id, newCodePath string) (*MigrateResult, error) {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	entry := kb.Manifest.Get(id)
	if entry == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if entry.Anchor == nil {
		return nil, fmt.Errorf("%s 没有代码锚点，无需迁移", id)
	}
	repo := kb.Config.Repo(entry.Anchor.Repo)
	if repo == nil {
		return nil, fmt.Errorf("仓库 %s 未在配置中", entry.Anchor.Repo)
	}

	normalized := pathmap.Normalize(newCodePath)
	oldDocAbs := kb.DocAbs(entry.DocPath)
	oldDocRel := entry.DocPath

	// 同一个代码文件上可能挂多条知识：Add 时会给后到的条目加 `-<id>` 后缀。
	// 迁移时必须把这个后缀带过去，否则多条文档会撞到同一个镜像路径上互相覆盖。
	suffix := docSuffix(repo.Name, entry.Anchor.Path, entry.DocPath)
	plainNew := pathmap.MirrorDocPath(repo.Name, normalized)
	newDocRel := path.Join(path.Dir(plainNew), pathmap.SplitExt(path.Base(plainNew))+suffix+".md")
	newDocAbs := kb.DocAbs(newDocRel)
	oldAssets := kb.DocAbs(pathmap.AssetsDirFor(entry.DocPath))
	newAssets := kb.DocAbs(pathmap.AssetsDirFor(newDocRel))

	entry.DriftedFrom = entry.Anchor.Path
	entry.Anchor.Path = normalized
	baseline := kb.AnchorBaseline(repo, normalized)
	if baseline.Hash != "" {
		entry.Anchor.Hash = baseline.Hash
	}
	if baseline.Commit != "" {
		entry.Anchor.Commit = baseline.Commit
	}
	entry.DocPath = newDocRel

	if oldDocAbs != newDocAbs {
		if err := os.MkdirAll(filepath.Dir(newDocAbs), 0o755); err != nil {
			return nil, err
		}
		if fileExists(oldDocAbs) {
			if err := os.Rename(oldDocAbs, newDocAbs); err != nil {
				return nil, err
			}
		}
	}
	if oldAssets != newAssets && dirExists(oldAssets) {
		if err := os.MkdirAll(filepath.Dir(newAssets), 0o755); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(newAssets); err != nil {
			return nil, err
		}
		if err := os.Rename(oldAssets, newAssets); err != nil {
			return nil, err
		}
	}

	entry.MarkFresh(entry.Anchor.Hash)
	kb.Manifest.Upsert(entry)
	if err := kb.WriteDoc(entry, kb.readBodyLocked(entry)); err != nil {
		return nil, err
	}
	if _, err := kb.saveLocked("migrate", entry.ID); err != nil {
		return nil, err
	}
	return &MigrateResult{
		ID:          entry.ID,
		OldDocPath:  oldDocRel,
		NewDocPath:  newDocRel,
		NewCodePath: normalized,
	}, nil
}

// docSuffix 计算文档名相对于「纯镜像名」多出来的后缀。
//
//	代码 pkg/db/pool.go + 文档 auth-service/pkg/db/pool-2026-01-15-x.md
//	→ "-2026-01-15-x"
//
// 用于迁移时保持「同文件多条目」的命名，避免路径碰撞。
// 注意用 TrimPrefix：多出来的部分是接在纯镜像名之后的，不是在结尾处被裁掉的。
func docSuffix(repoName, codePath, docPath string) string {
	plain := strings.TrimSuffix(path.Base(pathmap.MirrorDocPath(repoName, codePath)), ".md")
	actual := strings.TrimSuffix(path.Base(pathmap.Normalize(docPath)), ".md")
	if plain == actual {
		return ""
	}
	return strings.TrimPrefix(actual, plain)
}
