package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newReindexCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "从 md frontmatter 重建索引（manifest 损坏时的救命命令）",
		Long: `manifest 只是派生索引，md 文件才是唯一事实来源。
任何时候都可以从 md 重建，所以索引坏掉不致命。`,
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			report, err := knowledgeBase.Reindex()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(report)
			}
			fmt.Printf("已重建索引：%d 条（变化 %+d）\n", report.Entries, report.Delta)
			for _, item := range report.SkippedNoFrontmatter {
				warnf("跳过（缺少 frontmatter id）：%s", item)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	return cmd
}

func newValidateCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "校验索引与文档的一致性",
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			report := knowledgeBase.Validate()
			if asJSON {
				return printJSON(report)
			}
			fmt.Printf("已校验 %d 条\n", report.Checked)
			if len(report.Problems) == 0 && len(report.Missing) == 0 {
				fmt.Println("✅ 没有发现问题")
				return nil
			}
			for _, problem := range report.Problems {
				fmt.Printf("  ⚠️ %s\n", problem)
			}
			for _, missing := range report.Missing {
				fmt.Printf("  ❌ 文档缺失：%s\n", missing)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	return cmd
}

func newMigrateCommand() *cobra.Command {
	var (
		scan    bool
		repo    string
		apply   bool
		entryID string
		to      string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "处理代码文件改名/移动导致的锚点失联（需求 FR-2.2）",
		Long: strings.TrimSpace(`
代码 rename/move 后锚点会失联。kb 用**内容哈希**把它找回来：
扫描仓库里与记录时哈希一致的文件，认定这就是同一个文件的新位置。

    kb migrate --scan                    # 只看候选，不改动
    kb migrate --scan --apply            # 全部应用
    kb migrate --id <id> --to pkg/db/conn_pool.go   # 手工指定新路径
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}

			// 手工指定路径
			if entryID != "" && to != "" {
				result, migrateErr := knowledgeBase.MigrateEntry(entryID, to)
				if migrateErr != nil {
					return migrateErr
				}
				if asJSON {
					return printJSON(result)
				}
				fmt.Printf("已迁移 %s\n", result.ID)
				fmt.Printf("  代码锚点 %s\n", result.NewCodePath)
				fmt.Printf("  文档     %s\n", result.NewDocPath)
				fmt.Printf("  状态     已恢复 fresh（基线哈希已刷新）\n")
				return nil
			}

			if !scan && entryID == "" {
				return fmt.Errorf("请指定 --scan 或 --id <id> --to <新路径>")
			}

			findings := knowledgeBase.DriftScan(repo)
			if asJSON && !apply {
				return printJSON(map[string]any{"findings": findings, "total": len(findings)})
			}
			if len(findings) == 0 {
				fmt.Println("没有发现失联的锚点。")
				return nil
			}

			if !apply {
				fmt.Printf("发现 %d 个锚点失联，但可按内容哈希找回：\n\n", len(findings))
				writer := newTabWriter()
				fmt.Fprintln(writer, "  ID\t原路径\t新路径（按哈希匹配）")
				for _, finding := range findings {
					fmt.Fprintf(writer, "  %s\t%s\t%s\n", finding.ID, finding.OldPath, finding.NewPath)
				}
				if err := writer.Flush(); err != nil {
					return err
				}
				fmt.Println("\n应用迁移：kb migrate --scan --apply")
				return nil
			}

			fmt.Printf("正在应用 %d 个迁移…\n", len(findings))
			migrated := 0
			for _, finding := range findings {
				result, migrateErr := knowledgeBase.MigrateEntry(finding.ID, finding.NewPath)
				if migrateErr != nil {
					warnf("%s 迁移失败: %v", finding.ID, migrateErr)
					continue
				}
				fmt.Printf("  ✅ %s  %s -> %s\n", result.ID, finding.OldPath, finding.NewPath)
				migrated++
			}
			fmt.Printf("\n完成，共迁移 %d 条\n", migrated)
			return nil
		},
	}
	cmd.Flags().BoolVar(&scan, "scan", false, "扫描失联锚点")
	cmd.Flags().StringVar(&repo, "repo", "", "限定仓库")
	cmd.Flags().BoolVar(&apply, "apply", false, "应用扫描到的全部迁移")
	cmd.Flags().StringVar(&entryID, "id", "", "指定条目 id（配合 --to）")
	cmd.Flags().StringVar(&to, "to", "", "新的代码路径（配合 --id）")
	cmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	return cmd
}
