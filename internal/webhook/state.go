package webhook

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// seenLimit 每个仓库保留的已处理 commit 数量（幂等窗口）。
const seenLimit = 64

// RepoState 单个仓库的通知状态。
type RepoState struct {
	LastCommit string   `json:"last_commit,omitempty"`
	LastBranch string   `json:"last_branch,omitempty"`
	LastAt     string   `json:"last_at,omitempty"`
	Seen       []string `json:"seen,omitempty"`
}

// State 幂等状态（落盘在 .kb/webhook-state.json）。
type State struct {
	Repos map[string]*RepoState `json:"repos"`
}

// LoadState 读取状态文件，不存在则返回空状态。
func LoadState(path string) *State {
	state := &State{Repos: map[string]*RepoState{}}
	raw, err := os.ReadFile(path)
	if err != nil {
		return state
	}
	if err := json.Unmarshal(raw, state); err != nil {
		return &State{Repos: map[string]*RepoState{}}
	}
	if state.Repos == nil {
		state.Repos = map[string]*RepoState{}
	}
	return state
}

// Seen 该 commit 是否已处理过。
func (s *State) Seen(repo, commit string) bool {
	if commit == "" {
		return false
	}
	entry, ok := s.Repos[repo]
	if !ok {
		return false
	}
	for _, item := range entry.Seen {
		if item == commit {
			return true
		}
	}
	return false
}

// Mark 记录一次已处理的 commit。
func (s *State) Mark(repo, commit, branch string) {
	if s.Repos == nil {
		s.Repos = map[string]*RepoState{}
	}
	entry, ok := s.Repos[repo]
	if !ok {
		entry = &RepoState{}
		s.Repos[repo] = entry
	}
	entry.LastCommit = commit
	entry.LastBranch = branch
	entry.LastAt = time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
	if commit == "" {
		return
	}
	for _, item := range entry.Seen {
		if item == commit {
			return
		}
	}
	entry.Seen = append(entry.Seen, commit)
	if len(entry.Seen) > seenLimit {
		entry.Seen = entry.Seen[len(entry.Seen)-seenLimit:]
	}
}

// Save 原子写状态文件。
func (s *State) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// AppendOutbox 服务不可达时把事件追加到落盘队列。
//
// 这是对需求的一处健壮性补强：git hook 在服务未启动时不应静默丢事件。
func AppendOutbox(path string, payload Payload) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if payload.Timestamp == 0 {
		payload.Timestamp = time.Now().Unix()
	}
	line, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	handle, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer handle.Close()
	if _, err := handle.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

// ReadOutbox 读取队列内容。
func ReadOutbox(path string) ([]Payload, error) {
	handle, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer handle.Close()

	var payloads []Payload
	scanner := bufio.NewScanner(handle)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var payload Payload
		if err := json.Unmarshal([]byte(line), &payload); err != nil {
			continue
		}
		payloads = append(payloads, payload)
	}
	return payloads, scanner.Err()
}

// ClearOutbox 清空队列。
func ClearOutbox(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
