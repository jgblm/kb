package yamlx

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestScalarBareWhenSafe(t *testing.T) {
	cases := map[string]string{
		"偶发 502 超时":  "偶发 502 超时",
		"sha256:abc": `"sha256:abc"`, // 含 ": " 之外的冒号其实安全，这里验证冒号紧跟非空格
	}
	// 含 ": " 必须加引号
	if got := Scalar("a: b"); got != `"a: b"` {
		t.Errorf("含 ': ' 的值必须加引号，got %s", got)
	}
	// 看起来像数字的字符串必须加引号，否则会被解析成 int
	for _, raw := range []string{"502", "1.5", "true", "null", "0x1f", "~"} {
		if got := Scalar(raw); !strings.HasPrefix(got, `"`) {
			t.Errorf("%q 会被 YAML 解析成非字符串，必须加引号，got %s", raw, got)
		}
	}
	_ = cases
}

func TestFrontmatterRoundTrip(t *testing.T) {
	fields := []Field{
		{Key: "id", Value: "2026-01-15-pool-leak"},
		{Key: "symptom", Value: "偶发 502 超时"},
		{Key: "type", Value: "incident"},
		{Key: "confidence", Value: "verified"},
		{Key: "status", Value: "fresh"},
		{Key: "keywords", Value: []string{"502", "超时", "连接池"}},
		{Key: "anchor", Value: "auth-service/pkg/db/pool.go:120-180"},
		{Key: "anchor_hash", Value: "sha256:" + strings.Repeat("ab", 32)},
		{Key: "created_at", Value: "2026-01-15T10:00:00Z"},
		{Key: "empty", Value: ""},    // 应被省略
		{Key: "nothing", Value: nil}, // 应被省略
	}
	doc := JoinFrontmatter(fields, "## 现象\n\n线上偶发 502。\n")

	if strings.Contains(doc, "empty:") || strings.Contains(doc, "nothing:") {
		t.Fatalf("空值不应写入 frontmatter：\n%s", doc)
	}

	parsed, body, err := SplitFrontmatter(doc)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if parsed["symptom"] != "偶发 502 超时" {
		t.Errorf("symptom 往返失败: %#v", parsed["symptom"])
	}
	if parsed["anchor"] != "auth-service/pkg/db/pool.go:120-180" {
		t.Errorf("anchor 往返失败: %#v", parsed["anchor"])
	}
	keywords, ok := parsed["keywords"].([]any)
	if !ok || len(keywords) != 3 {
		t.Fatalf("keywords 往返失败: %#v", parsed["keywords"])
	}
	if body != "## 现象\n\n线上偶发 502。\n" {
		t.Errorf("正文往返失败: %q", body)
	}
}

func TestFrontmatterSpecialCharacters(t *testing.T) {
	tricky := []string{
		"含: 冒号的值",
		"# 井号开头",
		"- 破折号开头",
		"值里有 # 注释符",
		"引号 \" 与 ' 单引号",
		"末尾有空格 ",
		"502",
		"true",
		"中文与 english 混排",
	}
	for _, value := range tricky {
		doc := JoinFrontmatter([]Field{{Key: "symptom", Value: value}}, "body")
		parsed, _, err := SplitFrontmatter(doc)
		if err != nil {
			t.Fatalf("%q 解析失败: %v\n%s", value, err, doc)
		}
		got, _ := parsed["symptom"].(string)
		if got != strings.TrimSpace(value) && got != value {
			t.Errorf("往返不一致: in=%q out=%#v\ndoc=%s", value, parsed["symptom"], doc)
		}
	}
}

func TestSplitFrontmatterWithoutHeader(t *testing.T) {
	parsed, body, err := SplitFrontmatter("没有 frontmatter 的正文")
	if err != nil || parsed != nil || body != "没有 frontmatter 的正文" {
		t.Fatalf("无 frontmatter 时应原样返回，got (%v,%q,%v)", parsed, body, err)
	}
}

func TestEmptyKeywordListIsExplicit(t *testing.T) {
	doc := DumpFrontmatter([]Field{{Key: "keywords", Value: []string{}}})
	if !strings.Contains(doc, "keywords: []") {
		t.Fatalf("空列表应渲染成 []，got %q", doc)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(strings.Trim(doc, "-\n")), &parsed); err != nil {
		t.Fatalf("渲染结果不是合法 YAML: %v\n%s", err, doc)
	}
}
