// Package gitx 封装对系统 git 的调用。全部走 os/exec，不引入 go-git 依赖。
package gitx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrGitMissing 系统中没有 git 可执行文件。
var ErrGitMissing = errors.New("系统中未找到 git 可执行文件")

// Available 判断 git 是否可用。
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// RunRaw 执行 git 并返回 (退出码, stdout, stderr)。
func RunRaw(dir string, args ...string) (int, string, string, error) {
	if !Available() {
		return -1, "", "", ErrGitMissing
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimRight(stdout.String(), "\n")
	errOut := strings.TrimRight(stderr.String(), "\n")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), out, errOut, nil
		}
		return -1, out, errOut, err
	}
	return 0, out, errOut, nil
}

// Run 执行 git，非零退出码时返回错误。
func Run(dir string, args ...string) (string, error) {
	code, out, errOut, err := RunRaw(dir, args...)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("git %s 失败 (rc=%d): %s", strings.Join(args, " "), code, errOut)
	}
	return out, nil
}

// IsRepo dir 是否为某个 git 工作区**的一部分**（含位于仓库内层的目录）。
func IsRepo(dir string) bool {
	if !Available() {
		return false
	}
	code, out, _, err := RunRaw(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && code == 0 && out == "true"
}

// HasOwnRepo dir 自身是否就是 git 仓库根（存在 dir/.git）。
//
// 不能用 IsRepo 代替：IsRepo 基于 `rev-parse --is-inside-work-tree`，
// 对位于某仓库**内层**的目录同样返回 true。
// 知识库必须拥有自己的仓库，否则 kb 的自动提交会落到外层仓库上 ——
// 那等于把私人笔记 commit 进别人的代码库，直接违反「独立仓库」的设计前提。
func HasOwnRepo(dir string) bool {
	if dir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// RepoRoot 返回仓库根目录绝对路径。
func RepoRoot(dir string) (string, error) {
	out, err := Run(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// HeadCommit 返回短 commit hash；失败返回空串。
func HeadCommit(dir string) string {
	code, out, _, err := RunRaw(dir, "rev-parse", "--short", "HEAD")
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// CurrentBranch 返回当前分支名。
func CurrentBranch(dir string) string {
	code, out, _, err := RunRaw(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// DefaultBranch 探测仓库主干分支名：origin/HEAD → main/master → 当前分支。
func DefaultBranch(dir string) string {
	if code, out, _, err := RunRaw(dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil && code == 0 {
		name := strings.TrimSpace(out)
		if index := strings.LastIndex(name, "/"); index >= 0 {
			name = name[index+1:]
		}
		if name != "" {
			return name
		}
	}
	if code, out, _, err := RunRaw(dir, "branch", "--list", "--format=%(refname:short)"); err == nil && code == 0 {
		branches := strings.Split(out, "\n")
		for _, candidate := range []string{"main", "master"} {
			for _, branch := range branches {
				if strings.TrimSpace(branch) == candidate {
					return candidate
				}
			}
		}
	}
	if branch := CurrentBranch(dir); branch != "" {
		return branch
	}
	return "main"
}

// Init 初始化仓库（自身已是仓库则无操作）。
//
// 用 HasOwnRepo 而非 IsRepo：知识库可能被建在某个代码仓库内部
// （例如仓库根下的 .kb-notes/），此时必须建出**嵌套的独立仓库**，
// 而不是因为「已经身处某个工作区」就跳过。
func Init(dir, branch string) error {
	if HasOwnRepo(dir) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	_, err := Run(dir, "init", "-b", branch)
	return err
}

// StatusPorcelain 返回 git status --porcelain 输出。
func StatusPorcelain(dir string) string {
	code, out, _, err := RunRaw(dir, "status", "--porcelain")
	if err != nil || code != 0 {
		return ""
	}
	return out
}

// IsClean 工作区是否干净。
func IsClean(dir string) bool { return strings.TrimSpace(StatusPorcelain(dir)) == "" }

// CommitAll 把所有改动 add 并 commit。无改动时返回 ("", nil)。
//
// 硬性要求 dir 自身是仓库根：这是最后一道防线，防止调用方误传一个
// 「位于别的仓库内部」的目录，导致提交落进那个无关仓库。
func CommitAll(dir, message string) (string, error) {
	if !HasOwnRepo(dir) {
		return "", fmt.Errorf("%s 不是独立的 git 仓库（缺少 .git），拒绝提交以避免写入外层仓库", dir)
	}
	if strings.TrimSpace(StatusPorcelain(dir)) == "" {
		return "", nil
	}
	if _, err := Run(dir, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := Run(dir, "commit", "-m", message); err != nil {
		return "", err
	}
	return HeadCommit(dir), nil
}

// DiffNames 列出 revFrom..revTo 之间变更的文件（相对仓库根）。
func DiffNames(dir, revFrom, revTo string) ([]string, error) {
	out, err := Run(dir, "diff", "--name-only", revFrom+".."+revTo)
	if err != nil {
		return nil, err
	}
	return splitNonEmpty(out), nil
}

// UncommittedFiles 列出相对 HEAD 的未提交变更文件。
func UncommittedFiles(dir string) []string {
	code, out, _, err := RunRaw(dir, "diff", "--name-only", "HEAD")
	if err != nil || code != 0 {
		return nil
	}
	return splitNonEmpty(out)
}

// ChangedFilesForCommit 列出某个 commit 引入的变更文件。
//
// 根提交（没有父提交）时退化为 `git show --name-only`。
func ChangedFilesForCommit(dir, commit string) []string {
	if commit == "" {
		commit = "HEAD"
	}
	if out, err := Run(dir, "diff", "--name-only", commit+"^", commit); err == nil {
		return splitNonEmpty(out)
	}
	out, err := Run(dir, "show", "--pretty=format:", "--name-only", commit)
	if err != nil {
		return nil
	}
	return splitNonEmpty(out)
}

// UntrackedFiles 列出未跟踪文件。
func UntrackedFiles(dir string) []string {
	code, out, _, err := RunRaw(dir, "ls-files", "--others", "--exclude-standard")
	if err != nil || code != 0 {
		return nil
	}
	return splitNonEmpty(out)
}

func splitNonEmpty(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
