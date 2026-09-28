// Package kb 是知识库核心：md 读写、锚点解析、哈希比对、漂移探测、自动提交。
package kb

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/stone/kb/internal/config"
	"github.com/stone/kb/internal/gitx"
	"github.com/stone/kb/internal/hashx"
	"github.com/stone/kb/internal/model"
	"github.com/stone/kb/internal/pathmap"
	"github.com/stone/kb/internal/textx"
	"github.com/stone/kb/internal/yamlx"
)

// maxDriftScanBytes 漂移扫描时单文件大小上限，避免哈希大二进制。
const maxDriftScanBytes = 2 << 20

var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true,
	".venv": true, "venv": true, "__pycache__": true, "dist": true, "build": true,
	"target": true, ".idea": true, ".vscode": true,
}

// ErrNotFound 条目不存在。
var ErrNotFound = errors.New("条目不存在")

// ErrBadAnchor 锚点不合法。
var ErrBadAnchor = errors.New("代码锚点不合法")

// KB 一个已打开的知识库。所有公开方法都是并发安全的。
type KB struct {
	Config   *config.Config
	Manifest *Manifest

	// ManifestWarning 索引文件损坏时的告警；非空表示需要 kb reindex。
	ManifestWarning string

	mu sync.RWMutex
}

// Open 打开已存在的知识库。
func Open(root string) (*KB, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	manifest, warning, err := LoadManifest(cfg.ManifestPath())
	if err != nil {
		return nil, err
	}
	return &KB{Config: cfg, Manifest: manifest, ManifestWarning: warning}, nil
}

// InitOptions kb init 的参数。
//
// 都用「否定式」字段，让零值等于文档里的默认行为（InitOptions{} 即「默认来一套」）。
// 早先写的是 AutoCommit bool，于是 InitOptions{} 会静默关掉自动提交，
// 与 config schema 里 default: true 的约定相反。
type InitOptions struct {
	// Repos 初始纳管的代码仓库。
	Repos []config.RepoConfig
	// Force 已存在时也重建缺失的目录。
	Force bool
	// DisableAutoCommit 关闭编辑后的自动提交（默认开启）。
	DisableAutoCommit bool
	// NoGit 跳过 git init。
	NoGit bool
}

// Init 初始化知识库目录结构并建 git 仓库。
func Init(root string, opts InitOptions) (*KB, error) {
	resolved := config.ExpandUser(root)
	cfg, err := config.Load(resolved)
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			return nil, fmt.Errorf("知识库已存在但配置损坏: %w", err)
		}
		cfg = config.Default(resolved)
	}
	cfg.Git.AutoCommit = !opts.DisableAutoCommit

	for _, repo := range opts.Repos {
		if repo.PrimaryBranch == "" {
			repo.PrimaryBranch = "main"
		}
		cfg.UpsertRepo(repo)
	}

	for _, dir := range []string{
		cfg.KBDir(),
		filepath.Join(cfg.KBDir(), "incidents"),
		filepath.Join(cfg.KBDir(), "decisions"),
		filepath.Join(cfg.KBDir(), "cross-cutting"),
		cfg.StateDir(),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	if err := ensureDirKeep(cfg.KBDir()); err != nil {
		return nil, err
	}
	if err := cfg.Save(); err != nil {
		return nil, err
	}

	kb := &KB{Config: cfg, Manifest: NewManifest()}
	if err := kb.Manifest.Save(cfg.ManifestPath()); err != nil {
		return nil, err
	}

	if !opts.NoGit {
		if err := gitx.Init(resolved, "main"); err == nil {
			writeGitignore(cfg)
			if _, err := gitx.CommitAll(resolved, "kb(init): 初始化知识库"); err != nil {
				return nil, err
			}
		}
	}
	if !envFlag("KB_NO_REMEMBER") {
		if err := config.RememberRoot(resolved); err != nil {
			// 记不住默认路径不影响主流程
			_ = err
		}
	}
	return kb, nil
}

func envFlag(name string) bool {
	value := strings.TrimSpace(os.Getenv(name))
	return value == "1" || strings.EqualFold(value, "true")
}

func ensureDirKeep(dir string) error {
	keep := filepath.Join(dir, ".gitkeep")
	if _, err := os.Stat(keep); err == nil {
		return nil
	}
	return os.WriteFile(keep, nil, 0o644)
}

func writeGitignore(cfg *config.Config) {
	target := filepath.Join(cfg.Root, ".gitignore")
	if _, err := os.Stat(target); err == nil {
		return
	}
	content := strings.Join([]string{
		"# kb 运行时状态（不入版本库）",
		".kb/webhook-state.json",
		".kb/outbox.jsonl",
		".DS_Store",
		"",
	}, "\n")
	_ = os.WriteFile(target, []byte(content), 0o644)
}

// ---------------------------------------------------------------- 路径

// DocAbs 文档相对路径 -> 绝对路径。
func (kb *KB) DocAbs(docPath string) string {
	return filepath.Join(kb.Config.KBDir(), filepath.FromSlash(pathmap.Normalize(docPath)))
}

// AssetsAbs 某条目对应的 assets 目录绝对路径。
func (kb *KB) AssetsAbs(entry *model.Entry) string {
	return kb.DocAbs(pathmap.AssetsDirFor(entry.DocPath))
}

// RelDoc 绝对路径 -> 相对 kb/ 的 POSIX 路径。
func (kb *KB) RelDoc(absPath string) (string, error) {
	rel, err := filepath.Rel(kb.Config.KBDir(), absPath)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

// ---------------------------------------------------------------- md 读写

// ReadBody 读条目正文。
func (kb *KB) ReadBody(entry *model.Entry) (string, error) {
	raw, err := os.ReadFile(kb.DocAbs(entry.DocPath))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	_, body, err := yamlx.SplitFrontmatter(string(raw))
	if err != nil {
		return "", err
	}
	return body, nil
}

// WriteDoc 将 frontmatter + 正文写盘。
func (kb *KB) WriteDoc(entry *model.Entry, body string) error {
	target := kb.DocAbs(entry.DocPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(yamlx.JoinFrontmatter(entry.Frontmatter(), body)), 0o644)
}

// EnsureAssets 建立并保持 assets 目录存在。
func (kb *KB) EnsureAssets(entry *model.Entry) (string, error) {
	dir := kb.AssetsAbs(entry)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(dir)
	if err == nil && len(entries) == 0 {
		_ = os.WriteFile(filepath.Join(dir, ".gitkeep"), nil, 0o644)
	}
	return dir, nil
}

// ---------------------------------------------------------------- 锚点

// ResolveRepo 解析目标仓库：显式名字优先，否则从 cwd 推断。
func (kb *KB) ResolveRepo(name, cwd string) (*config.RepoConfig, error) {
	if name != "" {
		repo := kb.Config.Repo(name)
		if repo == nil {
			names := make([]string, 0, len(kb.Config.Repos))
			for _, item := range kb.Config.Repos {
				names = append(names, item.Name)
			}
			if len(names) == 0 {
				names = append(names, "（无）")
			}
			return nil, fmt.Errorf("未配置名为 %q 的仓库。已配置：%s", name, strings.Join(names, ", "))
		}
		return repo, nil
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if guessed := kb.Config.GuessRepo(cwd); guessed != nil {
		return guessed, nil
	}
	return nil, fmt.Errorf("无法从当前目录（%s）推断代码仓库，请用 --repo 指定，或先执行 kb repo add", cwd)
}

// ResolveCodePath 把 --at 的输入归一化成相对仓库根的 POSIX 路径。
func (kb *KB) ResolveCodePath(repo *config.RepoConfig, at, cwd string) (string, error) {
	if strings.TrimSpace(at) == "" {
		return "", ErrBadAnchor
	}
	root := repo.AbsPath()

	if filepath.IsAbs(at) {
		rel, err := filepath.Rel(root, filepath.Clean(at))
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("锚点 %s 不在仓库 %s（%s）内", at, repo.Name, root)
		}
		return filepath.ToSlash(rel), nil
	}

	base := cwd
	if base == "" {
		base, _ = os.Getwd()
	}

	if strings.HasPrefix(at, "./") || strings.HasPrefix(at, "../") {
		rel, err := filepath.Rel(root, filepath.Join(base, at))
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("锚点 %s 不在仓库 %s 内", at, repo.Name)
		}
		return filepath.ToSlash(rel), nil
	}

	// 若相对 cwd 真实存在，则以 cwd 为基准；否则按「相对仓库根」处理
	if _, err := os.Stat(filepath.Join(base, at)); err == nil {
		if rel, err := filepath.Rel(root, filepath.Join(base, at)); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel), nil
		}
	}
	return pathmap.Normalize(at), nil
}

// Baseline 锚点基线。
type Baseline struct {
	Hash   string
	Commit string
	Branch string
	Exists bool
}

// AnchorBaseline 采集锚点基线：哈希、commit、分支。
func (kb *KB) AnchorBaseline(repo *config.RepoConfig, codeRel string) Baseline {
	abs := filepath.Join(repo.AbsPath(), filepath.FromSlash(codeRel))
	hash, exists, _ := hashx.File(abs)
	out := Baseline{Hash: hash, Exists: exists}
	if gitx.IsRepo(repo.AbsPath()) {
		out.Commit = gitx.HeadCommit(repo.AbsPath())
		out.Branch = gitx.CurrentBranch(repo.AbsPath())
	}
	return out
}

// ---------------------------------------------------------------- 新增

// AddOptions kb add 的参数。
type AddOptions struct {
	Symptom      string
	At           string // 代码锚点（可选）
	Repo         string // 仓库名（可选）
	Type         model.EntryType
	Confidence   model.Confidence
	Body         string
	BodyProvided bool
	Keywords     []string
	ID           string
	Lines        string
	CWD          string
	WithAssets   bool
}

// AddResult 新增结果。
type AddResult struct {
	Entry    *model.Entry
	Warnings []string
}

// Add 新增条目。返回结果与警告列表。
func (kb *KB) Add(opts AddOptions) (*AddResult, error) {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	if strings.TrimSpace(opts.Symptom) == "" {
		return nil, errors.New("缺少症状/主题")
	}
	entryType := opts.Type
	if entryType == "" {
		entryType = model.TypeIncident
	}
	confidence := opts.Confidence
	if confidence == "" {
		confidence = model.ConfidenceUnverified
	}

	warnings := []string{}
	var anchor *model.Anchor
	var docPath string

	if strings.TrimSpace(opts.At) != "" {
		repo, err := kb.ResolveRepo(opts.Repo, opts.CWD)
		if err != nil {
			return nil, err
		}
		codeRel, err := kb.ResolveCodePath(repo, opts.At, opts.CWD)
		if err != nil {
			return nil, err
		}
		baseline := kb.AnchorBaseline(repo, codeRel)
		if !baseline.Exists {
			warnings = append(warnings, fmt.Sprintf(
				"锚点文件不存在：%s/%s（已按 %s 记录）", repo.Name, codeRel, model.ConfidenceUnverified))
			confidence = model.ConfidenceUnverified
		}
		anchor = &model.Anchor{
			Repo:   repo.Name,
			Path:   codeRel,
			Commit: baseline.Commit,
			Branch: baseline.Branch,
			Hash:   baseline.Hash,
			Lines:  opts.Lines,
		}
		docPath = pathmap.MirrorDocPath(repo.Name, codeRel)
	} else {
		folder := globalFolderFor(entryType)
		slug := opts.ID
		if slug == "" {
			slug = model.Today() + "-" + pathmap.Slugify(opts.Symptom, 32)
		}
		docPath = folder + "/" + slug + ".md"
		warnings = append(warnings, "未指定代码锚点，条目将存入全局目录（无过期追踪）")
	}

	entry := &model.Entry{
		ID:         kb.uniqueID(entryIDBase(opts.ID, opts.Symptom)),
		Symptom:    opts.Symptom,
		DocPath:    docPath,
		Type:       entryType,
		Confidence: confidence,
		Status:     model.StatusFresh,
		Anchor:     anchor,
		CreatedAt:  model.NowISO(),
		UpdatedAt:  model.NowISO(),
	}
	entry.Keywords = textx.ExtractKeywords(opts.Symptom, opts.Keywords, 8)

	// 目标文档已被占用时退避，避免互相覆盖
	if len(kb.Manifest.ByDocPath(entry.DocPath)) > 0 {
		dir := strings.TrimSuffix(entry.DocPath, filepath.Base(entry.DocPath))
		base := strings.TrimSuffix(filepath.Base(entry.DocPath), ".md")
		entry.DocPath = dir + base + "-" + entry.ID + ".md"
		warnings = append(warnings, "目标文档已存在，改用 "+entry.DocPath)
	}

	body := opts.Body
	if !opts.BodyProvided {
		body = kb.Scaffold(entry)
	}

	kb.Manifest.Upsert(entry)
	if err := kb.WriteDoc(entry, body); err != nil {
		return nil, err
	}
	if opts.WithAssets {
		if _, err := kb.EnsureAssets(entry); err != nil {
			return nil, err
		}
	}
	if _, err := kb.saveLocked("add", entry.ID); err != nil {
		return nil, err
	}
	return &AddResult{Entry: entry.Clone(), Warnings: warnings}, nil
}

func globalFolderFor(entryType model.EntryType) string {
	switch entryType {
	case model.TypeIncident:
		return "incidents"
	case model.TypeDecision:
		return "decisions"
	default:
		return "cross-cutting"
	}
}

func entryIDBase(explicit, symptom string) string {
	if strings.TrimSpace(explicit) != "" {
		return strings.TrimSpace(explicit)
	}
	return model.Today() + "-" + pathmap.Slugify(symptom, 32)
}

func (kb *KB) uniqueID(base string) string {
	candidate := base
	for suffix := 2; kb.Manifest.Has(candidate); suffix++ {
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
	return candidate
}

// Scaffold 按类型生成正文骨架（需求 §6.2：正文可后补，允许只留一行）。
func (kb *KB) Scaffold(entry *model.Entry) string {
	sections := map[model.EntryType][]string{
		model.TypeIncident:   {"现象", "根因", "解决"},
		model.TypeDecision:   {"背景", "决策", "权衡"},
		model.TypeConstraint: {"约束", "原因", "影响"},
		model.TypeNote:       {"说明"},
	}[entry.Type]
	if len(sections) == 0 {
		sections = []string{"说明"}
	}

	var builder strings.Builder
	for index, title := range sections {
		builder.WriteString("## " + title + "\n\n")
		if index == 0 {
			builder.WriteString(entry.Symptom)
		} else {
			builder.WriteString("_待补充_")
		}
		builder.WriteString("\n\n")
	}
	if entry.Anchor != nil {
		builder.WriteString("## 备注\n\n- 相关：`" + entry.Anchor.Ref() + "`\n")
	}
	return builder.String()
}

// ---------------------------------------------------------------- 更新 / 删除

// UpdateBody 更新正文（并可同时修正 confidence）。
func (kb *KB) UpdateBody(entry *model.Entry, body string, confidence *model.Confidence) error {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	entry.Touch()
	if confidence != nil {
		entry.Confidence = *confidence
	}
	kb.Manifest.Upsert(entry)
	if err := kb.WriteDoc(entry, body); err != nil {
		return err
	}
	_, err := kb.saveLocked("edit", entry.ID)
	return err
}

// Delete 删除条目及其文档、附件。
func (kb *KB) Delete(entry *model.Entry, removeFile bool) error {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	kb.Manifest.Remove(entry.ID)
	if removeFile {
		if err := os.Remove(kb.DocAbs(entry.DocPath)); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := os.RemoveAll(kb.AssetsAbs(entry)); err != nil {
			return err
		}
	}
	_, err := kb.saveLocked("delete", entry.ID)
	return err
}

// Recover 人工确认「代码已看过，笔记仍然成立」，恢复 fresh 并刷新基线哈希。
func (kb *KB) Recover(entry *model.Entry) error {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	var newHash string
	if entry.Anchor != nil {
		if repo := kb.Config.Repo(entry.Anchor.Repo); repo != nil {
			baseline := kb.AnchorBaseline(repo, entry.Anchor.Path)
			newHash = baseline.Hash
			if baseline.Commit != "" {
				entry.Anchor.Commit = baseline.Commit
			}
		}
	}
	entry.MarkFresh(newHash)
	kb.Manifest.Upsert(entry)
	if err := kb.WriteDoc(entry, kb.readBodyLocked(entry)); err != nil {
		return err
	}
	_, err := kb.saveLocked("recover", entry.ID)
	return err
}

func (kb *KB) readBodyLocked(entry *model.Entry) string {
	body, err := kb.ReadBody(entry)
	if err != nil {
		return ""
	}
	return body
}

// ---------------------------------------------------------------- 持久化

// Save 写 manifest，并按配置自动 git commit。返回 commit hash。
func (kb *KB) Save(action, entryID string) (string, error) {
	kb.mu.Lock()
	defer kb.mu.Unlock()
	return kb.saveLocked(action, entryID)
}

func (kb *KB) saveLocked(action, entryID string) (string, error) {
	if err := kb.Manifest.Save(kb.Config.ManifestPath()); err != nil {
		return "", err
	}
	if action == "" || !kb.Config.Git.AutoCommit {
		return "", nil
	}
	// 必须用 HasOwnRepo：知识库若被放在某个代码仓库内部，
	// 用 IsRepo 会判定为 true 并把笔记提交进那个仓库。
	if !gitx.HasOwnRepo(kb.Config.Root) {
		return "", nil
	}
	message := kb.Config.RenderCommitMessage(action, entryID)
	commit, err := gitx.CommitAll(kb.Config.Root, message)
	if err != nil {
		// 自动提交失败不应阻断主流程（例如未配置 user.email）
		return "", nil
	}
	return commit, nil
}

// Snapshot 返回条目的深拷贝（读接口用，避免锁外共享指针）。
func (kb *KB) Snapshot(entry *model.Entry) *model.Entry { return entry.Clone() }

// Entries 返回全部条目的深拷贝快照。
func (kb *KB) Entries() []*model.Entry {
	kb.mu.RLock()
	defer kb.mu.RUnlock()
	out := make([]*model.Entry, 0, len(kb.Manifest.Entries))
	for _, entry := range kb.Manifest.Entries {
		out = append(out, entry.Clone())
	}
	return out
}

// Get 按 id 取条目快照。
func (kb *KB) Get(id string) (*model.Entry, error) {
	kb.mu.RLock()
	defer kb.mu.RUnlock()
	entry := kb.Manifest.Get(id)
	if entry == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return entry.Clone(), nil
}

// ---------------------------------------------------------------- 一致性

// ReindexReport 重建结果。
type ReindexReport struct {
	Entries              int      `json:"entries"`
	Delta                int      `json:"delta"`
	SkippedNoFrontmatter []string `json:"skipped_no_frontmatter,omitempty"`
}

// Reindex 从 md frontmatter 重建 manifest。
func (kb *KB) Reindex() (*ReindexReport, error) {
	kb.mu.Lock()
	defer kb.mu.Unlock()

	rebuilt := NewManifest()
	report := &ReindexReport{}

	err := filepath.WalkDir(kb.Config.KBDir(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if skipDirs[entry.Name()] || strings.HasSuffix(entry.Name(), pathmap.AssetsSuffix) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".md") || strings.HasPrefix(entry.Name(), ".") {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		front, _, splitErr := yamlx.SplitFrontmatter(string(raw))
		if splitErr != nil || front == nil {
			rel, _ := kb.RelDoc(path)
			report.SkippedNoFrontmatter = append(report.SkippedNoFrontmatter, rel)
			return nil
		}
		rel, relErr := kb.RelDoc(path)
		if relErr != nil {
			return nil
		}
		parsed := kb.entryFromFrontmatter(front, rel)
		if parsed == nil {
			report.SkippedNoFrontmatter = append(report.SkippedNoFrontmatter, rel)
			return nil
		}
		rebuilt.Upsert(parsed)
		return nil
	})
	if err != nil {
		return nil, err
	}

	report.Entries = rebuilt.Len()
	report.Delta = rebuilt.Len() - kb.Manifest.Len()
	kb.Manifest = rebuilt
	if _, err := kb.saveLocked("reindex", ""); err != nil {
		return nil, err
	}
	return report, nil
}

func (kb *KB) entryFromFrontmatter(front map[string]any, rel string) *model.Entry {
	id := stringField(front, "id")
	if id == "" {
		return nil
	}
	existing := kb.Manifest.Get(id)

	entry := &model.Entry{
		ID:          id,
		Symptom:     stringField(front, "symptom"),
		DocPath:     rel,
		Type:        model.ParseType(stringField(front, "type")),
		Confidence:  model.ParseConfidence(stringField(front, "confidence")),
		Status:      model.ParseStatus(stringField(front, "status")),
		Keywords:    stringSliceField(front, "keywords"),
		CreatedAt:   stringField(front, "created_at"),
		UpdatedAt:   stringField(front, "updated_at"),
		StaleReason: stringField(front, "stale_reason"),
	}
	if entry.Symptom == "" {
		entry.Symptom = id
	}
	if entry.CreatedAt == "" {
		entry.CreatedAt = model.NowISO()
	}
	if entry.UpdatedAt == "" {
		entry.UpdatedAt = entry.CreatedAt
	}
	if existing != nil {
		entry.StaleSince = existing.StaleSince
		entry.DriftedFrom = existing.DriftedFrom
	}

	if ref := stringField(front, "anchor"); ref != "" {
		repo, rest, _ := strings.Cut(ref, "/")
		codePath, lines, _ := strings.Cut(rest, ":")
		entry.Anchor = &model.Anchor{
			Repo:   repo,
			Path:   pathmap.Normalize(codePath),
			Lines:  lines,
			Hash:   stringField(front, "anchor_hash"),
			Commit: stringField(front, "anchor_commit"),
		}
		if existing != nil && existing.Anchor != nil {
			entry.Anchor.Branch = existing.Anchor.Branch
		}
	}
	return entry
}

func stringField(data map[string]any, key string) string {
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func stringSliceField(data map[string]any, key string) []string {
	value, ok := data[key]
	if !ok || value == nil {
		return nil
	}
	var out []string
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if text := strings.TrimSpace(fmt.Sprint(item)); text != "" {
				out = append(out, text)
			}
		}
	case []string:
		out = append(out, typed...)
	case string:
		for _, item := range strings.Split(typed, ",") {
			if text := strings.TrimSpace(item); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

// ValidateReport 校验结果。
type ValidateReport struct {
	Checked  int      `json:"checked"`
	Problems []string `json:"problems,omitempty"`
	Missing  []string `json:"missing_docs,omitempty"`
}

// Validate 校验 manifest 与磁盘文档的一致性。
func (kb *KB) Validate() *ValidateReport {
	kb.mu.RLock()
	defer kb.mu.RUnlock()

	report := &ValidateReport{Checked: kb.Manifest.Len()}
	for _, entry := range kb.Manifest.Entries {
		if err := entry.Validate(); err != nil {
			report.Problems = append(report.Problems, err.Error())
		}
		if _, err := os.Stat(kb.DocAbs(entry.DocPath)); err != nil {
			report.Missing = append(report.Missing, entry.ID+" -> "+entry.DocPath)
		}
		if entry.Anchor != nil {
			if repo := kb.Config.Repo(entry.Anchor.Repo); repo == nil {
				report.Problems = append(report.Problems,
					fmt.Sprintf("%s: 锚点仓库 %s 未在配置中", entry.ID, entry.Anchor.Repo))
			}
		}
	}
	sort.Strings(report.Problems)
	sort.Strings(report.Missing)
	return report
}

func normalizeCodePath(p string) string {
	return pathmap.Normalize(p)
}
