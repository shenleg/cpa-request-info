#!/usr/bin/env bash
# 检查指定仓库的第一方 Go 格式、静态分析和测试编译；离线执行，不启动宿主。
set -euo pipefail
ROOT="$(cd "${1:-$(dirname "$0")/..}" && pwd -P)"
# 此命令检查指定根目录，不借用调用者的 index、worktree 或 repository 环境。
for name in ${!GIT_@}; do unset "$name"; done
export GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly GOTOOLCHAIN=local GOWORK=off
# 快照在源仓库的忽略子目录中；缺少自身模块时 Go 会静默向上找到工作树 go.mod。
[[ -f "$ROOT/go.mod" && ! -L "$ROOT/go.mod" ]] || { printf '%s\n' '检查根目录缺少独立 go.mod（不允许借用上层模块）。' >&2; exit 1; }
# 快照必须显式给 tracked manifest，绝不向上发现原仓库；工作树仅用 Git 清单。
MANIFEST="${2:-}"
if [[ -z "$MANIFEST" ]]; then
  [[ -e "$ROOT/.git" ]] || { printf '%s\n' '检查根目录不是独立 Git 仓库且未提供快照清单。' >&2; exit 1; }
  mkdir -p "$ROOT/.test-data"
  MANIFEST="$(mktemp "$ROOT/.test-data/tooling-fast-paths.XXXXXX")"
  # 先完成 Git 查询再读取清单，避免 process substitution 吞掉 Git 的失败退出码。
  git -C "$ROOT" ls-files --cached --others --exclude-standard -z > "$MANIFEST"
else
  [[ -f "$MANIFEST" && ! -e "$ROOT/.git" ]] || { printf '%s\n' '快照清单不存在或快照含 Git 元数据。' >&2; exit 1; }
fi
FILES=()
while IFS= read -r -d '' path; do
  case "$path" in
    .build/*|.test-data/*|node_modules/*|vendor/*|*/node_modules/*|*/vendor/*) continue ;;
    *.go) [[ -f "$ROOT/$path" ]] && FILES+=("$ROOT/$path") ;;
  esac
done < "$MANIFEST"
if [[ ${#FILES[@]} -gt 0 ]]; then
  BAD="$(gofmt -l "${FILES[@]}")"
  [[ -z "$BAD" ]] || { printf 'gofmt 检查失败（只报告，不修改）：\n%s\n' "$BAD" >&2; exit 1; }
fi
go -C "$ROOT" vet ./...
go -C "$ROOT" test ./... -run '^$'
printf '%s\n' '快速检查通过（gofmt、go vet、测试编译；未运行宿主或业务验收）。'
