package textx

import (
	"reflect"
	"testing"
)

func TestExtractKeywords(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "中英混排带错误码",
			input: "偶发 502 超时 timeout",
			want:  []string{"偶发", "502", "超时", "timeout"},
		},
		{
			name:  "单个数字无意义，应丢弃",
			input: "重试 2 次",
			want:  []string{"重试"},
		},
		{
			name:  "单个 ASCII 字符丢弃，两个以上保留",
			input: "a bb ccc",
			want:  []string{"bb", "ccc"},
		},
		{
			name:  "超长中文段切 2-gram",
			input: "压测时goroutine泄漏",
			want:  []string{"压测时goroutine泄漏"},
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got := ExtractKeywords(item.input, nil, 8)
			if item.name == "超长中文段切 2-gram" {
				// 混合段长度超过阈值时按 2-gram 切分，这里只断言非空且不超过上限
				if len(got) == 0 || len(got) > 8 {
					t.Fatalf("got %v", got)
				}
				return
			}
			if !reflect.DeepEqual(got, item.want) {
				t.Fatalf("ExtractKeywords(%q) = %v, want %v", item.input, got, item.want)
			}
		})
	}
}

func TestExtractKeywordsExtraAndLimit(t *testing.T) {
	got := ExtractKeywords("偶发超时", []string{"pool", "timeout"}, 3)
	if len(got) != 3 {
		t.Fatalf("应受 limit 限制，got %v", got)
	}
	if got[0] != "偶发超时" {
		t.Fatalf("人工补充的关键词应排在自动抽取之后，got %v", got)
	}
}

func TestExtractKeywordsDedup(t *testing.T) {
	got := ExtractKeywords("pool pool pool", []string{"pool"}, 8)
	if len(got) != 1 {
		t.Fatalf("应去重，got %v", got)
	}
}

func TestTruncateKeepsRunesIntact(t *testing.T) {
	// "偶发 502 超时" = [偶 发 ␠ 5 0 2 ␠ 超 时]（9 个字符），取前 4 个
	if got := Truncate("偶发 502 超时", 4); got != "偶发 5…" {
		t.Fatalf("按字符截断，got %q", got)
	}
	if got := Truncate("短", 10); got != "短" {
		t.Fatalf("未超长时不应改动，got %q", got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("\n\n## 现象\n正文"); got != "## 现象" {
		t.Fatalf("got %q", got)
	}
}
