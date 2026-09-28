// Package cli 实现 kb 命令行（cobra）。
package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/stone/kb/internal/config"
	"github.com/stone/kb/internal/kb"
)

var kbRootFlag string

// Version 当前构建版本，由 main 注入。
var Version = "dev"

// Execute 运行 CLI。
func Execute(version string) error {
	Version = version
	root := newRootCommand(version)
	return root.Execute()
}

func newRootCommand(version string) *cobra.Command {
	root := &cobra.Command{
		Use:   "kb",
		Short: "个人代码知识库 —— 带代码锚定的踩坑经验与设计决策",
		Long: strings.TrimSpace(`
kb 是一个带代码锚定的个人知识库，服务于两件事：
  · 关键时刻快速定位问题（按症状检索历史经验）
  · 沉淀个人判断力（踩坑、决策、隐性约束）

设计要点：
  稀疏优先   只记踩过的坑与做过的决策，不追求覆盖率
  症状检索   按现象而非模块检索
  路径镜像   知识文件路径 = 代码文件路径（换根）
  只提醒不改写  代码变更只标记 stale，绝不覆盖人工正文
`),
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
	}

	root.PersistentFlags().StringVar(&kbRootFlag, "kb", "",
		"知识库根目录（默认从 cwd 向上查找，或读 $KB_ROOT）")

	root.AddCommand(
		newInitCommand(),
		newRepoCommand(),
		newAddCommand(),
		newSearchCommand(),
		newAtCommand(),
		newStatusCommand(),
		newEditCommand(),
		newOpenCommand(),
		newRecoverCommand(),
		newDeleteCommand(),
		newReindexCommand(),
		newValidateCommand(),
		newMigrateCommand(),
		newServeCommand(),
		newMCPCommand(),
		newNotifyCommand(),
		newHookCommand(),
		newInstallHookCommand(),
	)
	return root
}

// openKB 打开知识库（定位根 → 读配置 → 读索引）。
func openKB() (*kb.KB, error) {
	root, err := config.FindRoot(kbRootFlag)
	if err != nil {
		return nil, err
	}
	knowledgeBase, err := kb.Open(root)
	if err != nil {
		return nil, err
	}
	if knowledgeBase.ManifestWarning != "" {
		warnf("%s", knowledgeBase.ManifestWarning)
	}
	return knowledgeBase, nil
}

// ---------------------------------------------------------------- 输出

// printJSON 以 UTF-8 友好的缩进 JSON 输出。
func printJSON(payload any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(payload)
}

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

// ---------------------------------------------------------------- 展示辅助

func statusBadge(status string, staleReason string) string {
	if status == "stale" {
		if staleReason != "" {
			return "⚠️ 已过期(" + staleReason + ")"
		}
		return "⚠️ 已过期"
	}
	return "✅ fresh"
}

func confidenceBadge(confidence string) string {
	if confidence == "verified" {
		return "✓已验证"
	}
	return "未验证"
}

func reasonText(reason string) string {
	switch reason {
	case "content-changed":
		return "文件内容已变更"
	case "file-missing":
		return "锚点文件已不存在"
	case "repo-not-configured":
		return "锚点仓库未配置"
	case "":
		return "-"
	default:
		return reason
	}
}

func shortHash(hash string) string {
	if hash == "" {
		return "-"
	}
	hexPart := hash
	if _, after, found := strings.Cut(hash, ":"); found {
		hexPart = after
	}
	if len(hexPart) > 12 {
		return hexPart[:12]
	}
	return hexPart
}

func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "  ! "+format+"\n", args...)
}
