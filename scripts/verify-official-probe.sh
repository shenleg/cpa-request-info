#!/usr/bin/env bash
# 在全新目录构建精确官方宿主和实验探针，运行合成集成验证；不打补丁、不部署、不删除文件。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
COMMIT=bf9341629ef2300ac9d846a2e80fe2756f63a671
SOURCE="${CPA_OFFICIAL_SOURCE:-$ROOT/.build/sources/CLIProxyAPI-v8.0.27}"
git -C "$SOURCE" cat-file -e "$COMMIT^{commit}"
mkdir -p "$ROOT/.build"
OUT="$(mktemp -d "$ROOT/.build/official-probe.XXXXXX")"
mkdir "$OUT/source" "$OUT/bin" "$OUT/plugins" "$OUT/nested-plugins"
printf '官方插件实验目录：%s\n' "$OUT"
# 导出对象而非缓存工作树；不借用已有修改，也不应用本项目宿主补丁。
git -C "$SOURCE" archive "$COMMIT" | tar -x -C "$OUT/source"
CGO_ENABLED=1 go -C "$OUT/source" build -o "$OUT/bin/cliproxyapi" ./cmd/server
case "$(go env GOOS)" in
  darwin) SUFFIX=dylib ;;
  linux) SUFFIX=so ;;
  *) printf '%s\n' '本实验只验收 macOS 和 Linux。' >&2; exit 1 ;;
esac
CGO_ENABLED=1 go -C "$ROOT" build -buildmode=c-shared -o "$OUT/plugins/official-probe.$SUFFIX" ./tests/fixtures/official-probe
CGO_ENABLED=1 go -C "$ROOT" build -buildmode=c-shared -o "$OUT/nested-plugins/http-once-fixture.$SUFFIX" ./tests/fixtures/http-once-plugin
cp "$OUT/plugins/official-probe.$SUFFIX" "$OUT/nested-plugins/"
python3 -I - "$OUT" "$COMMIT" "$(git -C "$SOURCE" rev-parse "$COMMIT^{tree}")" "$ROOT" <<'PY'
# 只记录固定输入、工具链与构建产物摘要，不读取合成配置或宿主日志。
import hashlib
import json
import pathlib
import subprocess
import sys
out = pathlib.Path(sys.argv[1])
root = pathlib.Path(sys.argv[4])
sources = ['Makefile', 'scripts/verify-official-probe.sh', 'tests/fixtures/official-probe/main.go', 'tests/fixtures/http-once-plugin/main.go', 'tests/system/official_probe_test.go', 'go.mod', 'go.sum']
artifacts = [out / 'bin/cliproxyapi', *sorted((out / 'plugins').glob('*')), *sorted((out / 'nested-plugins').glob('*'))]
metadata = {
    'official_commit': sys.argv[2],
    'official_tree': sys.argv[3],
    'patch_applied': False,
    'go_version': subprocess.check_output(['go', 'version'], text=True).strip(),
    'target': json.loads(subprocess.check_output(['go', 'env', '-json', 'GOOS', 'GOARCH'], text=True)),
    'source_sha256': {p: hashlib.sha256((root / p).read_bytes()).hexdigest() for p in sources},
    'sha256': {str(p.relative_to(out)): hashlib.sha256(p.read_bytes()).hexdigest() for p in artifacts},
}
(out / 'build.json').write_text(json.dumps(metadata, indent=2) + '\n')
PY
CPA_OFFICIAL_PROBE_DIR="$OUT" go -C "$ROOT" test -tags officialprobe ./tests/system -run '^TestOfficialProbe' -count=1 -v | tee "$OUT/test.log"
printf '有限结果：%s/result.json；构建证据：%s/build.json\n' "$OUT" "$OUT"
