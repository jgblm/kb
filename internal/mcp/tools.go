package mcp

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/stone/kb/internal/kb"
	"github.com/stone/kb/internal/model"
)

// toolDefinitions 返回 5 个工具的声明（需求 FR-4.2）。
func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name": "kb_add",
			"description": "记录一条知识（踩坑经验 / 设计决策 / 隐性约束）。" +
				"症状/主题必填，代码锚点可选但强烈建议填——有锚点才能享受代码变更时的过期提醒。" +
				"正文可以后补，允许只留一行。只记录「代码里看不出来的判断」，不要复述代码本身。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"symptom": map[string]any{
						"type":        "string",
						"description": "症状或主题，写成「现场能想起来的那句话」，例如「偶发 502 超时」",
					},
					"anchor": map[string]any{
						"type":        "string",
						"description": "代码锚点，相对仓库根的路径，例如 pkg/db/pool.go",
					},
					"repo": map[string]any{
						"type":        "string",
						"description": "代码仓库名。省略时按当前工作目录推断",
					},
					"type": map[string]any{
						"type":        "string",
						"enum":        []string{"incident", "decision", "constraint", "note"},
						"description": "知识类型，默认 incident",
					},
					"confidence": map[string]any{
						"type":        "string",
						"enum":        []string{"verified", "unverified"},
						"description": "可信度。已亲自验证过才填 verified",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Markdown 正文。省略则生成骨架（现象/根因/解决）稍后补",
					},
					"keywords": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "补充检索关键词（症状里的词会自动抽取）",
					},
				},
				"required": []string{"symptom"},
			},
		},
		{
			"name":        "kb_search",
			"description": "按症状检索历史经验。这是最常用的工具——遇到问题时先查「我以前遇到过类似的吗」。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "检索词，按现象写，例如「502」「超时」「连接池」",
					},
					"limit": map[string]any{
						"type": "integer", "description": "返回条数，默认 10",
					},
					"repo": map[string]any{
						"type": "string", "description": "限定仓库",
					},
					"type": map[string]any{
						"type": "string", "enum": []string{"incident", "decision", "constraint", "note"},
					},
					"status": map[string]any{
						"type": "string", "enum": []string{"fresh", "stale"},
						"description": "按时效状态过滤，stale 表示锚定代码已变更",
					},
				},
				"required": []string{"query"},
			},
		},
		{
			"name":        "kb_at",
			"description": "按代码路径查相关条目。正在读某个文件时用它，能立刻看到这个文件上沉淀过什么坑。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "代码路径，可以是全路径或尾段，例如 pkg/db/pool.go 或 pool.go",
					},
					"repo": map[string]any{"type": "string", "description": "限定仓库"},
				},
				"required": []string{"path"},
			},
		},
		{
			"name":        "kb_stale",
			"description": "列出因锚定代码变更而可能过时的条目。改动代码后用它检查是否推翻了自己的历史判断。",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"repo": map[string]any{"type": "string", "description": "限定仓库"},
				},
			},
		},
		{
			"name":        "kb_status",
			"description": "知识库全局统计：条目数、锚定比例、过期数、分布情况。",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

// callTool 分派并执行工具，返回给 agent 看的纯文本结果。
func (s *Server) callTool(name string, raw json.RawMessage) (string, error) {
	switch name {
	case "kb_add":
		return s.toolAdd(raw)
	case "kb_search":
		return s.toolSearch(raw)
	case "kb_at":
		return s.toolAt(raw)
	case "kb_stale":
		return s.toolStale(raw)
	case "kb_status":
		return s.toolStatus(raw)
	default:
		return "", fmt.Errorf("未知工具: %s", name)
	}
}

func (s *Server) toolAdd(raw json.RawMessage) (string, error) {
	var args struct {
		Symptom    string   `json:"symptom"`
		Anchor     string   `json:"anchor"`
		Repo       string   `json:"repo"`
		Type       string   `json:"type"`
		Confidence string   `json:"confidence"`
		Body       string   `json:"body"`
		Keywords   []string `json:"keywords"`
	}
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}

	result, err := s.KB.Add(kb.AddOptions{
		Symptom:      args.Symptom,
		At:           args.Anchor,
		Repo:         args.Repo,
		Type:         model.ParseType(args.Type),
		Confidence:   model.ParseConfidence(args.Confidence),
		Body:         args.Body,
		BodyProvided: strings.TrimSpace(args.Body) != "",
		Keywords:     args.Keywords,
	})
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "已记录：%s\n", result.Entry.Symptom)
	fmt.Fprintf(&builder, "  id      : %s\n", result.Entry.ID)
	fmt.Fprintf(&builder, "  doc_path: %s\n", result.Entry.DocPath)
	if result.Entry.Anchor != nil {
		fmt.Fprintf(&builder, "  锚点    : %s\n", result.Entry.Anchor.Ref())
		if result.Entry.Anchor.Hash != "" {
			fmt.Fprintf(&builder, "  基线哈希: %s\n", result.Entry.Anchor.Hash)
		}
	}
	fmt.Fprintf(&builder, "  关键词  : %s\n", strings.Join(result.Entry.Keywords, ", "))
	for _, warning := range result.Warnings {
		fmt.Fprintf(&builder, "  ! %s\n", warning)
	}
	builder.WriteString("正文可稍后用 kb edit / Web UI 补充。")
	return builder.String(), nil
}

func (s *Server) toolSearch(raw json.RawMessage) (string, error) {
	var args struct {
		Query  string `json:"query"`
		Limit  int    `json:"limit"`
		Repo   string `json:"repo"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}

	options := kb.SearchOptions{
		Query:         args.Query,
		Limit:         args.Limit,
		Repo:          args.Repo,
		IncludeGlobal: true,
	}
	if args.Type != "" {
		options.Type = model.ParseType(args.Type)
	}
	if args.Status != "" {
		options.Status = model.ParseStatus(args.Status)
	}

	hits := s.KB.Search(options)
	if len(hits) == 0 {
		return fmt.Sprintf("没有命中「%s」。\n可以换更口语化的现象词，或用 kb_at 按代码路径查。", args.Query), nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "命中 %d 条（query=%q）：\n", len(hits), args.Query)
	for index, hit := range hits {
		entry := hit.Entry
		fmt.Fprintf(&builder, "\n%d. [%s · %s · %.1f] %s\n", index+1,
			entry.Type, statusWord(entry.Status), hit.Score, entry.Symptom)
		fmt.Fprintf(&builder, "   id: %s", entry.ID)
		if entry.Confidence == model.ConfidenceVerified {
			builder.WriteString("  ✓已验证")
		} else {
			builder.WriteString("  ⚠未验证")
		}
		builder.WriteString("\n")
		if entry.Anchor != nil {
			fmt.Fprintf(&builder, "   锚点: %s\n", entry.Anchor.Ref())
		}
		if entry.Status == model.StatusStale {
			fmt.Fprintf(&builder, "   ⚠ 该条目已过期：%s\n", entry.StaleReason)
		}
		for _, snippet := range hit.Snippets {
			fmt.Fprintf(&builder, "   %s\n", oneLine(snippet.Text, 160))
		}
	}
	return builder.String(), nil
}

func (s *Server) toolAt(raw json.RawMessage) (string, error) {
	var args struct {
		Path string `json:"path"`
		Repo string `json:"repo"`
	}
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}

	entries := s.KB.SearchAt(args.Path, args.Repo)
	if len(entries) == 0 {
		return fmt.Sprintf("%s 上还没有沉淀任何知识。", args.Path), nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "%s 上有 %d 条知识：\n", args.Path, len(entries))
	for index, entry := range entries {
		fmt.Fprintf(&builder, "\n%d. [%s · %s] %s\n", index+1, entry.Type, statusWord(entry.Status), entry.Symptom)
		fmt.Fprintf(&builder, "   id: %s\n", entry.ID)
		if entry.Anchor != nil && entry.Anchor.Lines != "" {
			fmt.Fprintf(&builder, "   行范围: %s\n", entry.Anchor.Lines)
		}
		if body, err := s.KB.ReadBody(entry); err == nil && strings.TrimSpace(body) != "" {
			fmt.Fprintf(&builder, "   %s\n", oneLine(body, 200))
		}
	}
	return builder.String(), nil
}

func (s *Server) toolStale(raw json.RawMessage) (string, error) {
	var args struct {
		Repo string `json:"repo"`
	}
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}

	report := s.KB.Status(args.Repo)
	if len(report.Stale) == 0 {
		return "没有过期条目，所有笔记的锚定代码都没变过。", nil
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "%d 条笔记的锚定代码已变更，建议复核：\n", len(report.Stale))
	for index, item := range report.Stale {
		entry := item.Entry
		fmt.Fprintf(&builder, "\n%d. %s\n", index+1, entry.Symptom)
		fmt.Fprintf(&builder, "   id: %s\n", entry.ID)
		if entry.Anchor != nil {
			fmt.Fprintf(&builder, "   锚点: %s\n", entry.Anchor.Ref())
		}
		fmt.Fprintf(&builder, "   原因: %s\n", reasonWord(item.Eval.Reason))
	}
	builder.WriteString("\n复核后如果结论仍成立，用 kb recover <id> 刷新基线；如果已被推翻，直接编辑正文。")
	return builder.String(), nil
}

func (s *Server) toolStatus(raw json.RawMessage) (string, error) {
	var args struct {
		Repo string `json:"repo"`
	}
	if err := unmarshalArgs(raw, &args); err != nil {
		return "", err
	}

	report := s.KB.Status(args.Repo)
	stats := report.Stats

	var builder strings.Builder
	fmt.Fprintf(&builder, "知识库：%s\n", stats.Root)
	fmt.Fprintf(&builder, "条目总数: %d（已锚定 %d / 全局 %d）\n", stats.Total, stats.Anchored, stats.Global)
	fmt.Fprintf(&builder, "过期条目: %d\n", stats.Stale)
	if stats.LastCommit != "" {
		fmt.Fprintf(&builder, "最近提交: %s\n", stats.LastCommit)
	}
	writeSortedCounts(&builder, "类型", stats.ByType)
	writeSortedCounts(&builder, "仓库", stats.ByRepo)
	writeSortedCounts(&builder, "可信度", stats.ByConfidence)
	if len(stats.Orphans) > 0 {
		fmt.Fprintf(&builder, "孤立条目（锚点仓库未配置）: %s\n", strings.Join(stats.Orphans, ", "))
	}
	return builder.String(), nil
}

func writeSortedCounts(builder *strings.Builder, title string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	fmt.Fprintf(builder, "%s: %s\n", title, strings.Join(parts, "  "))
}

func unmarshalArgs(raw json.RawMessage, target any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("参数解析失败: %w", err)
	}
	return nil
}

func statusWord(status model.Status) string {
	if status == model.StatusStale {
		return "stale 已过期"
	}
	return "fresh"
}

func reasonWord(reason string) string {
	switch reason {
	case model.ReasonContentChanged:
		return "文件内容变了（content-changed）"
	case model.ReasonFileMissing:
		return "锚点文件已不存在（file-missing）"
	case model.ReasonRepoNotConfigured:
		return "锚点仓库未配置（repo-not-configured）"
	default:
		return reason
	}
}

func oneLine(text string, limit int) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	runes := []rune(collapsed)
	if len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return collapsed
}
