package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stone/kb/internal/kb"
	"github.com/stone/kb/internal/model"
)

func newAddCommand() *cobra.Command {
	var (
		at         string
		repo       string
		entryType  string
		confidence string
		body       string
		keywords   []string
		entryID    string
		lines      string
		withAsets  bool
		asJSON     bool
	)

	cmd := &cobra.Command{
		Use:   "add <症状/主题>",
		Short: "记录一条知识（最常用的入口）",
		Long: strings.TrimSpace(`
示例：

    kb add "偶发 502 超时" --at pkg/db/pool.go
    kb add "这字段不能动，下游报表依赖" --at api/order.go --type constraint --confidence verified
    kb add "为什么用 Redis 而非本地缓存" --type decision        # 无锚点的全局知识

正文可以后补：默认生成骨架（现象/根因/解决），也可以 --body 直接写，
或用 --body - 从 stdin 读入。
`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			symptom := strings.Join(args, " ")

			bodyText, bodyProvided := body, cmd.Flags().Changed("body")
			if body == "-" {
				raw, readErr := io.ReadAll(os.Stdin)
				if readErr != nil {
					return fmt.Errorf("读取 stdin 失败: %w", readErr)
				}
				bodyText, bodyProvided = string(raw), true
			}

			var keywordList []string
			for _, item := range keywords {
				for _, piece := range strings.Split(item, ",") {
					if trimmed := strings.TrimSpace(piece); trimmed != "" {
						keywordList = append(keywordList, trimmed)
					}
				}
			}

			cwd, _ := os.Getwd()
			result, err := knowledgeBase.Add(kb.AddOptions{
				Symptom:      symptom,
				At:           at,
				Repo:         repo,
				Type:         model.ParseType(entryType),
				Confidence:   model.ParseConfidence(confidence),
				Body:         bodyText,
				BodyProvided: bodyProvided,
				Keywords:     keywordList,
				ID:           entryID,
				Lines:        lines,
				CWD:          cwd,
				WithAssets:   withAsets,
			})
			if err != nil {
				return err
			}

			if asJSON {
				return printJSON(result)
			}

			entry := result.Entry
			fmt.Printf("已记录 %s\n", entry.ID)
			fmt.Printf("  症状     %s\n", entry.Symptom)
			fmt.Printf("  类型     %s（%s）\n", entry.Type, confidenceBadge(string(entry.Confidence)))
			fmt.Printf("  文档     %s\n", entry.DocPath)
			if entry.Anchor != nil {
				fmt.Printf("  锚点     %s\n", entry.Anchor.Ref())
				fmt.Printf("  基线哈希 %s%s\n", shortHash(entry.Anchor.Hash), suffixCommit(entry.Anchor.Commit))
			}
			fmt.Printf("  关键词   %s\n", strings.Join(entry.Keywords, ", "))
			for _, warning := range result.Warnings {
				warnf("%s", warning)
			}
			fmt.Printf("\n编辑正文：kb edit %s\n", entry.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&at, "at", "", "代码锚点（相对仓库根的路径）")
	cmd.Flags().StringVar(&repo, "repo", "", "代码仓库名（省略时按 cwd 推断）")
	cmd.Flags().StringVar(&entryType, "type", "incident", "类型：incident|decision|constraint|note")
	cmd.Flags().StringVar(&confidence, "confidence", "unverified", "可信度：verified|unverified")
	cmd.Flags().StringVar(&body, "body", "", "正文（- 表示从 stdin 读）")
	cmd.Flags().StringArrayVar(&keywords, "keywords", nil, "补充关键词，逗号分隔或重复传")
	cmd.Flags().StringVar(&entryID, "id", "", "自定义条目 id")
	cmd.Flags().StringVar(&lines, "lines", "", "相关行范围，如 120-180")
	cmd.Flags().BoolVar(&withAsets, "assets", false, "同时创建 .assets 附件目录")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	return cmd
}

func suffixCommit(commit string) string {
	if commit == "" {
		return ""
	}
	return "  @" + commit
}

func newSearchCommand() *cobra.Command {
	var (
		limit   int
		repo    string
		oneType string
		status  string
		asJSON  bool
		grep    bool
	)

	cmd := &cobra.Command{
		Use:   "search <检索词>",
		Short: "按症状检索历史经验",
		Long: strings.TrimSpace(`
打分规则（从重到轻）：症状 → 关键词 → 文档路径 → 锚点 → 正文。
全部查询词都命中症状时额外加权，因为「按现象检索」是本系统的核心体验。

    kb search 502
    kb search "连接池 超时" --status stale
    kb search pool --grep          # 只做文件级全文扫描
`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			query := strings.Join(args, " ")

			if grep {
				lines, usedRg := knowledgeBase.Grep(query, limit*5)
				if asJSON {
					return printJSON(map[string]any{"engine": engineName(usedRg), "lines": lines})
				}
				if len(lines) == 0 {
					fmt.Printf("没有命中「%s」\n", query)
					return nil
				}
				writer := newTabWriter()
				fmt.Fprintf(writer, "文件\t行\t内容\n")
				for _, line := range lines {
					fmt.Fprintf(writer, "%s\t%d\t%s\n", line.File, line.Line, line.Text)
				}
				return writer.Flush()
			}

			options := kb.SearchOptions{
				Query:         query,
				Limit:         limit,
				Repo:          repo,
				IncludeGlobal: true,
			}
			if oneType != "" {
				options.Type = model.ParseType(oneType)
			}
			if status != "" {
				options.Status = model.ParseStatus(status)
			}

			hits := knowledgeBase.Search(options)
			if asJSON {
				return printJSON(map[string]any{"query": query, "hits": hits, "total": len(hits)})
			}
			if len(hits) == 0 {
				fmt.Printf("没有命中「%s」。\n", query)
				fmt.Println("可以换更口语化的现象词；或用 kb at <代码路径> 按锚点查。")
				return nil
			}

			fmt.Printf("命中 %d 条（query=%q）\n", len(hits), query)
			for index, hit := range hits {
				entry := hit.Entry
				fmt.Printf("\n%d. %s\n", index+1, entry.Symptom)
				fmt.Printf("   [%s · %s · 得分 %.1f] %s\n",
					entry.Type, statusBadge(string(entry.Status), entry.StaleReason),
					hit.Score, confidenceBadge(string(entry.Confidence)))
				fmt.Printf("   id     %s\n", entry.ID)
				if entry.Anchor != nil {
					fmt.Printf("   锚点   %s\n", entry.Anchor.Ref())
				}
				if len(entry.Keywords) > 0 {
					fmt.Printf("   关键词 %s\n", strings.Join(entry.Keywords, ", "))
				}
				for _, snippet := range hit.Snippets {
					fmt.Printf("   ┌ %s\n", strings.ReplaceAll(snippet.Text, "\n", "\n   │ "))
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&limit, "limit", 10, "返回条数")
	cmd.Flags().StringVar(&repo, "repo", "", "限定仓库")
	cmd.Flags().StringVar(&oneType, "type", "", "限定类型")
	cmd.Flags().StringVar(&status, "status", "", "限定状态：fresh|stale")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	cmd.Flags().BoolVar(&grep, "grep", false, "只做文件级全文扫描（不按条目打分）")
	return cmd
}

func engineName(usedRipgrep bool) string {
	if usedRipgrep {
		return "ripgrep"
	}
	return "builtin"
}

func newAtCommand() *cobra.Command {
	var (
		repo   string
		asJSON bool
	)
	cmd := &cobra.Command{
		Use:   "at <代码路径>",
		Short: "按代码路径查相关条目",
		Long: strings.TrimSpace(`
正在读某个文件时用它，能立刻看到这个文件上沉淀过什么坑。

    kb at pkg/db/pool.go
    kb at pool.go --repo auth-service      # 尾段匹配也可以
`),
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			codePath := strings.Join(args, " ")
			entries := knowledgeBase.SearchAt(codePath, repo)
			if asJSON {
				return printJSON(map[string]any{"code_path": codePath, "entries": entries, "total": len(entries)})
			}
			if len(entries) == 0 {
				fmt.Printf("%s 上还没有沉淀任何知识。\n", codePath)
				fmt.Printf("可以这样记一条：kb add \"<症状>\" --at %s\n", codePath)
				return nil
			}
			fmt.Printf("%s 上有 %d 条知识\n", codePath, len(entries))
			for index, entry := range entries {
				fmt.Printf("\n%d. %s\n", index+1, entry.Symptom)
				fmt.Printf("   [%s · %s] %s\n", entry.Type,
					statusBadge(string(entry.Status), entry.StaleReason),
					confidenceBadge(string(entry.Confidence)))
				fmt.Printf("   id %s\n", entry.ID)
				if entry.Anchor != nil && entry.Anchor.Lines != "" {
					fmt.Printf("   行范围 %s\n", entry.Anchor.Lines)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "限定仓库")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	return cmd
}

func newStatusCommand() *cobra.Command {
	var (
		repo       string
		asJSON     bool
		refreshAll bool
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "全局状态 + 过期条目列表",
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			if refreshAll {
				count, refreshErr := knowledgeBase.RefreshAll()
				if refreshErr != nil {
					return refreshErr
				}
				fmt.Printf("已重新评估全部锚点，%d 条状态发生变化\n\n", count)
			}

			report := knowledgeBase.Status(repo)
			if asJSON {
				return printJSON(report)
			}

			stats := report.Stats
			fmt.Printf("知识库   %s\n", stats.Root)
			fmt.Printf("条目     %d 条（已锚定 %d / 全局 %d）\n", stats.Total, stats.Anchored, stats.Global)
			fmt.Printf("过期     %d 条\n", stats.Stale)
			if stats.LastCommit != "" {
				fmt.Printf("最近提交 %s\n", stats.LastCommit)
			}
			writeCounts("类型", stats.ByType)
			writeCounts("仓库", stats.ByRepo)
			writeCounts("可信度", stats.ByConfidence)

			if len(stats.Orphans) > 0 {
				fmt.Printf("\n⚠️ 孤立条目（锚点仓库未在配置中）：%s\n", strings.Join(stats.Orphans, ", "))
			}

			if len(report.Stale) == 0 {
				fmt.Println("\n没有过期条目：所有笔记的锚定代码都没变过。")
				return nil
			}

			fmt.Printf("\n⚠️ %d 条笔记的锚定代码已变更，建议复核：\n\n", len(report.Stale))
			writer := newTabWriter()
			fmt.Fprintln(writer, "  ID\t状态\t症状\t锚点\t原因")
			for _, item := range report.Stale {
				anchorRef := "-"
				if item.Entry.Anchor != nil {
					anchorRef = item.Entry.Anchor.Ref()
				}
				fmt.Fprintf(writer, "  %s\t%s\t%s\t%s\t%s\n",
					item.Entry.ID, statusBadge(string(item.Entry.Status), ""),
					item.Entry.Symptom, anchorRef, reasonText(item.Eval.Reason))
			}
			if err := writer.Flush(); err != nil {
				return err
			}
			fmt.Println("\n复核后：kb recover <id> 刷新基线 / kb edit <id> 修正结论")
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "限定仓库")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	cmd.Flags().BoolVar(&refreshAll, "refresh", false, "先重新评估所有锚点再输出")
	return cmd
}

func writeCounts(title string, counts map[string]int) {
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
	fmt.Printf("%s     %s\n", title, strings.Join(parts, "  "))
}

func newEditCommand() *cobra.Command {
	var (
		confidence string
		bodyFile   string
		noEditor   bool
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "编辑条目（默认打开 $EDITOR，也可批量改字段）",
		Long: strings.TrimSpace(`
    kb edit 2026-01-15-pool-leak                       # 打开编辑器改 md
    kb edit 2026-01-15-pool-leak --confidence verified # 只改可信度
    kb edit 2026-01-15-pool-leak --body-file fix.md    # 用文件替换正文
`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			entry, err := knowledgeBase.Get(args[0])
			if err != nil {
				return err
			}

			changed := false
			if bodyFile != "" {
				raw, readErr := os.ReadFile(bodyFile)
				if readErr != nil {
					return readErr
				}
				if err := knowledgeBase.UpdateBody(entry, string(raw), nil); err != nil {
					return err
				}
				changed = true
			}

			if cmd.Flags().Changed("confidence") {
				parsed := model.ParseConfidence(confidence)
				body, readErr := knowledgeBase.ReadBody(entry)
				if readErr != nil {
					return readErr
				}
				if err := knowledgeBase.UpdateBody(entry, body, &parsed); err != nil {
					return err
				}
				changed = true
			}

			if !noEditor && bodyFile == "" && !cmd.Flags().Changed("confidence") {
				target := knowledgeBase.DocAbs(entry.DocPath)
				if err := openInEditor(target); err != nil {
					return err
				}
				// 编辑器可能改了 confidence，重新从 frontmatter 同步
				report, reindexErr := knowledgeBase.Reindex()
				if reindexErr != nil {
					return reindexErr
				}
				_ = report
				changed = true
			}

			updated, err := knowledgeBase.Get(entry.ID)
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(updated)
			}
			if changed {
				fmt.Printf("已更新 %s\n", updated.ID)
			}
			fmt.Printf("  文档     %s\n", knowledgeBase.DocAbs(updated.DocPath))
			fmt.Printf("  可信度   %s\n", confidenceBadge(string(updated.Confidence)))
			fmt.Printf("  状态     %s\n", statusBadge(string(updated.Status), updated.StaleReason))
			return nil
		},
	}
	cmd.Flags().StringVar(&confidence, "confidence", "", "设置可信度：verified|unverified")
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "用文件内容替换正文")
	cmd.Flags().BoolVar(&noEditor, "no-editor", false, "不打开编辑器")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	return cmd
}

func newOpenCommand() *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "open <代码路径>",
		Short: "用系统编辑器打开对应知识文件",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			codePath := strings.Join(args, " ")
			entries := knowledgeBase.SearchAt(codePath, repo)
			if len(entries) == 0 {
				return fmt.Errorf("%s 上还没有知识条目", codePath)
			}
			if len(entries) > 1 {
				fmt.Printf("%s 上有 %d 条，打开第一条。其余：\n", codePath, len(entries))
				for _, entry := range entries[1:] {
					fmt.Printf("  %s  %s\n", entry.ID, entry.Symptom)
				}
			}
			return openInEditor(knowledgeBase.DocAbs(entries[0].DocPath))
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "限定仓库")
	return cmd
}

func newRecoverCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recover <id>",
		Short: "确认「代码看过了，结论仍然成立」，刷新基线哈希",
		Long: `这是唯一会清除 stale 标记的人工动作。执行后以当前文件内容为新基线，
之后再变更才会再次提示。绝不会自动发生。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			entry, err := knowledgeBase.Get(args[0])
			if err != nil {
				return err
			}
			if entry.Anchor == nil {
				return fmt.Errorf("%s 没有代码锚点，不存在过期概念", entry.ID)
			}
			if err := knowledgeBase.Recover(entry); err != nil {
				return err
			}
			updated, err := knowledgeBase.Get(entry.ID)
			if err != nil {
				return err
			}
			fmt.Printf("已确认为有效：%s\n", updated.ID)
			fmt.Printf("  基线哈希 %s\n", shortHash(updated.Anchor.Hash))
			fmt.Printf("  状态     %s\n", statusBadge(string(updated.Status), ""))
			return nil
		},
	}
	return cmd
}

func newDeleteCommand() *cobra.Command {
	var (
		keepFile bool
		yes      bool
	)
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "删除条目（连同文档与附件）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			entry, err := knowledgeBase.Get(args[0])
			if err != nil {
				return err
			}
			if !yes {
				fmt.Printf("将删除 %s（%s），文档 %s\n确认？[y/N] ", entry.ID, entry.Symptom, entry.DocPath)
				var answer string
				_, _ = fmt.Scanln(&answer)
				if !strings.EqualFold(strings.TrimSpace(answer), "y") {
					fmt.Println("已取消")
					return nil
				}
			}
			if err := knowledgeBase.Delete(entry, !keepFile); err != nil {
				return err
			}
			fmt.Printf("已删除 %s\n", entry.ID)
			return nil
		},
	}
	cmd.Flags().BoolVar(&keepFile, "keep-file", false, "只从索引移除，保留 md 文件")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "跳过确认")
	return cmd
}

// openInEditor 用 $KB_EDITOR / $EDITOR / $VISUAL 打开文件。
func openInEditor(path string) error {
	editor := os.Getenv("KB_EDITOR")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}
	parts := strings.Fields(editor)
	command := exec.Command(parts[0], append(parts[1:], path)...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
