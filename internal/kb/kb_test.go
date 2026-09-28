package kb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stone/kb/internal/config"
	"github.com/stone/kb/internal/model"
)

// newTestKB 在临时目录里造一个「代码仓库 + 知识库」，全程关闭 git 与自动提交。
func newTestKB(t *testing.T) (*KB, string, string) {
	t.Helper()
	// 避免污染 ~/.config/kb/root
	t.Setenv("KB_NO_REMEMBER", "1")

	dir := t.TempDir()
	codeRepo := filepath.Join(dir, "code", "auth-service")
	if err := os.MkdirAll(filepath.Join(codeRepo, "pkg", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(codeRepo, "pkg", "db", "pool.go"), "package db\n\nfunc New() {}\n")
	if err := os.MkdirAll(filepath.Join(codeRepo, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(codeRepo, "api", "order.go"), "package api\n")

	kbRoot := filepath.Join(dir, "my-kb")
	instance, err := Init(kbRoot, InitOptions{
		Repos: []config.RepoConfig{{
			Name:          "auth-service",
			Path:          codeRepo,
			PrimaryBranch: "main",
		}},
		AutoCommit: false,
		NoGit:      true,
	})
	if err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	return instance, kbRoot, codeRepo
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAddWritesMirroredDoc(t *testing.T) {
	instance, kbRoot, _ := newTestKB(t)

	result, err := instance.Add(AddOptions{
		Symptom: "偶发 502 超时",
		At:      "pkg/db/pool.go",
		Repo:    "auth-service",
		Type:    model.TypeIncident,
	})
	if err != nil {
		t.Fatalf("Add 失败: %v", err)
	}

	docPath := filepath.Join(kbRoot, "kb", "auth-service", "pkg", "db", "pool.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("知识文件没有落到镜像路径: %v", err)
	}
	text := string(raw)

	for _, want := range []string{
		"id: " + result.Entry.ID,
		"symptom: 偶发 502 超时",
		"anchor: auth-service/pkg/db/pool.go",
		"anchor_hash: sha256:",
		"status: fresh",
		"## 根因",
		"## 解决",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("frontmatter/正文缺少 %q：\n%s", want, text)
		}
	}

	if result.Entry.Anchor == nil || result.Entry.Anchor.Hash == "" {
		t.Fatal("应采集到基线哈希")
	}
	if len(result.Entry.Keywords) == 0 {
		t.Fatal("应自动抽取关键词")
	}
}

func TestApplyChangeMarksStaleAndRecovers(t *testing.T) {
	instance, kbRoot, codeRepo := newTestKB(t)
	poolPath := filepath.Join(codeRepo, "pkg", "db", "pool.go")

	result, err := instance.Add(AddOptions{Symptom: "偶发 502 超时", At: "pkg/db/pool.go", Repo: "auth-service"})
	if err != nil {
		t.Fatal(err)
	}
	entryID := result.Entry.ID

	// 无关文件变更不应影响该条目
	report, err := instance.ApplyChange("auth-service", []string{"api/order.go"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != 0 || len(report.Stale) != 0 {
		t.Fatalf("无关文件不应命中条目，got %+v", report)
	}

	// 锚点文件变更 → 标记 stale
	writeFile(t, poolPath, "package db\n\nfunc New() { /* 改了 */ }\n")
	report, err = instance.ApplyChange("auth-service", []string{"pkg/db/pool.go"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Matched != 1 || len(report.Stale) != 1 {
		t.Fatalf("应标记 1 条 stale，got %+v", report)
	}
	if report.Stale[0].Reason != model.ReasonContentChanged {
		t.Fatalf("原因应为 content-changed，got %q", report.Stale[0].Reason)
	}

	entry, err := instance.Get(entryID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Status != model.StatusStale {
		t.Fatalf("status 应为 stale，got %s", entry.Status)
	}
	// 只提醒不改写：正文必须原样保留
	body, err := instance.ReadBody(entry)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "偶发 502 超时") {
		t.Fatalf("正文被改动了：\n%s", body)
	}

	// 重复变更不应重复变更状态（幂等）
	report, _ = instance.ApplyChange("auth-service", []string{"pkg/db/pool.go"})
	if len(report.Stale) != 0 {
		t.Fatal("已 stale 的条目不应再次出现在 stale 列表里")
	}

	// 文件删除 → 原因从 content-changed 变为 file-missing，这是新的信息，应报告
	if err := os.Remove(poolPath); err != nil {
		t.Fatal(err)
	}
	report, _ = instance.ApplyChange("auth-service", []string{"pkg/db/pool.go"})
	if len(report.Stale) != 1 {
		t.Fatalf("原因变化应被报告，got %+v", report)
	}
	if report.Stale[0].Reason != model.ReasonFileMissing {
		t.Fatalf("原因应为 file-missing，got %q", report.Stale[0].Reason)
	}

	// 恢复文件内容为基线 → 自动回到 fresh
	writeFile(t, poolPath, "package db\n\nfunc New() {}\n")
	report, _ = instance.ApplyChange("auth-service", []string{"pkg/db/pool.go"})
	if len(report.Recovered) != 1 {
		t.Fatalf("内容回到基线时应恢复 fresh，got %+v", report)
	}

	_ = kbRoot
}

func TestRecoverRefreshesBaseline(t *testing.T) {
	instance, _, codeRepo := newTestKB(t)
	poolPath := filepath.Join(codeRepo, "pkg", "db", "pool.go")

	result, _ := instance.Add(AddOptions{Symptom: "偶发 502 超时", At: "pkg/db/pool.go", Repo: "auth-service"})
	writeFile(t, poolPath, "package db\n\n// 全新实现\n")
	if _, err := instance.ApplyChange("auth-service", []string{"pkg/db/pool.go"}); err != nil {
		t.Fatal(err)
	}

	entry, _ := instance.Get(result.Entry.ID)
	if err := instance.Recover(entry); err != nil {
		t.Fatal(err)
	}
	updated, _ := instance.Get(result.Entry.ID)
	if updated.Status != model.StatusFresh {
		t.Fatalf("recover 后应为 fresh，got %s", updated.Status)
	}
	if updated.Anchor.Hash == result.Entry.Anchor.Hash {
		t.Fatal("recover 应刷新基线哈希为当前内容")
	}

	// 再变更才会再次提示
	writeFile(t, poolPath, "package db\n\n// 又改了\n")
	report, _ := instance.ApplyChange("auth-service", []string{"pkg/db/pool.go"})
	if len(report.Stale) != 1 {
		t.Fatal("刷新基线后，新变更应重新触发 stale")
	}
}

func TestSearchRanksSymptomAboveBody(t *testing.T) {
	instance, _, _ := newTestKB(t)

	if _, err := instance.Add(AddOptions{Symptom: "偶发 502 超时", At: "pkg/db/pool.go", Repo: "auth-service"}); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Add(AddOptions{
		Symptom:      "缓存击穿",
		At:           "api/order.go",
		Repo:         "auth-service",
		Body:         "顺便说一句，偶发 502 也见过，但不是这里的问题",
		BodyProvided: true,
	}); err != nil {
		t.Fatal(err)
	}

	hits := instance.Search(SearchOptions{Query: "502", Limit: 10, IncludeGlobal: true})
	if len(hits) != 2 {
		t.Fatalf("应命中 2 条，got %d", len(hits))
	}
	if hits[0].Entry.Symptom != "偶发 502 超时" {
		t.Fatalf("症状命中的条目应排在正文命中之前，got %q（%.1f）", hits[0].Entry.Symptom, hits[0].Score)
	}
	if hits[0].Score <= hits[1].Score {
		t.Fatalf("分数应有区分度：%v vs %v", hits[0].Score, hits[1].Score)
	}
}

func TestSearchByAnchorSuffix(t *testing.T) {
	instance, _, _ := newTestKB(t)
	if _, err := instance.Add(AddOptions{Symptom: "偶发 502 超时", At: "pkg/db/pool.go", Repo: "auth-service"}); err != nil {
		t.Fatal(err)
	}

	for _, query := range []string{"pkg/db/pool.go", "auth-service/pkg/db/pool.go", "pool.go"} {
		entries := instance.SearchAt(query, "")
		if len(entries) != 1 {
			t.Errorf("kb at %q 应命中 1 条，got %d", query, len(entries))
		}
	}
	if entries := instance.SearchAt("pkg/db/other.go", ""); len(entries) != 0 {
		t.Errorf("不应命中无关路径，got %d", len(entries))
	}
}

func TestDriftScanAndMigratePreservesSuffix(t *testing.T) {
	instance, kbRoot, codeRepo := newTestKB(t)

	// 同一个代码文件上挂两条：第二条会自动加 `-<id>` 后缀避免覆盖
	first, _ := instance.Add(AddOptions{Symptom: "偶发 502 超时", At: "pkg/db/pool.go", Repo: "auth-service", ID: "e1"})
	second, _ := instance.Add(AddOptions{Symptom: "压测时 goroutine 泄漏", At: "pkg/db/pool.go", Repo: "auth-service", ID: "e2"})
	if first.Entry.DocPath == second.Entry.DocPath {
		t.Fatalf("同文件多条目不应共用文档：%s", first.Entry.DocPath)
	}
	if second.Entry.DocPath != "auth-service/pkg/db/pool-e2.md" {
		t.Fatalf("第二条应带 -e2 后缀，got %s", second.Entry.DocPath)
	}

	// 改名：内容不变，靠哈希找回
	if err := os.Rename(
		filepath.Join(codeRepo, "pkg", "db", "pool.go"),
		filepath.Join(codeRepo, "pkg", "db", "conn_pool.go"),
	); err != nil {
		t.Fatal(err)
	}

	findings := instance.DriftScan("auth-service")
	if len(findings) != 2 {
		t.Fatalf("应找到 2 个漂移候选，got %d：%+v", len(findings), findings)
	}

	for _, finding := range findings {
		if _, err := instance.MigrateEntry(finding.ID, finding.NewPath); err != nil {
			t.Fatalf("迁移 %s 失败: %v", finding.ID, err)
		}
	}

	if !fileExists(filepath.Join(kbRoot, "kb", "auth-service", "pkg", "db", "conn_pool.md")) {
		t.Error("主文档没有跟随代码路径迁移")
	}
	if !fileExists(filepath.Join(kbRoot, "kb", "auth-service", "pkg", "db", "conn_pool-e2.md")) {
		t.Error("第二条文档迁移时丢了 -e2 后缀（会与主文档互相覆盖）")
	}
	if fileExists(filepath.Join(kbRoot, "kb", "auth-service", "pkg", "db", "pool.md")) {
		t.Error("旧文档应已被移走")
	}

	updated, _ := instance.Get("e2")
	if updated.Status != model.StatusFresh {
		t.Errorf("迁移后应恢复 fresh，got %s", updated.Status)
	}
	if updated.DriftedFrom != "pkg/db/pool.go" {
		t.Errorf("应记录漂移前路径，got %q", updated.DriftedFrom)
	}
}

func TestReindexRebuildsFromFrontmatter(t *testing.T) {
	instance, kbRoot, _ := newTestKB(t)
	if _, err := instance.Add(AddOptions{Symptom: "偶发 502 超时", At: "pkg/db/pool.go", Repo: "auth-service"}); err != nil {
		t.Fatal(err)
	}

	// 模拟索引损坏
	if err := os.WriteFile(instance.Config.ManifestPath(), []byte("entries: ["), 0o644); err != nil {
		t.Fatal(err)
	}
	broken, err := Open(kbRoot)
	if err != nil {
		t.Fatalf("manifest 损坏不应导致知识库无法打开: %v", err)
	}
	if broken.ManifestWarning == "" {
		t.Fatal("索引损坏时应给出可展示的告警")
	}
	if broken.Manifest.Len() != 0 {
		t.Fatalf("损坏的索引应按空处理，got %d 条", broken.Manifest.Len())
	}

	report, err := broken.Reindex()
	if err != nil {
		t.Fatal(err)
	}
	if report.Entries != 1 {
		t.Fatalf("应从 md 重建出 1 条，got %d", report.Entries)
	}
	entries := broken.Entries()
	if entries[0].Anchor == nil || entries[0].Anchor.Hash == "" {
		t.Fatal("重建后锚点与基线哈希应被恢复")
	}
	if len(entries[0].Keywords) == 0 {
		t.Fatal("重建后关键词应被恢复")
	}
}

func TestAddWithoutAnchorGoesToGlobalFolder(t *testing.T) {
	instance, kbRoot, _ := newTestKB(t)

	result, err := instance.Add(AddOptions{Symptom: "为什么用 Redis 而非本地缓存", Type: model.TypeDecision})
	if err != nil {
		t.Fatal(err)
	}
	if result.Entry.Anchor != nil {
		t.Fatal("无锚点条目不应有 anchor")
	}
	if !strings.HasPrefix(result.Entry.DocPath, "decisions/") {
		t.Fatalf("decision 类应落到 decisions/，got %s", result.Entry.DocPath)
	}
	if !fileExists(filepath.Join(kbRoot, "kb", result.Entry.DocPath)) {
		t.Fatal("全局知识文档未落盘")
	}

	// 无锚点条目永远 fresh，也不参与过期扫描
	report, _ := instance.ApplyChange("auth-service", []string{"pkg/db/pool.go"})
	if len(report.Stale) != 0 {
		t.Fatal("无锚点条目不该被标记 stale")
	}
}
