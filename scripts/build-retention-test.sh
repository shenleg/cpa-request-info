#!/usr/bin/env bash
# 从固定宿主对象和正式补丁独立构建时钟验收产物；仅 overlay 审计接收取时，不改普通产物或删除源码。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMMIT=bf9341629ef2300ac9d846a2e80fe2756f63a671
SOURCE="$ROOT/.build/sources/CLIProxyAPI-v8.0.27"
OUT="$ROOT/.build/retention-test"
mkdir -p "$ROOT/.build/sources" "$OUT/sources" "$OUT/bin" "$OUT/plugins"
if [[ ! -d "$SOURCE" ]]; then
  git clone --depth 1 --branch v8.0.27 https://github.com/router-for-me/CLIProxyAPI.git "$SOURCE"
fi
[[ "$(git -C "$SOURCE" rev-parse HEAD)" == "$COMMIT" ]] || { printf '%s\n' '宿主 commit 不匹配，停止构建。' >&2; exit 1; }
python3 -I - "$ROOT" "$COMMIT" <<'PY'
# 与普通构建使用相同的固定对象和补丁摘要，不复用可能被修改的已打补丁工作树。
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
manifest = json.loads((root / 'host/manifest.json').read_text())
assert manifest['commit'] == sys.argv[2], '宿主清单 commit 不匹配'
for item in manifest['patches']:
    actual = hashlib.sha256((root / 'host' / item['path']).read_bytes()).hexdigest()
    assert actual == item['sha256'], '宿主补丁摘要不匹配'
PY
WORK="$(mktemp -d "$OUT/sources/host.XXXXXX")"
git -C "$SOURCE" archive "$COMMIT" | tar -x -C "$WORK"
PATCH="$ROOT/host/patches/0001-readonly-input-audit.patch"
patch -d "$WORK" -p1 --batch --fuzz=0 --dry-run < "$PATCH"
patch -d "$WORK" -p1 --batch --fuzz=0 < "$PATCH"
OVERLAY_DIR="$(mktemp -d "$OUT/sources/clock-overlay.XXXXXX")"
OVERLAY="$(python3 -I "$ROOT/scripts/retention-clock-overlay.py" "$WORK" "$ROOT" "$OVERLAY_DIR")"
gofmt -w "$OVERLAY_DIR/input_audit.go"
CGO_ENABLED=1 go -C "$WORK" build -overlay "$OVERLAY" -o "$OUT/bin/cliproxyapi" ./cmd/server
case "$(go env GOOS)" in
  darwin) SUFFIX=dylib ;;
  linux|freebsd) SUFFIX=so ;;
  *) printf '%s\n' '此平台没有已验证的 native 插件构建配置。' >&2; exit 1 ;;
esac
CGO_ENABLED=1 go -C "$ROOT" build -tags audittest -buildmode=c-shared -o "$OUT/plugins/request-input-audit.$SUFFIX" ./cmd/request-input-audit
printf '已构建隔离时钟宿主与插件：%s\n隔离源码：%s\nGo overlay：%s\n' "$OUT" "$WORK" "$OVERLAY"
printf '%s\n' '仅用于合成验收；普通 .build/bin 与 .build/plugins 未改动。'
