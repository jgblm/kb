// Package yamlx 处理 md frontmatter 的读写。
//
// manifest 用 yaml.v3 直接序列化；frontmatter 则手工渲染，
// 目的是让 frontmatter 保持人类可读（symptom: 偶发 502 超时，
// 而不是给每个值都套上引号），同时保证含特殊字符的值仍能被正确解析。
package yamlx

import (
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func itoa(v int) string { return strconv.Itoa(v) }

func trimFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// Field frontmatter 的一个字段。Value 支持 string / []string / int / bool / nil。
type Field struct {
	Key   string
	Value any
}

// quote 输出 YAML 双引号风格字符串（比 json.Marshal 更干净：不转义 < > &）。
func quote(s string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

const bareScalarChars = `-?:,[]{}#&*!|>'"%@` + "`"

// Scalar 把标量渲染成安全且尽量简洁的 YAML 标量文本。
func Scalar(value any) string {
	switch v := value.(type) {
	case nil:
		return "null"
	case bool:
		if v {
			return "true"
		}
		return "false"
	case int:
		return itoa(v)
	case int64:
		return itoa(int(v))
	case float64:
		return trimFloat(v)
	case []string:
		return InlineList(v)
	case string:
		return scalarString(v)
	default:
		return "null"
	}
}

func scalarString(s string) string {
	if s == "" {
		return `""`
	}
	if s != strings.TrimSpace(s) {
		return quote(s)
	}
	if strings.ContainsAny(s, "\n\r\t") {
		return quote(s)
	}
	if strings.Contains(s, ": ") || strings.Contains(s, " #") {
		return quote(s)
	}
	if strings.ContainsRune(bareScalarChars, rune(s[0])) {
		return quote(s)
	}
	// 会被 YAML 解析成非字符串的值，必须加引号
	if looksTyped(s) {
		return quote(s)
	}
	return s
}

func looksTyped(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~", ".nan", ".inf", "-.inf", "+.inf":
		return true
	}
	trimmed := strings.TrimLeft(s, "+-")
	if trimmed == "" {
		return false
	}
	lower := strings.ToLower(trimmed)
	if len(lower) > 2 && (strings.HasPrefix(lower, "0x") || strings.HasPrefix(lower, "0b") || strings.HasPrefix(lower, "0o")) {
		return true
	}
	for _, r := range trimmed {
		if (r >= '0' && r <= '9') || r == '.' || r == '_' || r == 'e' || r == 'E' {
			continue
		}
		return false
	}
	return true
}

// InlineList 渲染成 [a, b, c] 风格的行内列表。
func InlineList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, scalarString(item))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// DumpFrontmatter 渲染出 --- ... --- 文本块（以换行结尾）。
//
// 空值一律省略（nil / 空字符串），但显式传入的空列表会渲染成 []，
// 因为 `keywords: []` 与「字段缺失」在语义上确实不同。
func DumpFrontmatter(fields []Field) string {
	var builder strings.Builder
	builder.WriteString("---\n")
	for _, field := range fields {
		if field.Value == nil {
			continue
		}
		if text, ok := field.Value.(string); ok && text == "" {
			continue
		}
		if list, ok := field.Value.([]string); ok && len(list) == 0 {
			builder.WriteString(field.Key + ": []\n")
			continue
		}
		builder.WriteString(field.Key + ": " + Scalar(field.Value) + "\n")
	}
	builder.WriteString("---\n")
	return builder.String()
}

// SplitFrontmatter 把 md 文本拆成 (frontmatter, body)。
// 无 frontmatter 时返回 (nil, 原文, nil)。
func SplitFrontmatter(text string) (map[string]any, string, error) {
	if !strings.HasPrefix(text, "---") {
		return nil, text, nil
	}
	lines := splitLinesKeepEnds(text)
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r\n") != "---" {
		return nil, text, nil
	}

	end := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimRight(lines[index], "\r\n") == "---" {
			end = index
			break
		}
	}
	if end < 0 {
		return nil, text, nil
	}

	raw := strings.Join(lines[1:end], "")
	fields := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := yaml.Unmarshal([]byte(raw), &fields); err != nil {
			return nil, text, err
		}
	}
	body := strings.TrimLeft(strings.Join(lines[end+1:], ""), "\n")
	return fields, body, nil
}

// JoinFrontmatter 把 frontmatter 与正文拼成完整 md。
func JoinFrontmatter(fields []Field, body string) string {
	return DumpFrontmatter(fields) + "\n" + strings.TrimSpace(body) + "\n"
}

func splitLinesKeepEnds(text string) []string {
	var lines []string
	start := 0
	for index := 0; index < len(text); index++ {
		if text[index] == '\n' {
			lines = append(lines, text[start:index+1])
			start = index + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}
