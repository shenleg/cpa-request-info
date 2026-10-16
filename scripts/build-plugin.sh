#!/usr/bin/env bash
# 仅构建可挂载到原版 CPA 的正式插件；不获取、修改或重编宿主。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
case "$(go env GOOS)" in
  darwin) SUFFIX=dylib ;;
  linux) SUFFIX=so ;;
  *) printf '%s\n' '当前只验收 macOS 和 Linux 原生插件。' >&2; exit 1 ;;
esac
mkdir -p "$ROOT/.build/plugins"
CGO_ENABLED=1 go -C "$ROOT" build -buildmode=c-shared -o "$ROOT/.build/plugins/request-input-audit.$SUFFIX" ./cmd/request-input-audit
printf '正式插件：%s/.build/plugins/request-input-audit.%s\n' "$ROOT" "$SUFFIX"
