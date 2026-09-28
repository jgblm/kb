# kb — 个人代码知识库

带代码锚定的个人知识库。服务于两件事：**关键时刻快速定位问题**、**沉淀个人判断力**。

代码里大部分符号看名字就懂，不需要文档。真正值钱的是**踩坑经验、设计决策、历史包袱**——
这些 LLM 从代码里推不出来，天然稀疏、高价值、与个人强相关，值得独立于代码库保存。

```bash
# 1. 初始化（知识库是独立 git 仓库）
kb init --path ~/my-kb --repo auth-service=/path/to/auth-service

# 2. 遇到坑随手记（在代码目录里执行，锚点可省）
kb add "偶发 502 超时" --at pkg/db/pool.go

# 3. 下次故障现场按症状秒查
kb search 502
```

```bash
# 让代码变更自动触发过期提醒
kb install-hook /path/to/auth-service
kb serve                       # webhook 7777 + Web UI 7778
```

---

## 它跟「写文档」的区别

| | 传统文档 | kb |
|---|---|---|
| 记录粒度 | 符号级，追求覆盖率 | 只记坑与决策，**稀疏优先** |
| 检索入口 | 按模块/文件名 | 按**症状**（"偶发 502"），因为现场只记得现象 |
| 位置 | 独立 wiki，与代码脱节 | **路径镜像**：`pkg/db/pool.go` ↔ `kb/auth-service/pkg/db/pool.md` |
| 代码变更后 | 悄悄腐烂 | 哈希比对 → 标 `stale` 提醒，**只提醒不改写** |
| 存储 | 数据库/平台 | md + yaml，可 diff、可 git、无依赖 |

**只提醒不改写**是核心设计约束：人工判断是这套系统的护城河，机器只能把 `status` 从
`fresh` 改成 `stale`，永远不能动你的正文、不能删条目。过期的条目会降权但仍可检索——
它可能只是代码变了，结论仍然成立。

---

## 安装

```bash
git clone <this> && cd project_notion
make build                     # 产出 bin/kb，零运行时依赖
make install                   # 或装到 $GOPATH/bin
```

Go 1.23+。唯一第三方依赖是 `gopkg.in/yaml.v3` 和 `spf13/cobra`。
`ripgrep` 可选：装了用 `rg` 做全文扫描，没装自动退化为纯 Go 扫描。

---

## 命令

```bash
kb init --path ~/my-kb [--repo name=/path]    # 初始化
kb add "<症状>" [--at path] [--type] [--confidence] [--body -]   # 记录
kb search <检索词> [--status stale] [--grep]  # 按症状检索
kb at <代码路径>                              # 按锚点查该文件上的知识
kb status [--refresh]                         # 全局状态 + 过期列表
kb edit <id> [--confidence verified]          # 编辑（默认开 $EDITOR）
kb open <代码路径>                            # 打开对应知识文件
kb recover <id>                               # 确认仍然有效，刷新基线
kb delete <id>                                # 删除
kb repo add|list|remove                       # 管理纳管的代码仓库
kb install-hook <repo> [--with-pre-push]      # 装 git hook
kb serve [--port 7777] [--ui-port 7778]       # webhook + Web UI
kb mcp                                        # MCP Server（stdio）
kb migrate --scan [--apply]                   # 找回改名/移动后的失联锚点
kb reindex                                    # 从 md 重建索引
kb validate                                   # 校验索引与文档一致性
```

所有列表类命令都支持 `--json`。

---

## 目录结构

```
<kb-root>/                          # 独立 git 仓库
  .git/
  .gitignore                        # 忽略运行时状态
  .kb/
    config.yaml                     # 仓库映射、端口、提交模板
    webhook-state.json              # 幂等状态（不入库）
    outbox.jsonl                    # 服务离线时的事件队列（不入库）
  kb/                               # 知识内容根
    auth-service/pkg/db/pool.md     # 与代码目录镜像
    auth-service/pkg/db/pool.assets/# 位图/附件就近存放
    incidents/  decisions/  cross-cutting/   # 无锚点的全局知识
    .kb-manifest.yaml               # 派生索引（可随时重建）
```

知识文件本体：

```markdown
---
id: 2026-01-15-pool-leak
symptom: 偶发 502 超时
type: incident
confidence: verified
status: stale
keywords: [502, 超时, 连接池]
anchor: auth-service/pkg/db/pool.go:120-180
anchor_hash: sha256:ab12cd…
anchor_commit: a1b2c3d
created_at: 2026-01-15T10:00:00Z
updated_at: 2026-03-02T08:11:00Z
stale_reason: content-changed
---

## 现象
线上偶发 502，QPS 上来后概率增大……

## 根因
连接池 MaxIdleConns 默认 2，高并发下连接反复创建……

## 解决
```go
db.SetMaxIdleConns(20)
db.SetMaxOpenConns(100)
```
```

图表的处理：**mermaid 直接写在 md 里**（可 diff、可 review，不渲染成二进制），
位图就近放 `<path>.assets/`，md 用相对路径引用。不做图床、不做资产管理。

---

## 数据模型

三个 schema 都在 `docs/schema/`，可直接用于校验：

| 文件 | 描述 |
|---|---|
| `entry.schema.json` | 单条知识（索引里的元素） |
| `manifest.schema.json` | `kb/.kb-manifest.yaml` |
| `frontmatter.schema.json` | md 头部，**唯一事实来源** |
| `config.schema.json` | `.kb/config.yaml` |

关键点：**md 是事实来源，manifest 是派生索引**。索引坏了不致命，`kb reindex` 从 md 重建。

---

## 对需求文档的修正

实现过程中发现需求 v1.0 里有 5 处需要调整，都已按修正后的方案落地：

### 1. `confidence` 与 `status` 语义冲突（§3.1.3 vs §10.2）

§3.1.3 定义 `confidence: verified | unverified`，§10.2 又把 `stale` 塞进 confidence。
但「代码变了」和「我没验证过」是两件正交的事，混在一个字段会导致二者无法同时表达。

拆分为：

| 字段 | 取值 | 谁来写 |
|---|---|---|
| `confidence` | `verified` / `unverified` | **只由人写** |
| `status` | `fresh` / `stale` | **只由哈希比对引擎写** |

### 2. `post-push` 不是 Git 支持的 hook（§3.1）

Git 只有 `pre-push` / `post-commit` / `post-merge` / `post-rewrite` / `post-checkout`，
**没有 `post-push`**。按需求自己给的后备方案实现：

| Hook | 默认 | 理由 |
|---|---|---|
| `post-commit` | ✅ | 比等 push 更早发现过期，不依赖网络 |
| `post-merge` | ✅ | 覆盖 pull / rebase 拉进来的变更 |
| `pre-push` | `--with-pre-push` | 兜底，同步执行会短暂阻塞 push |

### 3. hook 脚本依赖 `jq`（§3.1.1）

原设计的 hook 用 `jq` 拼 JSON，但 `jq` 并非 macOS 默认自带。
改为 hook 只调用 `kb notify`，变更文件列表、JSON 编码、分支/commit 推断全在 Go 里做，
并且**把知识库路径写死进脚本**，于是 hook 在任何 cwd、任何环境变量下都能工作。

### 4. 服务未启动时事件会丢（§3.1）

原设计里 `kb serve` 没跑，hook 的上报就静默失败了。
增加 `.kb/outbox.jsonl`：上报失败时事件落盘排队，下次 `kb serve` 启动自动补投
（日志会打印「已补投 N 条积压的变更事件」）。

### 5. 「定期扫描」没有触发器（§2.2）

原设计说「定期扫描 + 内容哈希匹配，自动修正」，但没定义谁来定期触发。
v1 改成**显式命令** `kb migrate --scan`（只看候选）/ `--scan --apply`（应用迁移），
因为自动改路径属于「改写」，与「只提醒不改写」的原则冲突。要自动化可以自己挂 cron。

---

## 实现要点

### 过期判定

粒度是**整个文件**的 sha256（v1 简化，见 §3.3）。三条规则：

```
哈希 ≠ 基线           → stale (content-changed)
文件不存在            → stale (file-missing)
哈希 = 基线            → fresh   （代码回滚时自动恢复）
锚点仓库已从配置移除   → 保持原状，在 status 里列为「孤立条目」
```

`kb recover <id>` 是**唯一**会清除 stale 的人工动作：确认结论仍然成立，
把当前内容设为新基线，之后再变才会再提示。

### 检索打分

四路检索收敛到一个入口（症状 / 关键词 / 全文 / 锚点），权重：

```
症状 12  →  关键词精确 10  →  关键词部分 6  →  文档路径 5  →  锚点 4  →  正文 3
```

全部查询词都命中症状时额外 +8（「按现象检索」的核心体验），
`verified` 的条目 +1.5，`stale` 的 −1.0（降权但仍可见）。
向量检索按需求预留到 v2，只需在 `kb.Search` 里加一路打分来源。

### 并发

`KB` 内部一把 `sync.RWMutex`。读接口一律返回 `Entry.Clone()`，
避免锁外共享可变指针（webhook 与 UI 在同一进程里并发跑，`go test -race` 干净）。

### 知识库一定是「自己的」仓库

`gitx` 里有两个容易混淆的判断：

| 函数 | 语义 | 用途 |
|---|---|---|
| `IsRepo(dir)` | dir **位于**某个工作区内（含内层目录） | 判断代码仓库 |
| `HasOwnRepo(dir)` | dir **自身**就是仓库根（存在 `dir/.git`） | 判断知识库 |

两者绝不能互换。踩过的坑：`kb init` 原本用 `IsRepo` 判断「是否已经是仓库」，
于是把知识库建在**已有代码仓库内部**时（例如 `my-repo/.kb-notes/`），
`rev-parse --is-inside-work-tree` 返回 true，`git init` 被跳过 —— 知识库没有自己的
历史，而 `kb` 的自动提交会落进**外层代码仓库**，等于把私人笔记 commit 进项目里。

现在：`Init` 用 `HasOwnRepo` 建出嵌套的独立仓库；`CommitAll` 自身也拒绝
对「非仓库根」的目录提交（最后一道防线）；`kb init` 会提示把该目录加进外层
仓库的 `.gitignore`。回归测试见 `TestInitInsideExistingRepoCreatesNestedRepo`。

### 多知识库共存

监听端口可用环境变量覆盖，避免同一台机器上多个知识库抢 7777/7778：

```bash
KB_WEBHOOK_PORT=7877 KB_UI_PORT=7878 kb serve --kb ~/other-kb
```


---

## 开发

```bash
make build      # 构建 bin/kb
make test       # 单元测试
make check      # gofmt + go vet + test
make accept     # 端到端验收（62 项断言，覆盖 Phase 1/2/3 核心路径）
make demo       # 造一个带示例内容的知识库并启动服务
```

`tests/acceptance.sh` 会真起一个 git 代码仓库、真开 HTTP 服务、真发 webhook，
验证全链路：记录 → 镜像落盘 → 检索 → 装 hook → 事件排队 → 哈希比对 → 标 stale
→ **正文未被改写** → 人工确认恢复 → MCP 记录/检索 → 改名漂移找回 → 索引重建。

---

## 实现状态

| Phase | 内容 | 状态 |
|---|---|---|
| 1 | `init` / `add` / 目录镜像 / 哈希 / `search` / 文件落地 + git commit | ✅ |
| 2 | webhook + git hook / 哈希比对 + stale / `status` / Web UI / MCP Server | ✅ |
| 3 | 向量检索 / `migrate` / mermaid 渲染增强 / 编辑器插件 / 统计报告 | 🟡 仅 `migrate` 完成 |

Phase 3 待办：

- **向量检索**：`kb.Search` 加一路语义打分，接口已留好
- **mermaid 渲染**：Web UI 目前展示源码（可 diff、可 review 是刻意选择），未内联渲染
- **VSCode 插件**：目前 `kb open <path>` 走系统编辑器
- **统计报告**：覆盖率、孤儿条目、知识熵趋势

---

## 已知取舍

- **不抓取 commit / PR / issue**：按需求排除。抓取的收益低、架构复杂度高。
- **LLM 只整理措辞**：MCP 的 `kb_add` 让 agent 帮忙组织语言，但**判断由人给**。
  `kb_add` 的 tool description 里明确写了「不要复述代码本身」。
- **中文分词是规则法**：没有词典、没有 cgo。够用即可，要更准可换 jieba/gojieba，
  替换点集中在 `internal/textx`。
- **无锚点条目永不过期**：跨系统的全局知识（`incidents/` 等）不参与过期追踪，
  因为没有可以被哈希的锚点。这是概念上的必然，不是实现偷懒。
