package pathmap

import "testing"

func TestMirrorDocPath(t *testing.T) {
	cases := []struct {
		name string
		repo string
		code string
		want string
	}{
		{"go 文件", "auth-service", "pkg/db/pool.go", "auth-service/pkg/db/pool.md"},
		{"仓库根文件", "auth-service", "main.go", "auth-service/main.md"},
		{"多段扩展名", "web", "src/a.test.ts", "web/src/a.test.md"},
		{"无扩展名", "ops", "Makefile", "ops/Makefile.md"},
		{"前导 ./ 与反斜杠", "auth-service", "./pkg\\db\\pool.go", "auth-service/pkg/db/pool.md"},
		{"已归一化", "r", "/a/b/c.go", "r/a/b/c.md"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := MirrorDocPath(item.repo, item.code); got != item.want {
				t.Fatalf("MirrorDocPath(%q,%q) = %q, want %q", item.repo, item.code, got, item.want)
			}
		})
	}
}

func TestAssetsDirFor(t *testing.T) {
	if got := AssetsDirFor("auth-service/pkg/db/pool.md"); got != "auth-service/pkg/db/pool.assets" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitDocPath(t *testing.T) {
	repo, rel := SplitDocPath("auth-service/pkg/db/pool.md")
	if repo != "auth-service" || rel != "pkg/db/pool.md" {
		t.Fatalf("got (%q,%q)", repo, rel)
	}
	// 全局目录不归属任何仓库
	repo, _ = SplitDocPath("incidents/2026-01-01-x.md")
	if repo != "" {
		t.Fatalf("全局目录应返回空 repo，got %q", repo)
	}
}

func TestDocToCodeGlob(t *testing.T) {
	repo, glob, ok := DocToCodeGlob("auth-service/pkg/db/pool.md")
	if !ok || repo != "auth-service" || glob != "pkg/db/pool.*" {
		t.Fatalf("got (%q,%q,%v)", repo, glob, ok)
	}
	if _, _, ok := DocToCodeGlob("decisions/x.md"); ok {
		t.Fatal("全局目录不应产生代码 glob")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"偶发 502 超时":  "偶发-502-超时",
		"Pool Leak!": "pool-leak",
		"  - -  ":    "entry",
		"a/b/c":      "a-b-c",
		"为什么用 Redis": "为什么用-redis",
	}
	for input, want := range cases {
		if got := Slugify(input, 32); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSlugifyLimit(t *testing.T) {
	got := Slugify("一二三四五六七八九十", 3)
	if len([]rune(got)) != 3 {
		t.Fatalf("应截断到 3 个字符，got %q", got)
	}
}

func TestUnder(t *testing.T) {
	if !Under("pkg/db/pool.go", "pkg") {
		t.Fatal("pkg/db/pool.go 应在 pkg 下")
	}
	if Under("pkgx/a.go", "pkg") {
		t.Fatal("pkgx 不是 pkg 的子路径（不能按前缀匹配）")
	}
}
