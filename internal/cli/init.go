package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/stone/kb/internal/config"
	"github.com/stone/kb/internal/gitx"
	"github.com/stone/kb/internal/kb"
)

func newInitCommand() *cobra.Command {
	var (
		path         string
		repoSpecs    []string
		noGit        bool
		noAutoCommit bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "初始化知识库目录结构（并建独立 git 仓库）",
		Long: strings.TrimSpace(`
初始化后目录结构：

    <kb-root>/
      .git/
      .kb/config.yaml          # 工具配置
      kb/                      # 知识内容根
        <repo>/<path>.md       # 与代码目录镜像
        incidents/  decisions/  cross-cutting/
        .kb-manifest.yaml      # 索引
`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("缺少 --path")
			}

			var repos []config.RepoConfig
			for _, spec := range repoSpecs {
				name, repoPath, found := strings.Cut(spec, "=")
				if !found || strings.TrimSpace(name) == "" || strings.TrimSpace(repoPath) == "" {
					return fmt.Errorf("--repo 需要写成 name=path，收到: %s", spec)
				}
				abs := config.ExpandUser(strings.TrimSpace(repoPath))
				branch := "main"
				if gitx.IsRepo(abs) {
					branch = gitx.DefaultBranch(abs)
				}
				repos = append(repos, config.RepoConfig{
					Name:          strings.TrimSpace(name),
					Path:          abs,
					PrimaryBranch: branch,
				})
			}

			resolved := config.ExpandUser(path)
			knowledgeBase, err := kb.Init(resolved, kb.InitOptions{
				Repos:             repos,
				Force:             true,
				DisableAutoCommit: noAutoCommit,
				NoGit:             noGit,
			})
			if err != nil {
				return err
			}

			fmt.Printf("知识库已初始化：%s\n", knowledgeBase.Config.Root)
			fmt.Printf("  内容目录 : %s\n", knowledgeBase.Config.KBDir())
			fmt.Printf("  配置文件 : %s\n", knowledgeBase.Config.ConfigPath())
			if len(knowledgeBase.Config.Repos) > 0 {
				writer := newTabWriter()
				fmt.Fprintln(writer, "  已纳管仓库\t路径\t主干分支")
				for _, repo := range knowledgeBase.Config.Repos {
					fmt.Fprintf(writer, "  %s\t%s\t%s\n", repo.Name, repo.AbsPath(), repo.PrimaryBranch)
				}
				_ = writer.Flush()
			}
			fmt.Println("\n下一步：")
			fmt.Println("  kb add \"偶发 502 超时\" --at pkg/db/pool.go --repo <仓库名>")
			fmt.Println("  kb install-hook <代码仓库路径>     # 装 git hook，代码变更后自动标记过期")
			fmt.Println("  kb serve                           # 启动 webhook 服务与 Web UI")

			// 知识库建在别的仓库内部时，它自己仍是一个嵌套的独立仓库；
			// 但外层仓库会把它当成未跟踪内容，得提醒加 .gitignore。
			if !noGit {
				if outer, outerErr := gitx.RepoRoot(filepath.Dir(knowledgeBase.Config.Root)); outerErr == nil && outer != "" {
					warnf("知识库位于另一个 git 仓库内部：%s", outer)
					warnf("它已拥有自己的 .git（私有历史互不影响），但请把 %q 加入该仓库的 .gitignore",
						filepath.Base(knowledgeBase.Config.Root))
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&path, "path", "", "知识库根目录（必填）")
	cmd.Flags().StringArrayVar(&repoSpecs, "repo", nil, "纳管代码仓库，可重复：--repo auth-service=/path/to/repo")
	cmd.Flags().BoolVar(&noGit, "no-git", false, "不初始化 git 仓库")
	cmd.Flags().BoolVar(&noAutoCommit, "no-auto-commit", false, "关闭编辑后的自动提交")
	return cmd
}

func newRepoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "管理纳管的代码仓库",
	}

	var (
		branch string
		ignore []string
		asJSON bool
	)

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "列出已纳管的仓库",
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			if asJSON {
				return printJSON(knowledgeBase.Config.Repos)
			}
			if len(knowledgeBase.Config.Repos) == 0 {
				fmt.Println("还没有纳管任何仓库。用 kb repo add <name> <path> 添加。")
				return nil
			}
			writer := newTabWriter()
			fmt.Fprintln(writer, "仓库\t路径\t主干分支\t忽略模式\t状态")
			for _, repo := range knowledgeBase.Config.Repos {
				state := "✅ 可达"
				if !gitx.IsRepo(repo.AbsPath()) {
					state = "⚠️ 不是 git 仓库/路径不存在"
				}
				fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
					repo.Name, repo.AbsPath(), repo.PrimaryBranch,
					strings.Join(repo.Ignore, ","), state)
			}
			return writer.Flush()
		},
	}
	listCmd.Flags().BoolVar(&asJSON, "json", false, "输出 JSON")
	cmd.AddCommand(listCmd)

	addCmd := &cobra.Command{
		Use:   "add <name> <path>",
		Short: "纳管一个代码仓库",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			name := strings.TrimSpace(args[0])
			abs := config.ExpandUser(args[1])
			if info, statErr := os.Stat(abs); statErr != nil || !info.IsDir() {
				return fmt.Errorf("路径不存在或不是目录: %s", abs)
			}
			resolvedBranch := branch
			if resolvedBranch == "" {
				if gitx.IsRepo(abs) {
					resolvedBranch = gitx.DefaultBranch(abs)
				} else {
					resolvedBranch = "main"
				}
			}
			knowledgeBase.Config.UpsertRepo(config.RepoConfig{
				Name:          name,
				Path:          abs,
				PrimaryBranch: resolvedBranch,
				Ignore:        ignore,
			})
			if err := knowledgeBase.Config.Save(); err != nil {
				return err
			}
			if _, err := knowledgeBase.Save("repo", name); err != nil {
				return err
			}
			fmt.Printf("已纳管仓库 %s -> %s（主干分支 %s）\n", name, abs, resolvedBranch)
			if !gitx.IsRepo(abs) {
				warnf("%s 不是一个 git 仓库，代码变更检测不可用", abs)
			}
			return nil
		},
	}
	addCmd.Flags().StringVar(&branch, "branch", "", "主干分支名（默认自动探测）")
	addCmd.Flags().StringArrayVar(&ignore, "ignore", nil, "忽略的文件模式，可重复，如 --ignore '*_test.go'")
	cmd.AddCommand(addCmd)

	removeCmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "取消纳管（不删除已有知识）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			knowledgeBase, err := openKB()
			if err != nil {
				return err
			}
			name := args[0]
			if !knowledgeBase.Config.RemoveRepo(name) {
				return fmt.Errorf("未找到仓库 %s", name)
			}
			if err := knowledgeBase.Config.Save(); err != nil {
				return err
			}
			if _, err := knowledgeBase.Save("repo-remove", name); err != nil {
				return err
			}
			fmt.Printf("已取消纳管 %s。相关条目的锚点仍然保留，kb status 会把它们列为孤立条目。\n", name)
			return nil
		},
	}
	cmd.AddCommand(removeCmd)

	return cmd
}
