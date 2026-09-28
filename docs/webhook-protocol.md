# Webhook 接口协议

> 对应需求 FR-3.1 / FR-3.2 / FR-3.3。实现在 `internal/webhook/`，服务由 `kb serve` 拉起。

## 1. 端点

```
POST http://127.0.0.1:7777/webhook
Content-Type: application/json
X-KB-Token: <可选，配置了 server.token 时必填>
```

另有 `GET /healthz` 用于探活（返回 `{"ok":true}`）。

## 2. 请求体

```json
{
  "repo": "auth-service",
  "branch": "main",
  "commit": "a1b2c3d",
  "event": "post-commit",
  "files": ["pkg/db/pool.go", "pkg/db/conn.go"],
  "ts": 1769590000
}
```

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `repo` | string | ✅ | 仓库名，须与 `.kb/config.yaml` 的 `repos[].name` 一致 |
| `files` | string[] | ✅ | 变更文件，相对仓库根的 POSIX 路径。空数组合法（等同无事发生） |
| `branch` | string | | 分支名。省略则不做事先的分支过滤 |
| `commit` | string | | commit hash。省略则不写幂等记录 |
| `event` | string | | 事件来源，仅作日志：`post-commit` / `post-merge` / `pre-push` / `manual` |
| `ts` | int64 | | Unix 秒。省略则由服务端补 |

## 3. 响应体

HTTP 状态码：`200` 处理完成（含被跳过的情况）；`400` 请求有问题；`401` token 校验失败；`405` 方法不对。

```json
{
  "ok": true,
  "repo": "auth-service",
  "commit": "a1b2c3d",
  "branch": "main",
  "scanned_files": 2,
  "matched": 1,
  "stale": [
    {
      "id": "2026-01-15-pool-leak",
      "symptom": "偶发 502 超时",
      "doc_path": "auth-service/pkg/db/pool.md",
      "path": "pkg/db/pool.go",
      "reason": "content-changed"
    }
  ],
  "recovered": [],
  "duration_ms": 12
}
```

| 字段 | 说明 |
|---|---|
| `skipped` | 出现时说明整批都没处理，值是原因文本（非主干分支 / 重复 commit） |
| `idempotent` | `true` 表示该 `(repo, commit)` 已处理过，本次直接跳过 |
| `scanned_files` | 过滤忽略模式后实际参与比对的文件数 |
| `matched` | 锚定到这些文件的条目数（去重后） |
| `stale[]` | 本次**新变成** stale 的条目 |
| `recovered[]` | 本次**从 stale 回到** fresh 的条目（代码被回滚时会出现） |
| `error` | 有值时 `ok=false`，同时 HTTP 400 |

## 4. 处理规则

```
POST /webhook
  │
  ├─ token 校验（若配置了 server.token）      → 401
  ├─ repo 是否在配置中                        → 400 "仓库未在 kb 配置中"
  ├─ branch 是否等于 repo.primary_branch      → skipped "非主干分支"
  ├─ (repo, commit) 是否已处理过              → idempotent true
  ├─ 按 repo.ignore 过滤 files
  └─ 对每个文件：
       计算当前 sha256
       查 manifest 中锚定到该文件的条目
       哈希 ≠ 基线 → status=stale（reason=content-changed）
       文件不存在 → status=stale（reason=file-missing）
       哈希 = 基线 → status=fresh（代码回滚时自动恢复）
       仅改写 frontmatter 的 status 字段，正文一字不动
```

### 关于「只提醒不改写」

服务**永远不会**修改笔记正文，也**永远不会**删除条目。它只能改两个字段：
`status`（fresh/stale）、`stale_reason`。人工判断是这套系统的护城河，机器不许覆盖。

过期本身会触发一次 git commit（message 形如 `kb(stale): auth-service`），
所以「什么时候开始过期的」在历史里可追溯。

### 幂等

状态存在 `<kb-root>/.kb/webhook-state.json`，按仓库保留最近 64 个 commit。
重复投递同一 commit 只返回 `idempotent: true`，不重复处理。
该文件在 `.gitignore` 里，不进版本库。

## 5. Git hook

### 关于 `post-push` 的更正

需求 §3.1 写的是安装 `post-push` hook，但 **Git 不存在这个钩子**。
Git 提供的与 push 相关/相邻的钩子是：`pre-push`、`post-commit`、`post-merge`、
`post-rewrite`、`post-checkout`。需求自己也补了「或 post-commit + post-merge」，本实现按后者：

| Hook | 安装 | 触发时机 | 为什么 |
|---|---|---|---|
| `post-commit` | 默认 | 本地 commit 后 | 比等 push 更早发现过期；不依赖网络 |
| `post-merge` | 默认 | merge / pull / rebase 后 | 覆盖从远端拉进来的变更 |
| `pre-push` | `--with-pre-push` | push 前 | 兜底；同步执行，会短暂阻塞 push |

### 生成的 hook 脚本

```sh
#!/bin/sh
# 由 kb install-hook 生成，重装会覆盖此文件，勿手改。
# 上报失败一律静默，绝不阻断 git 操作。

( "/path/to/kb" notify --kb "/path/to/my-kb" --repo "/path/to/repo" --event post-commit --quiet >/dev/null 2>&1 & )
exit 0
```

三个设计点：

1. **后台执行**（`( ... & )`）—— 不让 `kb` 的启动耗时进入 `git commit` 的体感。
   `pre-push` 例外，它是同步的，用 `|| true` 兜住错误。
2. **知识库路径写死在脚本里**（`--kb`）—— 于是 hook 在任何 cwd、任何环境变量下都能工作。
3. **永不阻断 git** —— 无论 kb 是否存在、服务是否在跑，退出码永远是 0。

### 服务没启动时的事件不会丢

`kb notify` 在连接失败时把事件追加到 `<kb-root>/.kb/outbox.jsonl`，
下次 `kb serve` 启动时会自动补投（日志里能看到「已补投 N 条积压的变更事件」）。
这是对需求的一处健壮性补强——原设计里服务没起就等于丢事件。

## 6. 手动验证

```bash
kb serve &

curl -s -X POST http://127.0.0.1:7777/webhook \
  -H 'Content-Type: application/json' \
  -d '{"repo":"auth-service","branch":"main","commit":"abc1234","files":["pkg/db/pool.go"]}'

# 模拟 hook 上报（自动读 git 状态推断分支/commit/文件）
kb notify --repo /path/to/auth-service --event manual
```

> 注意：如果本机配了全局 HTTP 代理，`curl` 连 localhost 也会走代理，
> 需要加 `--noproxy '*'` 或设置 `NO_PROXY=127.0.0.1,localhost`。
