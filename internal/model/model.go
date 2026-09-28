// Package model 定义知识库的数据模型（需求 §4 + §10）。
//
// 对需求文档中一处矛盾的修正
//
// 文档 §3.1.3 写 confidence: verified | unverified，
// 而 §10.2 把 stale 也塞进了 confidence 的取值里。
// 二者语义不同，混在一个字段会无法同时表达「代码变了」与「我没验证过」，
// 因此拆成两个正交字段：
//
//	Confidence  人工声明的主观可信度   verified | unverified
//	Status      机器推导的时效状态     fresh | stale
//
// Status 只能由哈希比对引擎改写，人类不手改；Confidence 只由人类声明。
package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/stone/kb/internal/yamlx"
)

// EntryType 知识类型（需求 §10.1）。
type EntryType string

const (
	TypeIncident   EntryType = "incident"   // 事故 / 踩坑
	TypeDecision   EntryType = "decision"   // 设计决策
	TypeConstraint EntryType = "constraint" // 隐性约束
	TypeNote       EntryType = "note"       // 一般笔记
)

// EntryTypes 按稳定顺序列出全部类型（供 CLI 提示与 schema 使用）。
var EntryTypes = []EntryType{TypeIncident, TypeDecision, TypeConstraint, TypeNote}

// ParseType 宽容解析，未知值回落为 note。
func ParseType(raw string) EntryType {
	v := EntryType(strings.ToLower(strings.TrimSpace(raw)))
	for _, t := range EntryTypes {
		if v == t {
			return v
		}
	}
	return TypeNote
}

// Confidence 人工声明的可信度。
type Confidence string

const (
	// ConfidenceVerified 已验证，可信。
	ConfidenceVerified Confidence = "verified"
	// ConfidenceUnverified 未验证，仅供参考。
	ConfidenceUnverified Confidence = "unverified"
)

// ParseConfidence 宽容解析，默认 unverified。
func ParseConfidence(raw string) Confidence {
	switch Confidence(strings.ToLower(strings.TrimSpace(raw))) {
	case ConfidenceVerified:
		return ConfidenceVerified
	default:
		return ConfidenceUnverified
	}
}

// Status 由哈希比对引擎推导的时效状态。
type Status string

const (
	// StatusFresh 代码未变或条目无锚点。
	StatusFresh Status = "fresh"
	// StatusStale 锚定代码已变更。
	StatusStale Status = "stale"
)

// ParseStatus 宽容解析，默认 fresh。
func ParseStatus(raw string) Status {
	if Status(strings.ToLower(strings.TrimSpace(raw))) == StatusStale {
		return StatusStale
	}
	return StatusFresh
}

// 过期原因常量。
const (
	ReasonContentChanged    = "content-changed"
	ReasonFileMissing       = "file-missing"
	ReasonRepoNotConfigured = "repo-not-configured"
)

// Anchor 代码锚点。
type Anchor struct {
	Repo   string `yaml:"repo" json:"repo"`
	Path   string `yaml:"path" json:"path"`
	Commit string `yaml:"commit,omitempty" json:"commit,omitempty"`
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
	// Hash 记录时的整文件摘要，形如 sha256:ab12...
	Hash  string `yaml:"hash,omitempty" json:"hash,omitempty"`
	Lines string `yaml:"lines,omitempty" json:"lines,omitempty"`
}

// Ref 人类可读的一行引用：auth-service/pkg/db/pool.go:120-180
func (a *Anchor) Ref() string {
	if a == nil {
		return ""
	}
	base := a.Repo + "/" + a.Path
	if a.Lines != "" {
		return base + ":" + a.Lines
	}
	return base
}

// Entry 一条知识。索引信息进 manifest，正文进 md。
type Entry struct {
	ID         string     `yaml:"id" json:"id"`
	Symptom    string     `yaml:"symptom" json:"symptom"`
	Keywords   []string   `yaml:"keywords" json:"keywords"`
	Type       EntryType  `yaml:"type" json:"type"`
	Confidence Confidence `yaml:"confidence" json:"confidence"`
	Status     Status     `yaml:"status" json:"status"`
	Anchor     *Anchor    `yaml:"anchor,omitempty" json:"anchor,omitempty"`
	// DocPath 相对 <kb-root>/kb/ 的 POSIX 路径。
	DocPath   string `yaml:"doc_path" json:"doc_path"`
	CreatedAt string `yaml:"created_at" json:"created_at"`
	UpdatedAt string `yaml:"updated_at" json:"updated_at"`
	// 时效性元数据（机器写入）
	StaleSince  string `yaml:"stale_since,omitempty" json:"stale_since,omitempty"`
	StaleReason string `yaml:"stale_reason,omitempty" json:"stale_reason,omitempty"`
	// DriftedFrom 代码文件改名后，记录漂移前的路径。
	DriftedFrom string `yaml:"drifted_from,omitempty" json:"drifted_from,omitempty"`
}

// Touch 刷新 updated_at。
func (e *Entry) Touch() { e.UpdatedAt = NowISO() }

// MarkStale 标记过期。返回是否发生了状态变化（幂等）。
func (e *Entry) MarkStale(reason string) bool {
	if e.Status == StatusStale && e.StaleReason == reason {
		return false
	}
	e.Status = StatusStale
	e.StaleReason = reason
	if e.StaleSince == "" {
		e.StaleSince = NowISO()
	}
	e.Touch()
	return true
}

// MarkFresh 恢复新鲜，并可刷新基线哈希。
func (e *Entry) MarkFresh(newHash string) bool {
	changed := e.Status != StatusFresh
	e.Status = StatusFresh
	e.StaleReason = ""
	e.StaleSince = ""
	if newHash != "" && e.Anchor != nil {
		e.Anchor.Hash = newHash
	}
	e.Touch()
	return changed
}

// NowISO ISO-8601 UTC，秒精度，带 Z。
func NowISO() string {
	return time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
}

// Today 返回 UTC 日期，用于生成条目 id 前缀。
func Today() string { return time.Now().UTC().Format("2006-01-02") }

// Validate 结构性校验，error 为 nil 表示通过。
func (e *Entry) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return fmt.Errorf("条目缺少 id")
	}
	if strings.TrimSpace(e.Symptom) == "" {
		return fmt.Errorf("%s: 缺少 symptom", e.ID)
	}
	if strings.TrimSpace(e.DocPath) == "" {
		return fmt.Errorf("%s: 缺少 doc_path", e.ID)
	}
	if e.Anchor != nil {
		if e.Anchor.Repo == "" || e.Anchor.Path == "" {
			return fmt.Errorf("%s: anchor 需要 repo 与 path", e.ID)
		}
		if e.Anchor.Hash != "" && !strings.HasPrefix(e.Anchor.Hash, "sha256:") {
			return fmt.Errorf("%s: anchor.hash 必须是 sha256: 前缀", e.ID)
		}
	}
	return nil
}

// Clone 深拷贝。读接口向锁外返回条目时用它，避免共享可变指针。
func (e *Entry) Clone() *Entry {
	copied := *e
	if e.Keywords != nil {
		copied.Keywords = append([]string(nil), e.Keywords...)
	}
	if e.Anchor != nil {
		anchor := *e.Anchor
		copied.Anchor = &anchor
	}
	return &copied
}

// Frontmatter 渲染成 md frontmatter 字段列表。
//
// 只放人类需要看到、且可能手改的字段；status 由机器维护，
// 但写进 frontmatter 让 `git diff` 能看出条目何时变脏。
func (e *Entry) Frontmatter() []yamlx.Field {
	fields := []yamlx.Field{
		{Key: "id", Value: e.ID},
		{Key: "symptom", Value: e.Symptom},
		{Key: "type", Value: string(e.Type)},
		{Key: "confidence", Value: string(e.Confidence)},
		{Key: "status", Value: string(e.Status)},
		{Key: "keywords", Value: e.Keywords},
	}
	if e.Anchor != nil {
		fields = append(fields,
			yamlx.Field{Key: "anchor", Value: e.Anchor.Ref()},
			yamlx.Field{Key: "anchor_hash", Value: e.Anchor.Hash},
			yamlx.Field{Key: "anchor_commit", Value: e.Anchor.Commit},
		)
	}
	if e.Status == StatusStale {
		fields = append(fields, yamlx.Field{Key: "stale_reason", Value: e.StaleReason})
	}
	fields = append(fields,
		yamlx.Field{Key: "created_at", Value: e.CreatedAt},
		yamlx.Field{Key: "updated_at", Value: e.UpdatedAt},
	)
	return fields
}
