#!/usr/bin/env bash
# 仅启用当前仓库自带的 Git hook；遇到既有配置或 hook 时停止，不覆盖用户设置。
set -euo pipefail
ROOT="$(cd "${1:-$(dirname "$0")/..}" && pwd -P)"
# 安装不得因调用者的 Git 定位变量而修改其他仓库的配置。
for name in ${!GIT_@}; do unset "$name"; done
[[ -e "$ROOT/.git" ]] || { printf '%s\n' 'hook 安装需要独立 Git 仓库。' >&2; exit 1; }
OWN=.githooks
if CONFIGURED="$(git -C "$ROOT" config --get-all core.hooksPath)"; then
  # 空值也属于用户配置；多值或其他路径必须拒绝，不能用默认值覆盖。
  if [[ "$CONFIGURED" != "$OWN" ]]; then
    printf '既有 core.hooksPath=%s，停止安装，不覆盖。\n' "$CONFIGURED" >&2
    exit 1
  fi
else
  STATUS=$?
  [[ "$STATUS" == 1 ]] || { printf '%s\n' '读取 core.hooksPath 失败，停止安装。' >&2; exit "$STATUS"; }
fi
GITDIR="$(git -C "$ROOT" rev-parse --absolute-git-dir)"
if [[ -e "$GITDIR/hooks/pre-commit" || -L "$GITDIR/hooks/pre-commit" ]]; then
  printf '%s\n' '既有 .git/hooks/pre-commit，停止安装，不覆盖。' >&2
  exit 1
fi
[[ -f "$ROOT/$OWN/pre-commit" && -x "$ROOT/$OWN/pre-commit" ]] || {
  printf '%s\n' '自带 .githooks/pre-commit 不存在或不可执行，停止安装。' >&2
  exit 1
}
git -C "$ROOT" config --local core.hooksPath "$OWN"
printf '%s\n' '已仅在当前仓库启用 .githooks。'
