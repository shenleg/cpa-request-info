#!/usr/bin/env bash
# 从固定 Git 对象导出全新隔离目录，严格应用补丁，再成对构建；不删除既有源码或验收数据。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMMIT=bf9341629ef2300ac9d846a2e80fe2756f63a671
SOURCE="$ROOT/.build/sources/CLIProxyAPI-v8.0.27"
PATCH="$ROOT/host/patches/0001-readonly-input-audit.patch"
mkdir -p "$ROOT/.build/sources" "$ROOT/.build/bin" "$ROOT/.build/plugins"
if [[ ! -d "$SOURCE" ]]; then
  git clone --depth 1 --branch v8.0.27 https://github.com/router-for-me/CLIProxyAPI.git "$SOURCE"
fi
[[ "$(git -C "$SOURCE" rev-parse HEAD)" == "$COMMIT" ]] || { printf '%s\n' '宿主 commit 不匹配，停止构建。' >&2; exit 1; }
python3 -I - "$ROOT" <<'PY'
# 清单和补丁均属于本项目；校验固定摘要，不把已有工作树的额外改动混入构建。
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
manifest = json.loads((root / 'host/manifest.json').read_text())
assert manifest['commit'] == 'bf9341629ef2300ac9d846a2e80fe2756f63a671', '宿主清单 commit 不匹配'
for item in manifest['patches']:
    actual = hashlib.sha256((root / 'host' / item['path']).read_bytes()).hexdigest()
    assert actual == item['sha256'], '宿主补丁摘要不匹配'
PY
# 新目录避免借用曾经修改的工作树，也不以 reset/clean 清除用户数据。
WORK="$(mktemp -d "$ROOT/.build/sources/patched-host.XXXXXX")"
git -C "$SOURCE" archive "$COMMIT" | tar -x -C "$WORK"
patch -d "$WORK" -p1 --batch --fuzz=0 --dry-run < "$PATCH"
patch -d "$WORK" -p1 --batch --fuzz=0 < "$PATCH"
CGO_ENABLED=1 go -C "$WORK" build -o "$ROOT/.build/bin/cliproxyapi" ./cmd/server
case "$(go env GOOS)" in
  darwin) SUFFIX=dylib ;;
  linux|freebsd) SUFFIX=so ;;
  *) printf '%s\n' '当前仅验证 macOS；其他平台未完成运行验收。' >&2; exit 1 ;;
esac
CGO_ENABLED=1 go -C "$ROOT" build -buildmode=c-shared -o "$ROOT/.build/plugins/request-input-audit.$SUFFIX" ./cmd/request-input-audit
printf '已构建宿主与原生插件；隔离源码保留于 %s\n' "$WORK"
printf '%s\n' '未部署；请仅用合成配置验收。清理已有目录需要单独确认。'
