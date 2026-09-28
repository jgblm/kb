// Package webui 提供本地 Web UI 与 REST API（需求 FR-5.1）。
//
// 单文件 SPA，零构建、零外链：所有资源 embed 进二进制，
// 断网也能用，符合「本地优先」原则。
package webui

import (
	"embed"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/stone/kb/internal/kb"
	"github.com/stone/kb/internal/model"
	"github.com/stone/kb/internal/pathmap"
)

//go:embed assets/index.html
var assets embed.FS

// Server Web UI 的 HTTP 处理器。
type Server struct {
	KB *kb.KB
}

// New 构造 Server。
func New(knowledgeBase *kb.KB) *Server { return &Server{KB: knowledgeBase} }

// TreeNode 目录树节点（与代码目录镜像）。
type TreeNode struct {
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Kind     string      `json:"kind"` // dir | entry
	ID       string      `json:"id,omitempty"`
	Symptom  string      `json:"symptom,omitempty"`
	Type     string      `json:"type,omitempty"`
	Status   string      `json:"status,omitempty"`
	Children []*TreeNode `json:"children,omitempty"`
}

// Mux 返回 Web UI 的路由表。
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/tree", s.handleTree)
	mux.HandleFunc("/api/entries", s.handleEntries)
	mux.HandleFunc("/api/entry", s.handleEntry)
	mux.HandleFunc("/api/recover", s.handleRecover)
	mux.HandleFunc("/api/reindex", s.handleReindex)
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
	})
	return mux
}

func (s *Server) handleIndex(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, "/api/") {
		http.NotFound(writer, request)
		return
	}
	page, err := assets.ReadFile("assets/index.html")
	if err != nil {
		http.Error(writer, "UI 资源缺失", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	_, _ = writer.Write(page)
}

func (s *Server) handleStats(writer http.ResponseWriter, _ *http.Request) {
	report := s.KB.Status("")
	writeJSON(writer, http.StatusOK, report)
}

func (s *Server) handleTree(writer http.ResponseWriter, _ *http.Request) {
	entries := s.KB.Entries()
	roots := map[string]*TreeNode{}
	var order []string

	ensure := func(segments []string) *TreeNode {
		var parent *TreeNode
		var accumulated string
		for _, segment := range segments {
			if accumulated == "" {
				accumulated = segment
			} else {
				accumulated = accumulated + "/" + segment
			}
			key := accumulated
			node, ok := roots[key]
			if !ok {
				node = &TreeNode{Name: segment, Path: key, Kind: "dir"}
				roots[key] = node
				if parent == nil {
					order = append(order, key)
				} else {
					parent.Children = append(parent.Children, node)
				}
			}
			parent = node
		}
		return parent
	}

	for _, entry := range entries {
		rel := pathmap.Normalize(entry.DocPath)
		segments := strings.Split(rel, "/")
		if len(segments) > 1 {
			segments = segments[:len(segments)-1]
		} else {
			segments = nil
		}
		if segments == nil {
			segments = []string{"(根)"}
		}
		parent := ensure(segments)
		parent.Children = append(parent.Children, &TreeNode{
			Name:    baseName(entry.DocPath),
			Path:    rel,
			Kind:    "entry",
			ID:      entry.ID,
			Symptom: entry.Symptom,
			Type:    string(entry.Type),
			Status:  string(entry.Status),
		})
	}

	trees := make([]*TreeNode, 0, len(order))
	for _, key := range order {
		trees = append(trees, roots[key])
	}
	sortTree(trees)
	writeJSON(writer, http.StatusOK, map[string]any{"tree": trees, "total": len(entries)})
}

func baseName(docPath string) string {
	rel := pathmap.Normalize(docPath)
	if index := strings.LastIndex(rel, "/"); index >= 0 {
		return rel[index+1:]
	}
	return rel
}

func sortTree(nodes []*TreeNode) {
	sort.SliceStable(nodes, func(i, j int) bool {
		if (nodes[i].Kind == "dir") != (nodes[j].Kind == "dir") {
			return nodes[i].Kind == "dir"
		}
		return nodes[i].Name < nodes[j].Name
	})
	for _, node := range nodes {
		if len(node.Children) > 0 {
			sortTree(node.Children)
		}
	}
}

func (s *Server) handleEntries(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	text := strings.TrimSpace(query.Get("q"))
	limit := 200
	if raw := query.Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	options := kb.SearchOptions{
		Query:         text,
		Limit:         limit,
		Repo:          query.Get("repo"),
		IncludeGlobal: true,
	}
	if raw := query.Get("type"); raw != "" {
		options.Type = model.ParseType(raw)
	}
	if raw := query.Get("status"); raw != "" {
		options.Status = model.ParseStatus(raw)
	}

	if text == "" {
		entries := s.KB.Entries()
		filtered := make([]*model.Entry, 0, len(entries))
		for _, entry := range entries {
			if options.Repo != "" && (entry.Anchor == nil || entry.Anchor.Repo != options.Repo) {
				continue
			}
			if options.Type != "" && entry.Type != options.Type {
				continue
			}
			if options.Status != "" && entry.Status != options.Status {
				continue
			}
			filtered = append(filtered, entry)
		}
		sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].UpdatedAt > filtered[j].UpdatedAt })
		if len(filtered) > limit {
			filtered = filtered[:limit]
		}
		writeJSON(writer, http.StatusOK, map[string]any{"mode": "list", "entries": filtered, "total": len(filtered)})
		return
	}

	hits := s.KB.Search(options)
	writeJSON(writer, http.StatusOK, map[string]any{"mode": "search", "hits": hits, "total": len(hits)})
}

func (s *Server) handleEntry(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		id := request.URL.Query().Get("id")
		detail, err := s.KB.Detail(id)
		if err != nil {
			writeError(writer, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, detail)

	case http.MethodPut, http.MethodPost:
		var payload struct {
			ID         string `json:"id"`
			Body       string `json:"body"`
			Confidence string `json:"confidence"`
			Symptom    string `json:"symptom"`
			At         string `json:"at"`
			Repo       string `json:"repo"`
			Type       string `json:"type"`
		}
		if err := decodeJSON(request, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}

		// 无 id 视为新建
		if strings.TrimSpace(payload.ID) == "" {
			result, err := s.KB.Add(kb.AddOptions{
				Symptom:      payload.Symptom,
				At:           payload.At,
				Repo:         payload.Repo,
				Type:         model.ParseType(payload.Type),
				Confidence:   model.ParseConfidence(payload.Confidence),
				Body:         payload.Body,
				BodyProvided: payload.Body != "",
			})
			if err != nil {
				writeError(writer, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(writer, http.StatusCreated, result)
			return
		}

		entry, err := s.KB.Get(payload.ID)
		if err != nil {
			writeError(writer, http.StatusNotFound, err.Error())
			return
		}
		body := payload.Body
		if body == "" {
			body, _ = s.KB.ReadBody(entry)
		}
		var confidence *model.Confidence
		if payload.Confidence != "" {
			parsed := model.ParseConfidence(payload.Confidence)
			confidence = &parsed
		}
		if err := s.KB.UpdateBody(entry, body, confidence); err != nil {
			writeError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		detail, err := s.KB.Detail(entry.ID)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, detail)

	case http.MethodDelete:
		id := request.URL.Query().Get("id")
		entry, err := s.KB.Get(id)
		if err != nil {
			writeError(writer, http.StatusNotFound, err.Error())
			return
		}
		if err := s.KB.Delete(entry, true); err != nil {
			writeError(writer, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "deleted": id})

	default:
		writeError(writer, http.StatusMethodNotAllowed, "不支持的方法")
	}
}

func (s *Server) handleRecover(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		ID string `json:"id"`
	}
	if err := decodeJSON(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	entry, err := s.KB.Get(payload.ID)
	if err != nil {
		writeError(writer, http.StatusNotFound, err.Error())
		return
	}
	if err := s.KB.Recover(entry); err != nil {
		writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	detail, err := s.KB.Detail(payload.ID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, detail)
}

func (s *Server) handleReindex(writer http.ResponseWriter, _ *http.Request) {
	report, err := s.KB.Reindex()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, report)
}

func decodeJSON(request *http.Request, target any) error {
	raw, err := io.ReadAll(io.LimitReader(request.Body, 8<<20))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(payload)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]any{"ok": false, "error": message})
}
