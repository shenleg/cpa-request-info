#!/usr/bin/env bash
# 构建独立官方宿主与带审计时钟的验收插件；不修改宿主源码，不覆盖普通产物。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
COMMIT=bf9341629ef2300ac9d846a2e80fe2756f63a671
SOURCE="${CPA_OFFICIAL_SOURCE:-$ROOT/.build/sources/CLIProxyAPI-v8.0.27}"
OUT="$ROOT/.build/retention-test"
mkdir -p "$OUT/sources" "$OUT/bin" "$OUT/plugins"
git -C "$SOURCE" cat-file -e "$COMMIT^{commit}"
WORK="$(cd "$(mktemp -d "$OUT/sources/official-host.XXXXXX")" && pwd -P)"
git -C "$SOURCE" archive "$COMMIT" | tar -x -C "$WORK"
CGO_ENABLED=1 go -C "$WORK" build -o "$OUT/bin/cliproxyapi" ./cmd/server
case "$(go env GOOS)" in
  darwin) SUFFIX=dylib ;;
  linux) SUFFIX=so ;;
  *) printf '%s\n' '当前只验收 macOS 和 Linux。' >&2; exit 1 ;;
esac
# Before 观察时间和存储维护统一使用插件内部 auditNow，测试时钟无需改写官方 HTTP/WS 读取器。
CGO_ENABLED=1 go -C "$ROOT" build -tags audittest -buildmode=c-shared -o "$OUT/plugins/request-input-audit.$SUFFIX" ./cmd/request-input-audit
printf '官方宿主与隔离时钟插件：%s\n官方源码：%s\n' "$OUT" "$WORK"
