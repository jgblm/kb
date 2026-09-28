// Package mcp 实现自包含的 MCP Server（需求 FR-4.2 / §5）。
//
// 传输：stdio，newline-delimited JSON-RPC 2.0（不是 LSP 的 Content-Length 分帧）。
// 刻意不依赖官方 SDK，好处是零第三方依赖、单二进制即可分发；
// 若想换成 github.com/modelcontextprotocol/go-sdk，只需替换本文件的
// Run/dispatch 两层，工具实现（tools.go）不需要动。
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/stone/kb/internal/kb"
)

// ProtocolVersion 默认协议版本；若客户端在 initialize 里声明了版本则回显。
const ProtocolVersion = "2024-11-05"

// Server MCP 服务端。
type Server struct {
	KB      *kb.KB
	Version string

	In  io.Reader
	Out io.Writer
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// New 构造 MCP Server。
func New(knowledgeBase *kb.KB, version string) *Server {
	return &Server{KB: knowledgeBase, Version: version, In: os.Stdin, Out: os.Stdout}
}

// Run 主循环：逐行读 JSON-RPC 请求，逐行写响应。
func (s *Server) Run() error {
	reader := bufio.NewReaderSize(s.In, 1<<20)
	writer := bufio.NewWriter(s.Out)
	defer writer.Flush()

	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			if out := s.handleLine(line); len(out) > 0 {
				if _, writeErr := writer.Write(out); writeErr != nil {
					return writeErr
				}
				if flushErr := writer.Flush(); flushErr != nil {
					return flushErr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

func (s *Server) handleLine(line []byte) []byte {
	var request rpcRequest
	if err := json.Unmarshal(line, &request); err != nil {
		return encodeResponse(rpcResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32700, Message: "JSON 解析失败: " + err.Error()},
		})
	}
	// 通知（无 id）不需要响应
	isNotification := len(request.ID) == 0

	result, rpcErr := s.dispatch(request)
	if isNotification {
		return nil
	}
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	if rpcErr != nil {
		response.Error = rpcErr
	} else {
		encoded, err := json.Marshal(result)
		if err != nil {
			response.Error = &rpcError{Code: -32603, Message: err.Error()}
		} else {
			response.Result = encoded
		}
	}
	return encodeResponse(response)
}

func (s *Server) dispatch(request rpcRequest) (any, *rpcError) {
	switch request.Method {
	case "initialize":
		return s.handleInitialize(request.Params), nil
	case "notifications/initialized", "initialized":
		return map[string]any{}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefinitions()}, nil
	case "tools/call":
		return s.handleToolCall(request.Params)
	case "resources/list":
		return map[string]any{"resources": []any{}}, nil
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "未知方法: " + request.Method}
	}
}

func (s *Server) handleInitialize(params json.RawMessage) map[string]any {
	var payload struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(params, &payload)
	version := payload.ProtocolVersion
	if version == "" {
		version = ProtocolVersion
	}
	return map[string]any{
		"protocolVersion": version,
		"capabilities": map[string]any{
			"tools": map[string]any{"listChanged": false},
		},
		"serverInfo": map[string]any{
			"name":    "kb",
			"version": s.Version,
		},
		"instructions": "个人代码知识库。记录踩坑经验与设计决策，按症状检索。" +
			"遇到问题时先 kb_search 查历史经验；确认了新的坑用 kb_add 记录。" +
			"kb_stale 可列出因代码变更而可能过时的条目。",
	}
}

func (s *Server) handleToolCall(params json.RawMessage) (any, *rpcError) {
	var payload struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, &rpcError{Code: -32602, Message: "tools/call 参数非法: " + err.Error()}
	}
	text, err := s.callTool(payload.Name, payload.Arguments)
	if err != nil {
		return map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
			"isError": true,
		}, nil
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}, nil
}

func encodeResponse(response rpcResponse) []byte {
	encoded, err := json.Marshal(response)
	if err != nil {
		encoded, _ = json.Marshal(rpcResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: -32603, Message: fmt.Sprintf("序列化失败: %v", err)},
		})
	}
	return append(encoded, '\n')
}
