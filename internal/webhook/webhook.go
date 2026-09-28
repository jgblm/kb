// Package webhook 实现变更通知接收器（需求 FR-3.1 / FR-3.2）。
//
// 协议（完整说明见 docs/webhook-protocol.md）：
//
//	POST /webhook
//	{
//	  "repo": "auth-service",
//	  "branch": "main",
//	  "commit": "a1b2c3d",
//	  "event": "post-commit",
//	  "files": ["pkg/db/pool.go"],
//	  "ts": 1769590000
//	}
//
// 处理规则：
//  1. 只处理主干分支（repo.primary_branch）的变更
//  2. 幂等：同一 (repo, commit) 重复通知只处理一次
//  3. 忽略 repo.ignore 中匹配的文件
//  4. 只标记 stale，绝不改写人工正文
package webhook

import (
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/stone/kb/internal/kb"
)

// Payload git hook 上报的变更通知。
type Payload struct {
	Repo      string   `json:"repo"`
	Branch    string   `json:"branch"`
	Commit    string   `json:"commit"`
	Event     string   `json:"event,omitempty"`
	Files     []string `json:"files"`
	Timestamp int64    `json:"ts,omitempty"`
}

// Response 处理结果。
type Response struct {
	OK         bool          `json:"ok"`
	Repo       string        `json:"repo"`
	Commit     string        `json:"commit,omitempty"`
	Branch     string        `json:"branch,omitempty"`
	Skipped    string        `json:"skipped,omitempty"`
	Idempotent bool          `json:"idempotent,omitempty"`
	Scanned    int           `json:"scanned_files"`
	Matched    int           `json:"matched"`
	Stale      []kb.EntryRef `json:"stale"`
	Recovered  []kb.EntryRef `json:"recovered,omitempty"`
	DurationMS int64         `json:"duration_ms"`
	Error      string        `json:"error,omitempty"`
}

// Handler 处理 /webhook 请求。
type Handler struct {
	KB    *kb.KB
	State *State

	mu sync.Mutex
}

// NewHandler 构造处理器。
func NewHandler(knowledgeBase *kb.KB) *Handler {
	return &Handler{
		KB:    knowledgeBase,
		State: LoadState(knowledgeBase.Config.WebhookStatePath()),
	}
}

// ServeHTTP 实现 http.Handler。
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()

	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, Response{
			OK:    false,
			Error: "只接受 POST",
		})
		return
	}

	if token := h.KB.Config.Server.Token; token != "" {
		if request.Header.Get("X-KB-Token") != token {
			writeJSON(writer, http.StatusUnauthorized, Response{OK: false, Error: "token 校验失败"})
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(request.Body, 8<<20))
	if err != nil {
		writeJSON(writer, http.StatusBadRequest, Response{OK: false, Error: "读取请求体失败: " + err.Error()})
		return
	}

	var payload Payload
	if err := json.Unmarshal(body, &payload); err != nil {
		writeJSON(writer, http.StatusBadRequest, Response{OK: false, Error: "请求体不是合法 JSON: " + err.Error()})
		return
	}

	response := h.Handle(payload)
	response.DurationMS = time.Since(started).Milliseconds()

	status := http.StatusOK
	if response.Error != "" {
		status = http.StatusBadRequest
	}
	writeJSON(writer, status, response)
}

// Handle 执行一次变更检测（不涉及 HTTP）。
//
// 契约 —— 调用方（git hook / outbox 补投 / 测试）需要知道的全部：
//
//   - 只改条目的 status 与 stale_reason，绝不触碰正文，也不删条目
//   - 非主干分支、重复 commit 一律跳过，返回 ok:true 并带 skipped / idempotent
//   - 已处理的 commit 记在 .kb/webhook-state.json，按仓库保留最近 seenLimit 个
//
// 已知限制：Seen 检查与 Mark 之间存在一个极小的竞态窗口，两个同时到达的
// 同一 commit 可能都被处理。实践中 git hook 按仓库串行触发，且重复处理的
// 后果只是多跑一次幂等的哈希比对，因此不把锁跨越整个处理流程。
func (h *Handler) Handle(payload Payload) Response {
	response := Response{OK: true, Repo: payload.Repo, Commit: payload.Commit, Branch: payload.Branch}

	if strings.TrimSpace(payload.Repo) == "" {
		response.OK = false
		response.Error = "缺少 repo 字段"
		return response
	}

	repo := h.KB.Config.Repo(payload.Repo)
	if repo == nil {
		response.OK = false
		response.Error = "仓库未在 kb 配置中：" + payload.Repo
		return response
	}

	// 规则 1：只处理主干分支
	primary := repo.PrimaryBranch
	if primary == "" {
		primary = "main"
	}
	if payload.Branch != "" && payload.Branch != primary {
		response.Skipped = "非主干分支（" + payload.Branch + "，主干为 " + primary + "）"
		return response
	}

	// 规则 2：幂等
	h.mu.Lock()
	if payload.Commit != "" && h.State.Seen(payload.Repo, payload.Commit) {
		h.mu.Unlock()
		response.Idempotent = true
		response.Skipped = "该 commit 已处理过"
		return response
	}
	h.mu.Unlock()

	// 规则 3：过滤忽略文件
	files := filterFiles(payload.Files, repo.Ignore)
	response.Scanned = len(files)

	report, err := h.KB.ApplyChange(payload.Repo, files)
	if err != nil {
		response.OK = false
		response.Error = err.Error()
		return response
	}

	response.Matched = report.Matched
	response.Stale = report.Stale
	response.Recovered = report.Recovered
	if response.Stale == nil {
		response.Stale = []kb.EntryRef{}
	}

	h.mu.Lock()
	h.State.Mark(payload.Repo, payload.Commit, payload.Branch)
	saveErr := h.State.Save(h.KB.Config.WebhookStatePath())
	h.mu.Unlock()
	if saveErr != nil {
		response.Error = "状态落盘失败: " + saveErr.Error()
	}

	// 需求 FR-3.4：只提醒，不自动修改
	return response
}

// filterFiles 去掉忽略模式命中的文件与空项。
func filterFiles(files, ignore []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, file := range files {
		name := strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
		if name == "" || seen[name] {
			continue
		}
		skipped := false
		for _, pattern := range ignore {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if matched, _ := path.Match(pattern, name); matched {
				skipped = true
				break
			}
			if matched, _ := path.Match(pattern, path.Base(name)); matched {
				skipped = true
				break
			}
		}
		if skipped {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(payload)
}
