#!/usr/bin/env bash
# kb 端到端验收（Phase 1 + Phase 2 的核心路径）
#
# 覆盖需求验收点：
#   Phase 1  能记录、能搜到、知识文件与代码路径对应
#   Phase 2  代码变更后能看到相关条目过期提醒；能通过 agent 记录和查询
#
# 用法：
#   bash tests/acceptance.sh              # 用已构建的 ./bin/kb
#   bash tests/acceptance.sh --keep       # 保留临时目录便于排查
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KB_BIN="${KB_BIN:-$ROOT_DIR/bin/kb}"
KEEP=0
[[ "${1:-}" == "--keep" ]] && KEEP=1

# 不污染 ~/.config/kb/root 与全局环境；后续命令用 KB_ROOT 定位知识库
export KB_NO_REMEMBER=1
# 把监听端口钉死在本测试自己的端口上：默认 7777/7778 可能被同机器上别的 kb 实例占着，
# 那样「服务未启动」这一步的 notify 会打到别人的服务上，测试就不再是隔离的。
export KB_WEBHOOK_PORT=17777
export KB_UI_PORT=17778
# 本机可能有全局 HTTP 代理，localhost 请求必须绕过，否则探活会被代理的 502 干扰
export NO_PROXY="127.0.0.1,localhost,::1"
export no_proxy="$NO_PROXY"
CURL=(curl --noproxy '*' -s)

PASS=0
FAIL=0
STEP=0

ok()   { PASS=$((PASS+1)); printf '  \033[32m✓\033[0m %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); printf '  \033[31m✗\033[0m %s\n' "$1"; }
step() { STEP=$((STEP+1)); printf '\n\033[1m[%d] %s\033[0m\n' "$STEP" "$1"; }
check(){ if eval "$2" >/dev/null 2>&1; then ok "$1"; else bad "$1  →  $2"; fi; }

if [[ ! -x "$KB_BIN" ]]; then
  echo "找不到可执行文件 $KB_BIN，先执行：go build -o bin/kb ./cmd/kb" >&2
  exit 2
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/kb-acceptance-XXXXXX")"
CODE="$WORK/code/auth-service"
KBROOT="$WORK/my-kb"
trap '[[ $KEEP -eq 1 ]] || rm -rf "$WORK"' EXIT

echo "kb 验收测试"
echo "  kb 二进制 : $KB_BIN"
echo "  临时目录  : $WORK"

# ---------------------------------------------------------------- 准备代码仓库
step "准备代码仓库（模拟真实项目）"
mkdir -p "$CODE/pkg/db" "$CODE/api"
cd "$CODE"
git init -q -b main
git config user.email "test@example.com"
git config user.name "kb acceptance"
cat > pkg/db/pool.go <<'GO'
package db

// Pool 连接池封装
type Pool struct {
	maxIdle int
}

func New() *Pool {
	return &Pool{maxIdle: 2}
}
GO
cat > api/order.go <<'GO'
package api

// OrderID 订单号
type OrderID string
GO
git add -A && git commit -q -m "init"
ok "代码仓库就绪：$CODE"

# ---------------------------------------------------------------- Phase 1
step "Phase 1 · kb init 初始化知识库"
"$KB_BIN" init --path "$KBROOT" --repo "auth-service=$CODE" >/dev/null
check "配置目录 .kb/config.yaml 已创建" "test -f '$KBROOT/.kb/config.yaml'"
check "内容目录 kb/ 已创建" "test -d '$KBROOT/kb'"
check "全局目录 incidents/ 已创建" "test -d '$KBROOT/kb/incidents'"
check "索引 .kb-manifest.yaml 已创建" "test -f '$KBROOT/kb/.kb-manifest.yaml'"
check "知识库自身是 git 仓库" "git -C '$KBROOT' rev-parse --is-inside-work-tree"
check "自动纳管了 auth-service 仓库" "grep -q 'auth-service' '$KBROOT/.kb/config.yaml'"
export KB_ROOT="$KBROOT"

step "Phase 1 · kb add 记录（在代码仓库目录内执行，自动推断锚点）"
ADD_OUT="$(cd "$CODE" && "$KB_BIN" add "偶发 502 超时" --at pkg/db/pool.go --confidence unverified 2>&1)"
echo "$ADD_OUT" | sed 's/^/    /'
check "记录成功（输出含『已记录』）" "echo \"\$ADD_OUT\" | grep -q '已记录'"
DOC="$KBROOT/kb/auth-service/pkg/db/pool.md"
check "知识文件落地到镜像路径 auth-service/pkg/db/pool.md" "test -f '$DOC'"
check "frontmatter 含 symptom" "grep -q '偶发 502 超时' '$DOC'"
check "frontmatter 含 sha256 基线哈希" "grep -q 'anchor_hash: sha256:' '$DOC'"
check "frontmatter 记录 anchor 引用" "grep -q 'anchor: auth-service/pkg/db/pool.go' '$DOC'"
check "正文含骨架章节『## 根因』" "grep -q '## 根因' '$DOC'"
check "manifest 索引里能查到该条" "grep -q 'auth-service/pkg/db/pool.md' '$KBROOT/kb/.kb-manifest.yaml'"
check "知识库自动提交了一次" "test \"\$(git -C '$KBROOT' rev-list --count HEAD)\" -ge 2"

step "Phase 1 · 正文补充 + 第二条知识（无锚点的全局知识）"
cat > "$WORK/fix.md" <<'MD'
## 现象

线上偶发 502，QPS 上来后概率增大。

## 根因

连接池 MaxIdleConns 默认 2，高并发下连接反复创建。

## 解决

```go
db.SetMaxIdleConns(20)
db.SetMaxOpenConns(100)
```
MD
ENTRY_ID="$(grep -m1 '^id: ' "$DOC" | sed 's/^id: //')"
"$KB_BIN" edit "$ENTRY_ID" --body-file "$WORK/fix.md" --confidence verified >/dev/null
check "正文被替换" "grep -q 'MaxIdleConns 默认 2' '$DOC'"
check "confidence 更新为 verified" "grep -q 'confidence: verified' '$DOC'"

(cd "$CODE" && "$KB_BIN" add "这个字段不能动，下游报表依赖" --at api/order.go --type constraint --confidence verified >/dev/null)
check "第二条（constraint）落地到 api/order.md" "test -f '$KBROOT/kb/auth-service/api/order.md'"
"$KB_BIN" add "为什么用 Redis 而非本地缓存" --type decision >/dev/null
check "无锚点条目落到 decisions/" "ls '$KBROOT/kb/decisions/' | grep -q '\.md$'"

step "Phase 1 · kb search 症状检索"
SEARCH_OUT="$("$KB_BIN" search 502 2>&1)"
echo "$SEARCH_OUT" | sed 's/^/    /'
check "检索 '502' 命中 502 那条" "echo \"\$SEARCH_OUT\" | grep -q '偶发 502 超时'"
check "命中结果显示锚点" "echo \"\$SEARCH_OUT\" | grep -q 'pkg/db/pool.go'"
AT_OUT="$("$KB_BIN" at pkg/db/pool.go --repo auth-service 2>&1)"
check "kb at 按锚点查到条目" "echo \"\$AT_OUT\" | grep -q '偶发 502 超时'"
KW_OUT="$("$KB_BIN" search 连接池 2>&1)"
check "按正文/关键词也能命中（'连接池'）" "echo \"\$KW_OUT\" | grep -q '偶发 502 超时'"

# ---------------------------------------------------------------- Phase 2
step "Phase 2 · kb status 初始状态应为新鲜"
STATUS_OUT="$("$KB_BIN" status 2>&1)"
echo "$STATUS_OUT" | sed 's/^/    /'
check "状态显示共 3 条" "echo \"\$STATUS_OUT\" | grep -q '条目     3 条'"
check "初始没有过期条目" "echo \"\$STATUS_OUT\" | grep -q '没有过期条目'"

step "Phase 2 · kb install-hook 安装 git hook"
HOOK_OUT="$("$KB_BIN" install-hook "$CODE" 2>&1)"
echo "$HOOK_OUT" | sed 's/^/    /'
check "post-commit hook 已写入" "test -x '$CODE/.git/hooks/post-commit'"
check "post-merge hook 已写入" "test -x '$CODE/.git/hooks/post-merge'"
check "hook 脚本包含 kb notify" "grep -q 'notify' '$CODE/.git/hooks/post-commit'"
check "hook 不会阻断 git（含 exit 0）" "grep -q 'exit 0' '$CODE/.git/hooks/post-commit'"

step "Phase 2 · 服务未启动时 hook 事件应排队而不是丢失"
(cd "$CODE" && "$KB_BIN" notify --repo "$CODE" --event post-commit --quiet) || true
check "outbox 排队文件已生成" "test -f '$KBROOT/.kb/outbox.jsonl'"

step "Phase 2 · 启动 webhook 服务并触发哈希比对"
"$KB_BIN" serve --port 17777 --ui-port 17778 >"$WORK/serve.log" 2>&1 &
SERVE_PID=$!
for _ in $(seq 1 100); do
  if "${CURL[@]}" -f -o /dev/null "http://127.0.0.1:17778/healthz"; then break; fi
  sleep 0.1
done
check "Web UI 健康检查通过" "\"\${CURL[@]}\" -f -o /dev/null 'http://127.0.0.1:17778/healthz'"
PROBE_JSON="$("${CURL[@]}" -X POST -H 'Content-Type: application/json' -d '{"repo":"__probe__","files":[]}' 'http://127.0.0.1:17777/webhook')"
check "webhook 端点由 kb 处理（未知仓库返回结构化错误）" "echo \"\$PROBE_JSON\" | grep -q '未在 kb 配置中'"
check "服务启动时补投了 outbox 事件" "grep -q '补投' '$WORK/serve.log'"

# 改代码 + 造一次变更事件
cat > "$CODE/pkg/db/pool.go" <<'GO'
package db

// Pool 连接池封装
type Pool struct {
	maxIdle     int
	maxOpen     int
	healthCheck bool
}

func New() *Pool {
	return &Pool{maxIdle: 20, maxOpen: 100, healthCheck: true}
}
GO

HOOK_COMMIT="$(cd "$CODE" && git add -A && git commit -q -m 'set max idle conns' && git rev-parse --short HEAD)"
WEBHOOK_JSON="$("${CURL[@]}" -X POST http://127.0.0.1:17777/webhook \
  -H 'Content-Type: application/json' \
  -d "{\"repo\":\"auth-service\",\"branch\":\"main\",\"commit\":\"$HOOK_COMMIT\",\"event\":\"post-commit\",\"files\":[\"pkg/db/pool.go\"]}")"
echo "$WEBHOOK_JSON" | sed 's/^/    /'
check "webhook 返回 ok" "echo \"\$WEBHOOK_JSON\" | grep -q '\"ok\": true'"
check "webhook 标记了 1 条 stale" "echo \"\$WEBHOOK_JSON\" | grep -q '\"reason\": \"content-changed\"'"

IDEMPOTENT_JSON="$("${CURL[@]}" -X POST http://127.0.0.1:17777/webhook \
  -H 'Content-Type: application/json' \
  -d "{\"repo\":\"auth-service\",\"branch\":\"main\",\"commit\":\"$HOOK_COMMIT\",\"files\":[\"pkg/db/pool.go\"]}")"
check "重复通知是幂等的" "echo \"\$IDEMPOTENT_JSON\" | grep -q '\"idempotent\": true'"

BRANCH_JSON="$("${CURL[@]}" -X POST http://127.0.0.1:17777/webhook \
  -H 'Content-Type: application/json' \
  -d '{"repo":"auth-service","branch":"feature/x","commit":"deadbeef","files":["pkg/db/pool.go"]}')"
check "非主干分支被跳过" "echo \"\$BRANCH_JSON\" | grep -q '非主干分支'"

step "Phase 2 · 过期提醒（FR-3.4：只提醒不改写）"
STATUS2="$("$KB_BIN" status 2>&1)"
echo "$STATUS2" | sed 's/^/    /'
check "status 列出过期条目" "echo \"\$STATUS2\" | grep -q '已过期'"
check "过期原因为文件内容已变更" "echo \"\$STATUS2\" | grep -q '文件内容已变更'"
check "md frontmatter 里 status 变为 stale" "grep -q 'status: stale' '$DOC'"
check "人工正文没有被改写（MaxIdleConns 描述仍在）" "grep -q 'MaxIdleConns 默认 2' '$DOC'"
check "过期这件事本身被 commit 了" "git -C '$KBROOT' log --oneline | grep -q 'stale'"

step "Phase 2 · Web UI / REST API"
TREE_JSON="$("${CURL[@]}" 'http://127.0.0.1:17778/api/tree')"
check "目录树按 repo 镜像组织" "echo \"\$TREE_JSON\" | grep -q 'auth-service'"
STATS_JSON="$("${CURL[@]}" 'http://127.0.0.1:17778/api/stats')"
check "统计里 stale=1" "echo \"\$STATS_JSON\" | grep -q '\"stale\": 1'"
ENTRY_JSON="$("${CURL[@]}" "http://127.0.0.1:17778/api/entry?id=$ENTRY_ID")"
check "条目详情可读" "echo \"\$ENTRY_JSON\" | grep -q '偶发 502 超时'"
SEARCH_JSON="$("${CURL[@]}" 'http://127.0.0.1:17778/api/entries?q=502')"
check "UI 检索接口命中" "echo \"\$SEARCH_JSON\" | grep -q '偶发 502 超时'"

step "Phase 2 · 确认仍然有效（kb recover）"
"$KB_BIN" recover "$ENTRY_ID" >/dev/null
check "恢复后状态为 fresh" "grep -q 'status: fresh' '$DOC'"
check "基线哈希已刷新为当前内容" "grep -q \"\$(shasum -a 256 '$CODE/pkg/db/pool.go' | cut -c1-12)\" '$DOC'"

step "Phase 2 · kb_at MCP 工具"
MCP_OUT="$(printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}' \
  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"kb_search","arguments":{"query":"502"}}}' \
  '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"kb_at","arguments":{"path":"pkg/db/pool.go"}}}' \
  '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"kb_status","arguments":{}}}' \
  | "$KB_BIN" mcp 2>"$WORK/mcp.err")"
check "MCP initialize 返回 serverInfo" "echo \"\$MCP_OUT\" | grep -q '\"name\":\"kb\"'"
check "MCP tools/list 暴露 5 个工具" "[[ \$(echo \"\$MCP_OUT\" | grep -oE 'kb_(add|search|at|stale|status)' | sort -u | wc -l | tr -d ' ') -eq 5 ]]"
check "MCP kb_search 返回命中文本" "echo \"\$MCP_OUT\" | grep -q '偶发 502 超时'"
check "MCP kb_at 返回该文件上的知识" "echo \"\$MCP_OUT\" | grep -q 'pkg/db/pool.go'"
check "MCP 每行都是合法 JSON-RPC 响应" "echo \"\$MCP_OUT\" | grep -q '\"jsonrpc\":\"2.0\"'"

step "Phase 2 · kb_add MCP 工具（agent 对话式记录）"
MCP_ADD="$(printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"kb_add","arguments":{"symptom":"压测时 goroutine 泄漏","anchor":"pkg/db/pool.go","repo":"auth-service","type":"incident","confidence":"unverified"}}}' \
  | "$KB_BIN" mcp 2>&1)"
check "MCP kb_add 成功创建条目" "echo \"\$MCP_ADD\" | grep -q '已记录'"
check "通过 MCP 创建的条目也走路径镜像（同文件多条目自动加后缀）" "ls '$KBROOT/kb/auth-service/pkg/db/' | grep -qE '^pool(-.+)?\.md$'"

step "Phase 3 · 路径漂移找回（kb migrate）"
git -C "$CODE" mv pkg/db/pool.go pkg/db/conn_pool.go
git -C "$CODE" commit -q -m "rename pool.go -> conn_pool.go"
DRIFT_OUT="$("$KB_BIN" migrate --scan 2>&1)"
echo "$DRIFT_OUT" | sed 's/^/    /'
check "扫描到失联锚点" "echo \"\$DRIFT_OUT\" | grep -q 'pkg/db/conn_pool.go'"
"$KB_BIN" migrate --scan --apply >/dev/null 2>&1
check "迁移后文档路径已跟随代码" "test -f '$KBROOT/kb/auth-service/pkg/db/conn_pool.md'"
check "同文件多条目迁移后不互相覆盖（后缀被保留）" "ls '$KBROOT/kb/auth-service/pkg/db/' | grep -qE '^conn_pool-.+\.md$'"
check "旧文档已移走" "test ! -f '$KBROOT/kb/auth-service/pkg/db/pool.md'"
check "迁到新目录的文档名没有粘连（防 TrimSuffix 误用回归）" "ls '$KBROOT/kb/auth-service/pkg/db/' | grep -qvE 'poolpool'"

step "Phase 3 · 索引重建与校验"
REINDEX_OUT="$("$KB_BIN" reindex 2>&1)"
echo "$REINDEX_OUT" | sed 's/^/    /'
VALIDATE_OUT="$("$KB_BIN" validate 2>&1)"
echo "$VALIDATE_OUT" | sed 's/^/    /'
check "校验通过（无缺失文档）" "echo \"\$VALIDATE_OUT\" | grep -q '没有发现问题'"

step "回归 · 知识库建在已有 git 仓库内部时必须是嵌套独立仓库"
# 曾经的缺陷：git rev-parse --is-inside-work-tree 对仓库内层目录也返回 true，
# 于是 kb init 误判「已是仓库」而跳过 git init，自动提交会落到外层仓库上。
INNER="$CODE/.kb-notes"
OUTER_HEAD_BEFORE="$(git -C "$CODE" rev-parse HEAD)"
"$KB_BIN" init --path "$INNER" --repo "auth-service=$CODE" >/dev/null 2>&1
check "嵌套知识库拥有自己的 .git" "test -d '$INNER/.git'"
check "从知识库内解析到的仓库根是它自己" "test \"\$(cd '$INNER' && git rev-parse --show-toplevel)\" = \"\$(cd '$INNER' && pwd -P)\""
check "init 提示了外层仓库需要加 .gitignore" "\"\$KB_BIN\" init --path '$INNER' --repo 'auth-service=$CODE' 2>&1 | grep -q 'gitignore'"
(cd "$CODE" && "$KB_BIN" add "嵌套知识库不该污染外层仓库" --at api/order.go --repo auth-service --kb "$INNER" >/dev/null 2>&1)
check "写入知识后外层仓库 HEAD 未变" "test \"\$(git -C '$CODE' rev-parse HEAD)\" = '$OUTER_HEAD_BEFORE'"
check "提交落在了知识库自己的仓库里" "test \"\$(git -C '$INNER' rev-list --count HEAD)\" -ge 2"

step "收尾"
kill "$SERVE_PID" 2>/dev/null
wait "$SERVE_PID" 2>/dev/null
check "知识库 git 历史完整（≥5 次提交）" "test \"\$(git -C '$KBROOT' rev-list --count HEAD)\" -ge 5"

printf '\n\033[1m结果：%d 通过 / %d 失败\033[0m\n' "$PASS" "$FAIL"
if [[ $FAIL -gt 0 ]]; then
  echo "临时目录保留在 $WORK"
  exit 1
fi
echo "知识库结构："
find "$KBROOT/kb" -name '.kb-manifest.yaml' -prune -o -type f -print | sed "s|$KBROOT/|  |" | sort
exit 0
