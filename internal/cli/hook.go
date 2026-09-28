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
	"github.com/stone/kb/internal/webhook"
)

// 关于 hook 名称的一处修正
//
// 需求 §3.1 写的是安装 `post-push` hook，但 Git 并不存在 post-push 钩子
// （只有 pre-push / post-commit / post-merge / post-rewrite / post-checkout）。
// 需求自己也补了「或 post-commit + post-merge」，这里按后者实现：
//
//	post-commit  提交后立即上报，比等 push 更早发现过期，且不依赖网络
//	post-merge   合并后上报，覆盖 pull/rebase 进来的变更
//	pre-push     可选，push 前再兜底上报一次（异步、非阻塞）
//
// 三个 hook 都必须「永不阻断 git」：脚本里一律 `|| true` 且后台执行。
var defaultHookEvents = []string{"post-commit", "post-merge"}

const hookMarker = "由 kb install-hook 生成"

func newHookCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "在代码仓库安装/卸载变更上报 hook",
	}
	cmd.AddCommand(
		hookActionCommand("install", "安装 hook", true),
		hookActionCommand("uninstall", "卸载 hook", false),
		hookListCommand(),
	)
	return cmd
}

// newInstallHookCommand 顶层别名：kb install-hook <repo>
func newInstallHookCommand() *cobra.Command {
	return hookActionCommand("install-hook", "在代码仓库安装变更上报 hook", true)
}

func hookActionCommand(use, short string, install bool) *cobra.Command {
	var (
		events     []string
		withPush   bool
		noRegister bool
	)
	cmd := &cobra.Command{
		Use:   use + " <代码仓库路径>",
		Short: short,
		Long: strings.TrimSpace(`
在 .git/hooks/ 下写入 post-commit / post-merge 脚本，脚本只做一件事：
调用 kb notify 把本次变更上报给本地 webhook 服务，由服务做哈希比对。

注意：脚本永不影响 git 操作（失败静默、后台执行）。
服务没启动时事件会排队到 .kb/outbox.jsonl，下次 kb serve 自动补投。
`),
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath := config.ExpandUser(args[0])
			if install {
				return installHooks(repoPath, events, withPush, noRegister)
			}
			return uninstallHooks(repoPath)
		},
	}
	if install {
		cmd.Flags().StringArrayVar(&events, "events", defaultHookEvents,
			"要安装的 hook 事件，可重复：--events post-commit --events post-merge")
		cmd.Flags().BoolVar(&withPush, "with-pre-push", false, "额外安装 pre-push 兜底上报")
		cmd.Flags().BoolVar(&noRegister, "no-register", false, "不自动把仓库注册到 kb 配置")
	}
	return cmd
}

func hookListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list <代码仓库路径>",
		Short: "查看该仓库已安装的 kb hook",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repoPath := config.ExpandUser(args[0])
			hooksDir, err := hooksDirOf(repoPath)
			if err != nil {
				return err
			}
			writer := newTabWriter()
			fmt.Fprintln(writer, "HOOK\t状态")
			for _, event := range []string{"post-commit", "post-merge", "pre-push"} {
				target := filepath.Join(hooksDir, event)
				state := "未安装"
				if raw, readErr := os.ReadFile(target); readErr == nil {
					if strings.Contains(string(raw), hookMarker) {
						state = "✅ 由 kb 管理"
					} else {
						state = "⚠️ 已存在但非 kb 生成"
					}
				}
				fmt.Fprintf(writer, "%s\t%s\n", event, state)
			}
			return writer.Flush()
		},
	}
}

func hooksDirOf(repoPath string) (string, error) {
	if !gitx.IsRepo(repoPath) {
		return "", fmt.Errorf("%s 不是 git 仓库", repoPath)
	}
	root, err := gitx.RepoRoot(repoPath)
	if err != nil {
		return "", err
	}
	// 兼容 worktree / 自定义 core.hooksPath
	if custom, err := gitx.Run(root, "config", "core.hooksPath"); err == nil && strings.TrimSpace(custom) != "" {
		path := strings.TrimSpace(custom)
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		return path, nil
	}
	if output, err := gitx.Run(root, "rev-parse", "--git-path", "hooks"); err == nil && strings.TrimSpace(output) != "" {
		path := strings.TrimSpace(output)
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		return path, nil
	}
	return filepath.Join(root, ".git", "hooks"), nil
}

func installHooks(repoPath string, events []string, withPush, noRegister bool) error {
	if !gitx.Available() {
		return fmt.Errorf("系统中未找到 git")
	}
	if !gitx.IsRepo(repoPath) {
		return fmt.Errorf("%s 不是 git 仓库", repoPath)
	}
	root, err := gitx.RepoRoot(repoPath)
	if err != nil {
		return err
	}
	hooksDir, err := hooksDirOf(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return err
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法定位 kb 可执行文件: %w", err)
	}
	if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
		executable = resolved
	}

	// 知识库根写进 hook 脚本：这样 hook 在任何 cwd / 任何环境变量下都能工作
	knowledgeBase, err := openKB()
	if err != nil {
		return err
	}
	kbRoot := knowledgeBase.Config.Root
	if !noRegister {
		if err := registerRepo(knowledgeBase, root); err != nil {
			return err
		}
	}

	targetEvents := append([]string{}, events...)
	if len(targetEvents) == 0 {
		targetEvents = defaultHookEvents
	}
	if withPush {
		targetEvents = append(targetEvents, "pre-push")
	}

	writer := newTabWriter()
	fmt.Fprintln(writer, "HOOK\t结果")
	for _, event := range targetEvents {
		target := filepath.Join(hooksDir, event)
		result := "已安装"

		if existing, readErr := os.ReadFile(target); readErr == nil {
			if !strings.Contains(string(existing), hookMarker) {
				backup := target + ".kb-backup"
				if _, statErr := os.Stat(backup); os.IsNotExist(statErr) {
					if copyErr := os.WriteFile(backup, existing, 0o755); copyErr != nil {
						return copyErr
					}
				}
				result = "已覆盖（原内容备份为 " + filepath.Base(backup) + "）"
			} else {
				result = "已更新"
			}
		}

		script := hookScript(executable, kbRoot, root, event)
		if writeErr := os.WriteFile(target, []byte(script), 0o755); writeErr != nil {
			return writeErr
		}
		if chmodErr := os.Chmod(target, 0o755); chmodErr != nil {
			return chmodErr
		}
		fmt.Fprintf(writer, "%s\t%s\n", event, result)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n代码仓库 %s\n知识库   %s\n", root, kbRoot)
	fmt.Println("hook 上报失败不会影响 git；服务未启动时事件会排队，下次 kb serve 补投。")
	return nil
}

func hookScript(executable, kbRoot, repoRoot, event string) string {
	invocation := fmt.Sprintf(`"%s" notify --kb "%s" --repo "%s" --event %s --quiet`,
		executable, kbRoot, repoRoot, event)
	if event == "pre-push" {
		// pre-push 是同步执行的，必须自己兜住错误
		invocation = invocation + " >/dev/null 2>&1 || true"
	} else {
		// post-* 后台执行，避免拖慢 git commit
		invocation = "( " + invocation + " >/dev/null 2>&1 & )"
	}
	return strings.Join([]string{
		"#!/bin/sh",
		"# " + hookMarker + "，重装会覆盖此文件，勿手改。",
		"# 上报失败一律静默，绝不阻断 git 操作。",
		"",
		invocation,
		"exit 0",
		"",
	}, "\n")
}

func uninstallHooks(repoPath string) error {
	hooksDir, err := hooksDirOf(repoPath)
	if err != nil {
		return err
	}
	root, _ := gitx.RepoRoot(repoPath)
	fmt.Printf("仓库 %s\n", root)

	removed := 0
	for _, event := range []string{"post-commit", "post-merge", "pre-push"} {
		target := filepath.Join(hooksDir, event)
		raw, readErr := os.ReadFile(target)
		if readErr != nil || !strings.Contains(string(raw), hookMarker) {
			continue
		}
		if removeErr := os.Remove(target); removeErr != nil {
			return removeErr
		}
		fmt.Printf("  已移除 %s\n", event)
		removed++

		backup := target + ".kb-backup"
		if backupRaw, backupErr := os.ReadFile(backup); backupErr == nil {
			if restoreErr := os.WriteFile(target, backupRaw, 0o755); restoreErr != nil {
				return restoreErr
			}
			_ = os.Remove(backup)
			fmt.Printf("  已还原原 %s（来自 .kb-backup）\n", event)
		}
	}
	if removed == 0 {
		fmt.Println("  没有需要移除的 kb hook")
	}
	return nil
}

// registerRepo 确保仓库已登记在 kb 配置中（供 hook 上报时反查仓库名）。
func registerRepo(knowledgeBase *kb.KB, repoRoot string) error {
	if existing := knowledgeBase.Config.GuessRepo(repoRoot); existing != nil && existing.AbsPath() == repoRoot {
		// 已登记，顺手校正主干分支
		branch := gitx.DefaultBranch(repoRoot)
		if existing.PrimaryBranch != branch {
			existing.PrimaryBranch = branch
			if saveErr := knowledgeBase.Config.Save(); saveErr != nil {
				return saveErr
			}
		}
		return nil
	}

	name := filepath.Base(repoRoot)
	if conflict := knowledgeBase.Config.Repo(name); conflict != nil && conflict.AbsPath() != repoRoot {
		name = name + "-" + shortID(repoRoot)
	}
	knowledgeBase.Config.UpsertRepo(config.RepoConfig{
		Name:          name,
		Path:          repoRoot,
		PrimaryBranch: gitx.DefaultBranch(repoRoot),
	})
	if err := knowledgeBase.Config.Save(); err != nil {
		return err
	}
	fmt.Printf("已自动纳管仓库 %s -> %s\n", name, repoRoot)
	return nil
}

func shortID(text string) string {
	sum := 0
	for _, r := range text {
		sum = (sum*31 + int(r)) % 100000
	}
	return fmt.Sprintf("%05d", sum)
}

func newNotifyCommand() *cobra.Command {
	var (
		repoPath string
		event    string
		commit   string
		branch   string
		files    []string
		quiet    bool
	)
	cmd := &cobra.Command{
		Use:    "notify",
		Short:  "（内部）上报一次代码变更事件，由 git hook 调用",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := config.FindRoot(kbRootFlag)
			if err != nil {
				if quiet {
					return nil
				}
				return err
			}
			cfg, err := config.Load(root)
			if err != nil {
				if quiet {
					return nil
				}
				return err
			}

			target := repoPath
			if target == "" {
				target, _ = os.Getwd()
			}
			target = config.ExpandUser(target)

			repo := cfg.GuessRepo(target)
			if repo == nil {
				if quiet {
					return nil
				}
				return fmt.Errorf("仓库 %s 未纳管，用 kb repo add 或 kb install-hook 登记", target)
			}

			if commit == "" {
				commit = gitx.HeadCommit(target)
			}
			if branch == "" {
				branch = gitx.CurrentBranch(target)
			}
			changed := files
			if len(changed) == 0 {
				changed = gitx.ChangedFilesForCommit(target, commit)
				if len(changed) == 0 {
					changed = append(gitx.UncommittedFiles(target), gitx.UntrackedFiles(target)...)
				}
			}
			if event == "" {
				event = "manual"
			}

			payload := webhook.Payload{
				Repo:   repo.Name,
				Branch: branch,
				Commit: commit,
				Event:  event,
				Files:  changed,
			}
			result := webhook.Notify(cfg, payload)

			if quiet {
				return nil
			}
			switch {
			case result.Queued:
				fmt.Printf("服务未启动，事件已排队（%d 个文件），下次 kb serve 自动补投\n", len(changed))
			case result.Delivered && result.Response != nil:
				fmt.Printf("已上报 %s@%s（%d 个文件，命中 %d 条）\n",
					repo.Name, commit, result.Response.Scanned, result.Response.Matched)
				for _, item := range result.Response.Stale {
					fmt.Printf("  ⚠️ %s 已标记过期：%s\n", item.ID, item.Symptom)
				}
			default:
				return fmt.Errorf("上报失败: %s", result.Error)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repoPath, "repo", "", "代码仓库路径（默认 cwd）")
	cmd.Flags().StringVar(&event, "event", "", "事件名，如 post-commit / post-merge")
	cmd.Flags().StringVar(&commit, "commit", "", "commit hash（默认 HEAD）")
	cmd.Flags().StringVar(&branch, "branch", "", "分支名（默认当前分支）")
	cmd.Flags().StringArrayVar(&files, "files", nil, "显式指定变更文件，可重复")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "静默模式（hook 使用）")
	return cmd
}
