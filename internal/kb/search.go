package kb

import (
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/stone/kb/internal/model"
)

// 字段权重：症状命中最重，正文命中作为兜底。
const (
	weightSymptom        = 12.0
	weightKeyword        = 10.0
	weightKeywordPartial = 6.0
	weightDocPath        = 5.0
	weightAnchor         = 4.0
	weightBody           = 3.0
	bonusAllTerms        = 8.0
	bonusVerified        = 1.5
	penaltyStale         = 1.0
)

var splitRe = regexp.MustCompile(`[\s,，。;；:：/\\|()（）\[\]【】"'` + "`" + `!?！？=+*&^%$#@~<>]+`)

var asciiPieceRe = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_.\-]*|\d+`)

// Tokenize 查询切词。
//
// 中文按整段保留（子串匹配足够），英文按分隔符切；
// 中英混排时额外抽出 ASCII 片段，让 "pool连接池" 这类输入也能命中。
func Tokenize(query string) []string {
	var terms []string
	seen := map[string]bool{}
	add := func(term string) {
		lowered := strings.ToLower(term)
		if lowered == "" || seen[lowered] {
			return
		}
		seen[lowered] = true
		terms = append(terms, term)
	}
	for _, token := range splitRe.Split(strings.TrimSpace(query), -1) {
		if token == "" {
			continue
		}
		add(token)
		for _, piece := range asciiPieceRe.FindAllString(token, -1) {
			if piece != token {
				add(piece)
			}
		}
	}
	return terms
}

// Snippet 正文命中片段。
type Snippet struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Hit 一条检索结果。
type Hit struct {
	Entry    *model.Entry `json:"entry"`
	Score    float64      `json:"score"`
	Fields   []string     `json:"matched_fields"`
	Snippets []Snippet    `json:"snippets,omitempty"`
}

// SearchOptions 检索参数。
type SearchOptions struct {
	Query         string
	Limit         int
	Repo          string
	Type          model.EntryType
	Status        model.Status
	IncludeGlobal bool
}

// Search 统一检索入口（需求 FR-4）。
//
// 语义检索（向量）按需求留到 v2，届时在此处增加一个打分来源即可，
// 调用方无需改动。
func (kb *KB) Search(opts SearchOptions) []Hit {
	terms := Tokenize(opts.Query)
	if len(terms) == 0 {
		return nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	kb.mu.RLock()
	defer kb.mu.RUnlock()

	hits := make([]Hit, 0, 16)
	for _, entry := range kb.Manifest.Entries {
		if opts.Repo != "" && (entry.Anchor == nil || entry.Anchor.Repo != opts.Repo) {
			continue
		}
		if entry.Anchor == nil && !opts.IncludeGlobal {
			continue
		}
		if opts.Type != "" && entry.Type != opts.Type {
			continue
		}
		if opts.Status != "" && entry.Status != opts.Status {
			continue
		}

		body, err := kb.ReadBody(entry)
		if err != nil {
			body = ""
		}
		hit := scoreEntry(entry, terms, body)
		if hit.Score > 0 {
			hit.Entry = entry.Clone()
			hits = append(hits, hit)
		}
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Entry.ID < hits[j].Entry.ID
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

func scoreEntry(entry *model.Entry, terms []string, body string) Hit {
	hit := Hit{Fields: []string{}}

	symptom := strings.ToLower(entry.Symptom)
	docPath := strings.ToLower(entry.DocPath)
	anchorPath := ""
	if entry.Anchor != nil {
		anchorPath = strings.ToLower(entry.Anchor.Path)
	}
	bodyLower := strings.ToLower(body)
	keywords := make([]string, 0, len(entry.Keywords))
	for _, keyword := range entry.Keywords {
		keywords = append(keywords, strings.ToLower(keyword))
	}

	matchedAny := false
	symptomAll := true
	keywordMatched := false
	docMatched := false
	anchorMatched := false
	bodyMatched := false

	for _, term := range terms {
		lowered := strings.ToLower(term)
		local := false

		if strings.Contains(symptom, lowered) {
			hit.Score += weightSymptom
			local = true
		} else {
			symptomAll = false
		}

		for _, keyword := range keywords {
			if keyword == lowered {
				hit.Score += weightKeyword
				keywordMatched, local = true, true
				break
			}
			if strings.Contains(keyword, lowered) {
				hit.Score += weightKeywordPartial
				keywordMatched, local = true, true
				break
			}
		}

		if strings.Contains(docPath, lowered) {
			hit.Score += weightDocPath
			docMatched, local = true, true
		}
		if anchorPath != "" && strings.Contains(anchorPath, lowered) {
			hit.Score += weightAnchor
			anchorMatched, local = true, true
		}

		if lowered != "" {
			if occurrences := strings.Count(bodyLower, lowered); occurrences > 0 {
				hit.Score += weightBody + float64(min(occurrences-1, 3))*0.5
				bodyMatched, local = true, true
			}
		}
		matchedAny = matchedAny || local
	}

	if !matchedAny {
		return Hit{Entry: entry, Fields: []string{}}
	}
	if symptomAll && len(terms) > 1 {
		hit.Score += bonusAllTerms
	}
	if entry.Confidence == model.ConfidenceVerified {
		hit.Score += bonusVerified
	}
	if entry.Status == model.StatusStale {
		// 过期的条目降权，但仍可见
		hit.Score -= penaltyStale
	}

	if strings.Contains(symptom, strings.ToLower(terms[0])) {
		hit.Fields = append(hit.Fields, "symptom")
	}
	if keywordMatched {
		hit.Fields = append(hit.Fields, "keyword")
	}
	if docMatched {
		hit.Fields = append(hit.Fields, "doc_path")
	}
	if anchorMatched {
		hit.Fields = append(hit.Fields, "anchor")
	}
	if bodyMatched {
		hit.Fields = append(hit.Fields, "body")
		hit.Snippets = snippets(body, terms, 3, 1)
	}
	return hit
}

func snippets(body string, terms []string, limit, context int) []Snippet {
	if body == "" {
		return nil
	}
	lines := strings.Split(body, "\n")
	lowered := make([]string, len(lines))
	for index, line := range lines {
		lowered[index] = strings.ToLower(line)
	}

	var out []Snippet
	used := map[int]bool{}
	for index, line := range lowered {
		matched := false
		for _, term := range terms {
			if lowered := strings.ToLower(term); lowered != "" && strings.Contains(line, lowered) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		start := max(0, index-context)
		end := min(len(lines), index+context+1)

		overlapped := false
		for i := start; i < end; i++ {
			if used[i] {
				overlapped = true
				break
			}
		}
		if overlapped {
			continue
		}
		for i := start; i < end; i++ {
			used[i] = true
		}
		text := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		if len(text) > 400 {
			text = text[:400]
		}
		out = append(out, Snippet{Line: index + 1, Text: text})
		if len(out) >= limit {
			break
		}
	}
	return out
}

// SearchAt 按锚点查（需求 FA-4.2 的 kb_at）。支持全路径或任意尾段匹配。
func (kb *KB) SearchAt(codePath, repo string) []*model.Entry {
	needle := strings.ToLower(normalizeCodePath(codePath))

	kb.mu.RLock()
	defer kb.mu.RUnlock()

	var out []*model.Entry
	for _, entry := range kb.Manifest.Entries {
		if entry.Anchor == nil {
			continue
		}
		if repo != "" && entry.Anchor.Repo != repo {
			continue
		}
		full := strings.ToLower(entry.Anchor.Repo + "/" + entry.Anchor.Path)
		pathLower := strings.ToLower(entry.Anchor.Path)
		if full == needle || pathLower == needle || strings.HasSuffix(full, "/"+needle) {
			out = append(out, entry.Clone())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Anchor.Path != out[j].Anchor.Path {
			return out[i].Anchor.Path < out[j].Anchor.Path
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// GrepLine 行级全文命中。
type GrepLine struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Grep 文件级全文检索。优先使用 ripgrep，缺失时退化为纯 Go 扫描。
// 返回 (命中行, 是否用了 ripgrep)。
func (kb *KB) Grep(query string, limit int) ([]GrepLine, bool) {
	if limit <= 0 {
		limit = 50
	}
	if path, err := exec.LookPath("rg"); err == nil {
		cmd := exec.Command(path,
			"--line-number", "--no-heading", "--ignore-case", "--max-count", "5",
			"--glob", "*.md",
			query, kb.Config.KBDir(),
		)
		out, _ := cmd.Output()
		lines := parseRipgrep(out, kb.Config.KBDir(), limit)
		return lines, true
	}

	needle := strings.ToLower(query)
	var out []GrepLine
	for _, entry := range kb.Entries() {
		body, err := kb.ReadBody(entry)
		if err != nil {
			continue
		}
		for index, line := range strings.Split(body, "\n") {
			if strings.Contains(strings.ToLower(line), needle) {
				text := strings.TrimSpace(line)
				if len(text) > 400 {
					text = text[:400]
				}
				out = append(out, GrepLine{File: entry.DocPath, Line: index + 1, Text: text})
				if len(out) >= limit {
					return out, false
				}
			}
		}
	}
	return out, false
}

func parseRipgrep(raw []byte, root string, limit int) []GrepLine {
	var out []GrepLine
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		file, err := relFromRoot(root, parts[0])
		if err != nil {
			continue
		}
		lineNo := atoiSafe(parts[1])
		text := parts[2]
		if len(text) > 400 {
			text = text[:400]
		}
		out = append(out, GrepLine{File: file, Line: lineNo, Text: text})
		if len(out) >= limit {
			break
		}
	}
	return out
}
