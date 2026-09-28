// Package pathmap 实现路径镜像规则（需求 FR-2）。
//
//	<code-repo>/pkg/db/pool.go
//	        ↓
//	<kb>/<repo-name>/pkg/db/pool.md
//	<kb>/<repo-name>/pkg/db/pool.assets/
//
// 全部使用 POSIX 语义（path 包，而非 filepath），保证在 Windows 上生成的
// manifest 与 macOS/Linux 一致，可跨平台共享知识库。
package pathmap

import (
	"path"
	"regexp"
	"strings"
	"unicode"
)

// AssetsSuffix 位图/附件的目录后缀。
const AssetsSuffix = ".assets"

// GlobalDirs 自由目录：不参与路径镜像的全局知识。
var GlobalDirs = []string{"incidents", "decisions", "cross-cutting"}

var (
	// 注意：Go 的 RE2 不支持 \uXXXX，Unicode 码点要写成 \x{XXXX}
	nonSlug    = regexp.MustCompile(`[^0-9a-zA-Z\x{4e00}-\x{9fff}]+`)
	multiDash  = regexp.MustCompile(`-{2,}`)
	knownMulti = map[string]bool{".min.js": true}
)

// Normalize 归一化为 POSIX 相对路径（去掉 ./ 前缀与反斜杠）。
func Normalize(p string) string {
	clean := strings.ReplaceAll(p, "\\", "/")
	clean = strings.TrimPrefix(clean, "./")
	return strings.Trim(clean, "/")
}

// SplitExt 拆出「去掉最后一层扩展名」的文件名。
//
//	pool.go      -> pool
//	pool.test.ts -> pool.test
//	Makefile     -> Makefile
func SplitExt(name string) string {
	ext := path.Ext(name)
	if ext == "" || knownMulti[ext] {
		return name
	}
	return strings.TrimSuffix(name, ext)
}

// MirrorDocPath 代码相对路径 -> 知识文档相对路径（相对 kb/ 目录）。
func MirrorDocPath(repo, codeRelPath string) string {
	rel := Normalize(codeRelPath)
	dir := path.Dir(rel)
	name := SplitExt(path.Base(rel))
	parts := []string{repo}
	if dir != "." && dir != "" {
		parts = append(parts, dir)
	}
	parts = append(parts, name+".md")
	return path.Join(parts...)
}

// AssetsDirFor a/b/pool.md -> a/b/pool.assets
func AssetsDirFor(docRelPath string) string {
	rel := Normalize(docRelPath)
	return path.Join(path.Dir(rel), SplitExt(path.Base(rel))+AssetsSuffix)
}

// SplitDocPath auth-service/pkg/db/pool.md -> ("auth-service", "pkg/db/pool.md")
//
// 全局目录（incidents/ 等）返回空 repo。
func SplitDocPath(docRelPath string) (repo, rel string) {
	normalized := Normalize(docRelPath)
	parts := strings.Split(normalized, "/")
	if len(parts) < 2 {
		return "", normalized
	}
	for _, globalDir := range GlobalDirs {
		if parts[0] == globalDir {
			return "", normalized
		}
	}
	return parts[0], strings.Join(parts[1:], "/")
}

// DocToCodeGlob 由文档路径反推代码路径的 glob（漂移兜底用）。
//
//	auth-service/pkg/db/pool.md -> ("auth-service", "pkg/db/pool.*")
func DocToCodeGlob(docRelPath string) (repo, glob string, ok bool) {
	repoName, rel := SplitDocPath(docRelPath)
	if repoName == "" {
		return "", "", false
	}
	dir := path.Dir(rel)
	stem := SplitExt(path.Base(rel))
	if dir == "." || dir == "" {
		return repoName, stem + ".*", true
	}
	return repoName, dir + "/" + stem + ".*", true
}

// Under child 是否位于 parent 目录下。
func Under(child, parent string) bool {
	c := strings.Split(Normalize(child), "/")
	p := strings.Split(Normalize(parent), "/")
	if len(c) < len(p) {
		return false
	}
	for i := range p {
		if c[i] != p[i] {
			return false
		}
	}
	return true
}

// Slugify 症状 -> 短 slug。保留中文，其余非字母数字折叠为连字符。
func Slugify(text string, limit int) string {
	lowered := strings.ToLower(strings.TrimSpace(text))
	slug := strings.Trim(nonSlug.ReplaceAllString(lowered, "-"), "-")
	slug = multiDash.ReplaceAllString(slug, "-")
	if slug == "" {
		return "entry"
	}
	runes := []rune(slug)
	if limit > 0 && len(runes) > limit {
		slug = strings.Trim(string(runes[:limit]), "-")
	}
	if slug == "" {
		return "entry"
	}
	return slug
}

// HasCJK 判断字符串是否含中日韩字符（关键词抽取用）。
func HasCJK(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) {
			return true
		}
	}
	return false
}
