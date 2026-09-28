#!/usr/bin/env bash
# kb 用自己管理自己：为本项目建一个知识库、纳管本仓库、装 git hook。
#
#   bash scripts/dogfood.sh              # 默认知识库 self-kb/
#   bash scripts/dogfood.sh /tmp/sk      # 指定别处
#
# 之所以刻意不写 ~/.config/kb/root：那是全局默认知识库指针，
# 脚本不该悄悄改掉用户的选择。后续命令用 --kb 或 $KB_ROOT 指定即可。
set -uo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
KB_BIN="$ROOT_DIR/bin/kb"
KB_ROOT="${1:-$ROOT_DIR/self-kb}"
REPO_ROOT="$ROOT_DIR"

[[ -x "$KB_BIN" ]] || { echo "先执行 make build" >&2; exit 2; }
export KB_NO_REMEMBER=1
export KB_ROOT="$KB_ROOT"

if [[ ! -f "$KB_ROOT/.kb/config.yaml" ]]; then
  "$KB_BIN" init --path "$KB_ROOT" --repo "kb=$REPO_ROOT" >/dev/null
  echo "已初始化自举知识库：$KB_ROOT"
fi

cd "$REPO_ROOT"

add() { # add <症状> <锚点> <类型> <可信度> [正文文件]
  local symptom="$1" at="$2" type="$3" conf="$4" body="${5:-}"
  local args=(add "$symptom" --at "$at" --type "$type" --confidence "$conf")
  [[ -n "$body" ]] && args+=(--body-file "$body")
  "$KB_BIN" "${args[@]}" >/dev/null 2>&1 || true
}

add "改注释也会把笔记标成 stale，因为过期粒度是整个文件" \
    internal/kb/change.go constraint verified
add "webhook 只处理主干分支，feature 分支的提交不会误伤笔记" \
    internal/webhook/webhook.go constraint verified
add "路径迁移后同文件的多条笔记会互相覆盖，靠 docSuffix 保留 -id 后缀" \
    internal/kb/store.go incident verified
add "Go RE2 不支持 \\uXXXX，Unicode 码点必须写 \\x{4e00}" \
    internal/pathmap/pathmap.go incident verified
add "检索打分权重：症状 12 / 关键词 10 / 路径 5 / 锚点 4 / 正文 3" \
    internal/cli/entry.go note unverified

"$KB_BIN" add "为什么 md 是唯一事实来源，manifest 只是派生索引" --type decision --confidence verified >/dev/null 2>&1 || true

echo
"$KB_BIN" install-hook "$REPO_ROOT"
echo
echo "自举完成。常用命令（--kb 可省略，因为已导出 KB_ROOT）："
echo "    kb --kb $KB_ROOT status          # 全局状态 + 过期列表"
echo "    kb --kb $KB_ROOT search 哈希      # 按症状检索"
echo "    kb --kb $KB_ROOT serve           # 起服务（Web UI 默认 7778）"
