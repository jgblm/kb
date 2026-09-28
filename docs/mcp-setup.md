# MCP 接入说明

`kb mcp` 以 stdio 方式运行 MCP Server，供 agent 在对话里直接记录和检索知识。

## 1. 注册

在 agent 的 MCP 配置里加一项（`~/.workbuddy/mcp.json` 或对应客户端的配置）：

```json
{
  "mcpServers": {
    "kb": {
      "command": "/absolute/path/to/kb",
      "args": ["mcp", "--kb", "/Users/you/my-kb"]
    }
  }
}
```

`--kb` 可以省略，此时按 `$KB_ROOT` → cwd 向上查找 → `~/.config/kb/root` 的顺序定位知识库。
建议写绝对路径：MCP 客户端的工作目录往往不是你预期的那个。

用官方 SDK 换掉内置实现（可选）：`go get github.com/modelcontextprotocol/go-sdk`，
然后替换 `internal/mcp/mcp.go` 的 `Run`/`dispatch` 两层即可，`tools.go` 里的五个工具实现不用动。

## 2. 工具

| 工具 | 参数 | 用途 |
|---|---|---|
| `kb_add` | `symptom`(必填) `anchor` `repo` `type` `confidence` `body` `keywords` | 记录一条知识 |
| `kb_search` | `query`(必填) `limit` `repo` `type` `status` | 按症状检索 |
| `kb_at` | `path`(必填) `repo` | 按代码路径查该文件上沉淀过什么 |
| `kb_stale` | `repo` | 列出因代码变更而可能过时的条目 |
| `kb_status` | `repo` | 全局统计 |

## 3. 典型用法

### 排查问题时先查历史

> **你**：线上又出现偶发 502 了，QPS 一上来就有
>
> *（agent 调 `kb_search`，query="502 超时"）*

```
命中 2 条（query="502 超时"）：

1. [incident · fresh · 23.5] 偶发 502 超时
   id: 2026-01-15-pool-leak  ✓已验证
   锚点: auth-service/pkg/db/pool.go
   线上偶发 502，QPS 上来后概率增大。连接池 MaxIdleConns 默认 2 …
```

### 读代码时看这个文件上的坑

> **你**：帮我看看 pkg/db/pool.go
>
> *（agent 调 `kb_at`，path="pkg/db/pool.go"）*

### 解决了新问题顺手记下来

> **你**：记一下，这个连接池泄漏是因为没设置 MaxIdleConns
>
> *（agent 调 `kb_add`，symptom="连接池泄漏", anchor="pkg/db/pool.go"）*

### 改完代码检查有没有推翻旧结论

> **你**：我刚重构了 pool.go，有相关的旧笔记需要更新吗
>
> *（agent 调 `kb_stale`）*

## 4. 协议细节

- 传输：**stdio，newline-delimited JSON-RPC 2.0**（每行一条完整 JSON，不是 LSP 的 `Content-Length` 分帧）
- 方法：`initialize`、`notifications/initialized`、`tools/list`、`tools/call`、`ping`
  （`resources/list`、`prompts/list` 返回空数组，不报错）
- 通知（无 `id`）不返回响应
- `kb_add` 的 `body` 省略时会生成骨架（现象/根因/解决），正文可以后补

手工验证：

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"kb_status","arguments":{}}}' \
| kb mcp --kb ~/my-kb
```

## 5. 给 agent 的提示词建议

数据库本身不强迫 LLM 用它，所以值得在 agent 的 system prompt 里写一句：

```
遇到线上问题时，先用 kb_search 查历史经验，再自己分析；
解决了新坑之后用 kb_add 记一条（锚点尽量填，正文可以后补）。
不要复述代码里能直接看出来的东西，只记「代码里看不出的判断」。
```

最后一句很关键：`kb_add` 的描述里也写了同样的话。这套系统的价值密度取决于**不记什么**。
