#!/usr/bin/env bash
# 从精确官方 Git 对象全新导出构建开发宿主，不应用任何补丁；独立插件可用 build-plugin.sh 构建。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
COMMIT=bf9341629ef2300ac9d846a2e80fe2756f63a671
SOURCE="${CPA_OFFICIAL_SOURCE:-$ROOT/.build/sources/CLIProxyAPI-v8.0.27}"
mkdir -p "$ROOT/.build/sources" "$ROOT/.build/bin"
if [[ ! -d "$SOURCE" ]]; then
  git clone --depth 1 --branch v8.0.27 https://github.com/router-for-me/CLIProxyAPI.git "$SOURCE"
fi
git -C "$SOURCE" cat-file -e "$COMMIT^{commit}"
python3 -I - "$ROOT" "$COMMIT" <<'PY'
# 只校验当前官方基线；历史补丁文件保留，但不参与正式构建。
import json
import pathlib
import sys
manifest = json.loads((pathlib.Path(sys.argv[1]) / 'host/manifest.json').read_text())
assert manifest['commit'] == sys.argv[2], '宿主清单 commit 不匹配'
assert manifest['patches'] == [], '精简版构建不接受宿主补丁'
PY
WORK="$(cd "$(mktemp -d "$ROOT/.build/sources/official-host.XXXXXX")" && pwd -P)"
git -C "$SOURCE" archive "$COMMIT" | tar -x -C "$WORK"
CGO_ENABLED=1 go -C "$WORK" build -o "$ROOT/.build/bin/cliproxyapi" ./cmd/server
bash "$ROOT/scripts/build-plugin.sh"
printf '开发验收用官方宿主：%s/.build/bin/cliproxyapi\n固定官方源码保留于：%s\n' "$ROOT" "$WORK"
printf '%s\n' '未部署。安装插件到兼容原版 CPA 不需要替换此开发宿主。'
