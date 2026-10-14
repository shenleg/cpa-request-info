#!/usr/bin/env bash
# 从真实 index 导出保留的隔离快照再检查，避免工作树修复掩盖暂存区错误。
set -euo pipefail
ROOT="$(cd "${1:-$(dirname "$0")/..}" && pwd -P)"
FAST="$(cd "$(dirname "$0")" && pwd -P)/check-fast.sh"
[[ -e "$ROOT/.git" ]] || { printf '%s\n' '暂存区检查需要独立 Git 仓库。' >&2; exit 1; }
# 保留仓库的 patch whitespace 属性；不对统一 diff 的上下文前缀做源码式清理。
git -C "$ROOT" diff --cached --check
TREE="$(git -C "$ROOT" write-tree)"
mkdir -p "$ROOT/.test-data"
WORK="$(mktemp -d "$ROOT/.test-data/tooling-index.XXXXXX")"
SNAPSHOT="$WORK/source"
MANIFEST="$WORK/tracked-paths"
mkdir "$SNAPSHOT"
printf '暂存区快照保留于 %s\n' "$WORK"
git -C "$ROOT" ls-tree -r --name-only -z "$TREE" > "$MANIFEST"
git -C "$ROOT" archive "$TREE" | tar -x -C "$SNAPSHOT"
# export-ignore 会静默漏检暂存源码；逐个核对 tree 路径，不允许导出缺失文件。
while IFS= read -r -d '' path; do
  [[ -f "$SNAPSHOT/$path" || -L "$SNAPSHOT/$path" ]] || {
    printf '暂存区导出缺失 tracked 文件（检查 export-ignore 或子模块）：%s\n' "$path" >&2
    exit 1
  }
done < "$MANIFEST"
(
  # Git hook 可传入 index/repository 环境；快照子进程不能借用原仓库或上层仓库。
  for name in ${!GIT_@}; do unset "$name"; done
  bash "$FAST" "$SNAPSHOT" "$MANIFEST"
)
