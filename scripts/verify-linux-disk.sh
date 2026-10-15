#!/usr/bin/env bash
# 本文件提供显式Linux磁盘验收入口；参数交给同名控制器，错误非零退出，不自动提权或删除数据。
set -euo pipefail
exec python3 -I "$(cd "$(dirname "$0")" && pwd)/verify-linux-disk.py" "$@"
