#!/usr/bin/env bash
# 构建独立兼容性宿主与插件；故障仅存在于宿主 Go overlay，不修改正式产物或加入生产故障入口。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
COMMIT=bf9341629ef2300ac9d846a2e80fe2756f63a671
SOURCE="$ROOT/.build/sources/CLIProxyAPI-v8.0.27"
OUT="$ROOT/.build/compatibility-test"
mkdir -p "$OUT/sources" "$OUT/bin" "$OUT/plugins"
[[ "$(git -C "$SOURCE" rev-parse HEAD)" == "$COMMIT" ]] || { printf '%s\n' '固定宿主源码缺失或 commit 不匹配。' >&2; exit 1; }
python3 -I - "$ROOT" "$COMMIT" <<'PY'
# 摘要检查与正式构建相同，不能把修改后的源码目录当固定基线。
import hashlib, json, pathlib, sys
root = pathlib.Path(sys.argv[1])
manifest = json.loads((root / 'host/manifest.json').read_text())
assert manifest['commit'] == sys.argv[2], '宿主清单 commit 不匹配'
for item in manifest['patches']:
    assert hashlib.sha256((root / 'host' / item['path']).read_bytes()).hexdigest() == item['sha256'], '宿主补丁摘要不匹配'
PY
# Go -C 使用物理目录；overlay 路径也必须解析 .build 的符号链接，否则替换会被静默忽略。
WORK="$(cd "$(mktemp -d "$OUT/sources/host.XXXXXX")" && pwd -P)"
git -C "$SOURCE" archive "$COMMIT" | tar -x -C "$WORK"
PATCH="$ROOT/host/patches/0001-readonly-input-audit.patch"
patch -d "$WORK" -p1 --batch --fuzz=0 --dry-run < "$PATCH"
patch -d "$WORK" -p1 --batch --fuzz=0 < "$PATCH"
OVERLAY_DIR="$(cd "$(mktemp -d "$OUT/sources/overlay.XXXXXX")" && pwd -P)"
python3 -I - "$WORK" "$OVERLAY_DIR" <<'PY'
# 只替代真实宿主契约/注册能力；断言仍经真实鉴权、模型 HTTP 与管理 HTTP，而非插件私有方法。
import json, pathlib, sys
work, out = map(pathlib.Path, sys.argv[1:])
replacements = {}
def rewrite(relative, old, new):
    source = work / relative
    target = out / source.name
    text = target.read_text() if target.exists() else source.read_text()
    assert text.count(old) == 1, f'兼容性 overlay 目标不唯一: {relative}'
    target.write_text(text.replace(old, new))
    replacements[str(source)] = str(target)

rewrite('internal/pluginhost/host_callbacks.go', 'func (h *Host) callFromPlugin(ctx context.Context, method string, request []byte) ([]byte, error) {', '''func (h *Host) callFromPlugin(ctx context.Context, method string, request []byte) ([]byte, error) {
    if method == pluginabi.MethodHostInputAuditCapabilities && h.compatibilityMode() == "final-callback-unavailable" {
        if instance := hostCallbackInstanceFromContext(ctx); instance != nil && instance.closed.Load() {
            return nil, fmt.Errorf("compatibility fixture: final callback unavailable")
        }
    }''')
rewrite('internal/pluginhost/host_callbacks.go', '\tswitch method {', '''\tif method == pluginabi.MethodHostInputAuditCapabilities {
        switch h.compatibilityMode() {
        case "extension-missing", "extension-missing-no-management":
            return nil, fmt.Errorf("unsupported host callback %s", method)
        case "callback-unavailable":
            return nil, fmt.Errorf("compatibility fixture: callback unavailable")
        case "callback-blocked":
            h.waitCompatibilityCapabilities()
        case "version-incompatible":
            dir, _, _ := h.inputAuditConfig()
            return marshalRPCResult(map[string]any{"contract_version": 2, "available": true, "state_dir": dir, "protocols": []string{"chat-completions", "responses", "anthropic-messages", "responses-websocket"}})
        case "protocol-incompatible":
            dir, _, _ := h.inputAuditConfig()
            return marshalRPCResult(map[string]any{"contract_version": 3, "available": true, "state_dir": dir, "protocols": []string{"chat-completions", "responses", "anthropic-messages"}})
        case "bridge-unavailable":
            return marshalRPCResult(map[string]any{"contract_version": 3, "available": false, "reason": "observer_bridge_unavailable", "bridge_failures": h.inputAuditBridgeFailures.Load(), "protocols": []string{"chat-completions", "responses", "anthropic-messages", "responses-websocket"}})
        }
    }
\tswitch method {''')
rewrite('internal/pluginhost/rpc_client.go', '\tadapter := &rpcPluginAdapter{', '''    // Registration fixtures remove actual negotiated capabilities, not the reported status.
    if id == inputAuditPluginID {
        switch host.compatibilityMode() {
        case "observer-missing":
            resp.Capabilities.InputAuditObserver = false
        case "extension-missing-no-management", "management-missing":
            resp.Capabilities.ManagementAPI = false
        }
    }
\tadapter := &rpcPluginAdapter{''')
rewrite('internal/pluginhost/adapters_usage_translation.go', 'func (h *Host) isPluginFused(id string) bool {', '''func (h *Host) isPluginFused(id string) bool {
    if h != nil && id == inputAuditPluginID {
        mode := h.compatibilityMode()
        h.mu.Lock()
        if mode == "fused" { h.fused[id] = "compatibility_fixture" } else if h.fused[id] == "compatibility_fixture" { delete(h.fused, id) }
        h.mu.Unlock()
    }''')
# 新 Go 文件通过 overlay 映射入真实宿主包；控制文件在专用审计目录旁，不进入生产配置或 API。
fixture = out / 'compatibility_fixture.go'
fixture.write_text('''// This test-only overlay selects replacement host contracts without production fault APIs.
package pluginhost
import "os"
import "strings"
import "sync"
import "time"
var compatibilityDirectories sync.Map
func (h *Host) waitCompatibilityCapabilities() {
    dir, _, _ := h.inputAuditConfig()
    if err := os.WriteFile(dir + ".capabilities-entered", []byte("entered"), 0600); err != nil { panic("compatibility fixture barrier failed") }
    for h.compatibilityMode() == "callback-blocked" { time.Sleep(time.Millisecond) }
}
func (h *Host) compatibilityMode() string {
    dir, _, reason := h.inputAuditConfig()
    if reason == "" {
        compatibilityDirectories.Store(h, dir)
    } else {
        // Runtime removal disables configuration before native shutdown; retain only this test control path.
        previous, exists := compatibilityDirectories.Load(h)
        if !exists { return "" }
        dir = previous.(string)
    }
    raw, err := os.ReadFile(dir + ".compatibility")
    if os.IsNotExist(err) { return "" }
    if err != nil { panic("compatibility fixture control unavailable") }
    return strings.TrimSpace(string(raw))
}
''')
replacements[str(work / 'internal/pluginhost/compatibility_fixture.go')] = str(fixture)
(out / 'overlay.json').write_text(json.dumps({'Replace': replacements}))
PY
gofmt -w "$OVERLAY_DIR"/*.go
CGO_ENABLED=1 go -C "$WORK" build -overlay "$OVERLAY_DIR/overlay.json" -o "$OUT/bin/cliproxyapi" ./cmd/server
case "$(go env GOOS)" in
  darwin) SUFFIX=dylib ;;
  linux|freebsd) SUFFIX=so ;;
  *) printf '%s\n' '此平台没有已验证的 native 插件构建配置。' >&2; exit 1 ;;
esac
CGO_ENABLED=1 go -C "$ROOT" build -buildmode=c-shared -o "$OUT/plugins/request-input-audit.$SUFFIX" ./cmd/request-input-audit
printf '兼容性验收产物：%s\n隔离源码：%s\nGo overlay：%s\n' "$OUT" "$WORK" "$OVERLAY_DIR/overlay.json"
