// Package textx 文本处理小工具：关键词抽取、字符串归一化。
//
// 关键词抽取是「症状检索」的前置环节。这里用规则法（无外部词典、无 cgo），
// 意图是够用即可：
//
//	ASCII 词       长度 >= 2 保留
//	数字           长度 >= 3 保留（502 / 404 这类错误码有意义，单个 2 无意义）
//	中文连续段      2..8 字整体保留；更长则切 2-gram（上限 4 个）
//
// 若后续需要更准的中文切词，可换 jieba/gojieba，或在 v2 引入 TF-IDF，
// 调用点只需替换本包实现。
package textx

import (
	"strings"
	"unicode"

	"github.com/stone/kb/internal/pathmap"
)

const (
	maxCJKRun   = 8
	maxShingles = 4
)

// ExtractKeywords 从文本抽关键词，去重保序，最多 limit 个。
func ExtractKeywords(text string, extra []string, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	var results []string
	seen := map[string]bool{}

	add := func(token string) {
		if token == "" || seen[token] {
			return
		}
		seen[token] = true
		results = append(results, token)
	}

	for _, run := range runs(text) {
		switch {
		case allCJK(run):
			runes := []rune(run)
			if len(runes) <= maxCJKRun {
				if len(runes) >= 2 {
					add(run)
				}
				continue
			}
			count := 0
			for i := 0; i+2 <= len(runes) && count < maxShingles; i += 2 {
				add(string(runes[i : i+2]))
				count++
			}
		case allDigits(run):
			if len(run) >= 3 {
				add(run)
			}
		default:
			if len([]rune(run)) >= 2 {
				add(strings.ToLower(run))
			}
		}
	}

	for _, item := range extra {
		add(strings.TrimSpace(item))
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results
}

// runs 把文本切成连续的「字母/数字/下划线」段，其余字符视为分隔符。
func runs(text string) []string {
	var out []string
	var current []rune
	flush := func() {
		if len(current) > 0 {
			out = append(out, string(current))
			current = current[:0]
		}
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			current = append(current, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func allCJK(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.Is(unicode.Han, r) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// HasCJK 透出给 CLI 判断是否需要中文友好的输出对齐。
func HasCJK(s string) bool { return pathmap.HasCJK(s) }

// Truncate 按字符数截断，超出加省略号。
func Truncate(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// FirstLine 取第一行非空内容。
func FirstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
