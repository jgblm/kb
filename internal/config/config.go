// Package config 管理知识库配置 <kb-root>/.kb/config.yaml。
//
// <kb-root> 布局（需求 §2.1）：
//
//	<kb-root>/                     # 独立 git 仓库
//	  .git/
//	  .kb/config.yaml              # 本文件
//	  kb/                          # 知识内容根
//	    <repo>/<path>.md
//	    incidents/  decisions/  cross-cutting/
//	    .kb-manifest.yaml          # 索引
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// ConfigRel 配置相对路径。
	ConfigRel = ".kb/config.yaml"
	// KBDirName 知识内容目录名。
	KBDirName = "kb"
	// ManifestName 索引文件名。
	ManifestName = ".kb-manifest.yaml"
	// EnvKBRoot 环境变量名。
	EnvKBRoot = "KB_ROOT"
	// DefaultCommitMessage 默认 commit message 模板。
	DefaultCommitMessage = "kb({action}): {id}"
)

// ErrNotFound 找不到知识库。
var ErrNotFound = errors.New("找不到知识库。请用 --kb 指定，或先执行 kb init --path <dir>")

// RepoConfig 一个被纳管的代码仓库。
type RepoConfig struct {
	Name          string   `yaml:"name"`
	Path          string   `yaml:"path"`
	PrimaryBranch string   `yaml:"primary_branch"`
	Ignore        []string `yaml:"ignore,omitempty"`
}

// AbsPath 展开 ~ 并转绝对路径。
func (r RepoConfig) AbsPath() string {
	return ExpandUser(r.Path)
}

// ServerConfig webhook / UI 监听配置。
type ServerConfig struct {
	Host        string `yaml:"host"`
	WebhookPort int    `yaml:"webhook_port"`
	UIPort      int    `yaml:"ui_port"`
	// Token 可选，hook 侧通过 X-KB-Token 头携带。
	Token string `yaml:"token,omitempty"`
}

// GitConfig 自动提交配置。
type GitConfig struct {
	AutoCommit    bool   `yaml:"auto_commit"`
	CommitMessage string `yaml:"commit_message"`
}

// Config 知识库根配置。
type Config struct {
	Version int          `yaml:"version"`
	Repos   []RepoConfig `yaml:"repos"`
	Server  ServerConfig `yaml:"server"`
	Git     GitConfig    `yaml:"git"`

	// Root 知识库根目录（不写入配置文件）。
	Root string `yaml:"-"`
}

// Default 返回默认配置。
func Default(root string) *Config {
	return &Config{
		Version: 1,
		Server: ServerConfig{
			Host:        "127.0.0.1",
			WebhookPort: 7777,
			UIPort:      7778,
		},
		Git: GitConfig{
			AutoCommit:    true,
			CommitMessage: DefaultCommitMessage,
		},
		Root: root,
	}
}

// KBDir 知识内容根目录。
func (c *Config) KBDir() string { return filepath.Join(c.Root, KBDirName) }

// ManifestPath 索引文件路径。
func (c *Config) ManifestPath() string { return filepath.Join(c.KBDir(), ManifestName) }

// StateDir 工具状态目录。
func (c *Config) StateDir() string { return filepath.Join(c.Root, ".kb") }

// ConfigPath 配置文件路径。
func (c *Config) ConfigPath() string { return filepath.Join(c.Root, ConfigRel) }

// OutboxPath 服务不可达时 hook 事件的落盘队列。
func (c *Config) OutboxPath() string { return filepath.Join(c.StateDir(), "outbox.jsonl") }

// WebhookStatePath 幂等状态文件。
func (c *Config) WebhookStatePath() string { return filepath.Join(c.StateDir(), "webhook-state.json") }

// Repo 按名查找仓库。
func (c *Config) Repo(name string) *RepoConfig {
	for index := range c.Repos {
		if c.Repos[index].Name == name {
			return &c.Repos[index]
		}
	}
	return nil
}

// UpsertRepo 新增或更新仓库配置。
func (c *Config) UpsertRepo(repo RepoConfig) {
	for index := range c.Repos {
		if c.Repos[index].Name == repo.Name {
			c.Repos[index] = repo
			return
		}
	}
	c.Repos = append(c.Repos, repo)
}

// RemoveRepo 删除仓库配置。
func (c *Config) RemoveRepo(name string) bool {
	for index := range c.Repos {
		if c.Repos[index].Name == name {
			c.Repos = append(c.Repos[:index], c.Repos[index+1:]...)
			return true
		}
	}
	return false
}

// GuessRepo 按路径包含关系猜测所属仓库（用于从 cwd 推断锚点）。
func (c *Config) GuessRepo(target string) *RepoConfig {
	abs := ExpandUser(target)
	var best *RepoConfig
	bestLen := -1
	for index := range c.Repos {
		root := c.Repos[index].AbsPath()
		if root == abs || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			if len(root) > bestLen {
				best = &c.Repos[index]
				bestLen = len(root)
			}
		}
	}
	return best
}

// RenderCommitMessage 套用 commit message 模板。
func (c *Config) RenderCommitMessage(action, entryID string) string {
	template := c.Git.CommitMessage
	if strings.TrimSpace(template) == "" {
		template = DefaultCommitMessage
	}
	if entryID == "" {
		entryID = "-"
	}
	message := strings.ReplaceAll(template, "{action}", action)
	message = strings.ReplaceAll(message, "{id}", entryID)
	return strings.TrimSpace(message)
}

// Save 写回配置文件。
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.ConfigPath()), 0o755); err != nil {
		return err
	}
	payload, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(c.ConfigPath(), payload, 0o644)
}

// Load 从知识库根目录读取配置。
func Load(root string) (*Config, error) {
	resolved := ExpandUser(root)
	raw, err := os.ReadFile(filepath.Join(resolved, ConfigRel))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s 不是知识库根目录（缺少 %s）：%w", resolved, ConfigRel, ErrNotFound)
		}
		return nil, err
	}
	cfg := Default(resolved)
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", ConfigRel, err)
	}
	cfg.Root = resolved
	if cfg.Server.Host == "" {
		cfg.Server.Host = "127.0.0.1"
	}
	if cfg.Server.WebhookPort == 0 {
		cfg.Server.WebhookPort = 7777
	}
	if cfg.Server.UIPort == 0 {
		cfg.Server.UIPort = 7778
	}
	if cfg.Git.CommitMessage == "" {
		cfg.Git.CommitMessage = DefaultCommitMessage
	}
	for index := range cfg.Repos {
		if cfg.Repos[index].PrimaryBranch == "" {
			cfg.Repos[index].PrimaryBranch = "main"
		}
	}
	return cfg, nil
}

// IsKBRoot 判断目录是否为知识库根。
func IsKBRoot(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ConfigRel))
	return err == nil && !info.IsDir()
}

// FindRoot 定位知识库根：显式路径 → KB_ROOT → 向上遍历 → ~/.config/kb/root。
func FindRoot(explicit string) (string, error) {
	if explicit != "" {
		resolved := ExpandUser(explicit)
		if IsKBRoot(resolved) {
			return resolved, nil
		}
		return "", fmt.Errorf("%s 不是知识库根目录（缺少 %s）", resolved, ConfigRel)
	}

	if env := os.Getenv(EnvKBRoot); env != "" {
		resolved := ExpandUser(env)
		if IsKBRoot(resolved) {
			return resolved, nil
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		current := cwd
		for {
			if IsKBRoot(current) {
				return current, nil
			}
			parent := filepath.Dir(current)
			if parent == current {
				break
			}
			current = parent
		}
	}

	if pointer := pointerPath(); pointer != "" {
		if raw, err := os.ReadFile(pointer); err == nil {
			candidate := ExpandUser(strings.TrimSpace(string(raw)))
			if IsKBRoot(candidate) {
				return candidate, nil
			}
		}
	}
	return "", ErrNotFound
}

// RememberRoot 记下默认知识库，方便任意 cwd 下直接使用 kb。
func RememberRoot(root string) error {
	pointer := pointerPath()
	if pointer == "" {
		return errors.New("无法定位用户配置目录")
	}
	if err := os.MkdirAll(filepath.Dir(pointer), 0o755); err != nil {
		return err
	}
	return os.WriteFile(pointer, []byte(ExpandUser(root)+"\n"), 0o644)
}

func pointerPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "kb", "root")
}

// ExpandUser 展开 ~ 并返回绝对路径（不要求路径存在）。
func ExpandUser(path string) string {
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
