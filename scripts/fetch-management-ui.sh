#!/usr/bin/env bash
# 只获取固定官方静态管理页面并验证摘要；不执行下载脚本，不覆盖已有产物。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DIR="$ROOT/.build/assets/management-v1.26.2"
if [[ ! -e "$DIR" ]]; then
  mkdir -p "$ROOT/.build/assets"
  mkdir "$DIR"
  curl --fail --location 'https://github.com/router-for-me/Cli-Proxy-API-Management-Center/releases/download/v1.26.2/management.html' -o "$DIR/management.html"
fi
python3 -I - "$DIR/management.html" <<'PY'
# 下载物只当数据读取，不从其目录启动解释器或安装器。
import hashlib, pathlib, sys
actual = hashlib.sha256(pathlib.Path(sys.argv[1]).read_bytes()).hexdigest()
assert actual == '808cfd05ce5766a0f3854f8e095e5c5ea6e4313399f2df0d52b7a40352be29df', '管理页面缺失或摘要不匹配；不自动覆盖已有目录'
PY
printf '%s\n' '固定管理页面校验通过；仅在隔离合成实例使用。'
