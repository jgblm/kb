#!/usr/bin/env bash
# 造一个带示例内容的知识库（demo-kb/）并启动服务，用于试用 Web UI。
#
#   bash scripts/demo.sh          # 造数据并启动服务
#   bash scripts/demo.sh --seed   # 只造数据
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KB_BIN="$ROOT_DIR/bin/kb"
WORK="$ROOT_DIR/demo-kb"
CODE="$WORK/code/auth-service"

[[ -x "$KB_BIN" ]] || { echo "先执行 make build" >&2; exit 2; }

if [[ -d "$WORK" ]]; then
  echo "复用已有的 $WORK（想重来先 make clean）"
else
  echo "正在构造演示数据…"
  mkdir -p "$CODE/pkg/db" "$CODE/pkg/cache" "$CODE/api"
  cd "$CODE"
  git init -q -b main
  git config user.email "demo@example.com"
  git config user.name "kb demo"
  # 给 fixture 一个独立的 go.mod：这样上层 `go test ./...` 不会把示例代码
  # 当成本项目的一部分去编译（也顺带让 fixture 更像真实项目）
  cat > go.mod <<'GOMOD'
module example.com/auth-service

go 1.23
GOMOD
  cat > pkg/db/pool.go <<'GO'
package db

type Pool struct {
	maxIdle int
}

func New() *Pool { return &Pool{maxIdle: 2} }
GO
  cat > pkg/cache/redis.go <<'GO'
package cache

func NewRedis() error { return nil }
GO
  cat > api/order.go <<'GO'
package api

type OrderID string
GO
  git add -A && git commit -q -m "init"

  cd "$ROOT_DIR"
  export KB_NO_REMEMBER=1
  "$KB_BIN" init --path "$WORK/my-kb" --repo "auth-service=$CODE" >/dev/null

  export KB_ROOT="$WORK/my-kb"
  cd "$CODE"
  "$KB_BIN" add "偶发 502 超时" --at pkg/db/pool.go --confidence verified >/dev/null
  "$KB_BIN" add "压测时 goroutine 数持续上涨" --at pkg/db/pool.go >/dev/null
  "$KB_BIN" add "这个字段不能动，下游报表依赖它" --at api/order.go \
      --type constraint --confidence verified >/dev/null
  "$KB_BIN" add "为什么用 Redis 而非进程内本地缓存" --type decision --confidence verified >/dev/null
  "$KB_BIN" add "线上机器磁盘满导致服务假死" --type incident >/dev/null

  cd "$ROOT_DIR"
  # 给第一条补一段真实感更强的正文（含 mermaid 与代码块）
  POOL_DOC="$WORK/my-kb/kb/auth-service/pkg/db/pool.md"
  ENTRY_ID="$(grep -m1 '^id: ' "$POOL_DOC" | sed 's/^id: //')"
  cat > "$WORK/fix.md" <<'MD'
## 现象

线上偶发 502，QPS 上来后概率明显增大。低峰期几乎不复现。

## 根因

连接池 `MaxIdleConns` 默认值 2。高并发下连接被反复创建销毁，
TCP 握手与 TLS 协商的尾巴吃掉了大部分请求预算，超时后网关返回 502。

确认方式：`ss -s` 看 TIME_WAIT 数量在高峰时暴涨。

## 解决

```go
db.SetMaxIdleConns(20)
db.SetMaxOpenConns(100)
db.SetConnMaxLifetime(30 * time.Minute)
```

## 时序

```mermaid
sequenceDiagram
  Client->>Service: 请求
  Service->>Pool: 取连接
  Pool-->>Service: 连接不足，等待
  Service-->>Client: 502
```

## 备注

- 教训：所有连接池都要显式设 MaxIdle，不要信默认值
- 相关：`pkg/db/pool.go` L12-40
MD
  "$KB_BIN" edit "$ENTRY_ID" --body-file "$WORK/fix.md" --confidence verified >/dev/null
  rm -f "$WORK/fix.md"

  echo "演示数据就绪：$WORK/my-kb"
fi

if [[ "${1:-}" == "--seed" ]]; then
  exit 0
fi

echo
echo "启动服务 —— Web UI http://127.0.0.1:7788  （Ctrl-C 退出）"
echo "想看过期提醒？在另一个终端里："
echo "    cd $CODE && sed -i '' 's/maxIdle: 2/maxIdle: 20/' pkg/db/pool.go && git commit -am 'tune pool'"
echo "然后刷新页面，那条笔记会变成 stale。"
echo
exec "$KB_BIN" serve --kb "$WORK/my-kb" --port 7787 --ui-port 7788
