// 本文件加载仅测试使用的真实 native 插件，从原 Chat HTTP 回调发起嵌套执行，验证它不是新的客户端提交。
package system

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// buildHTTPOnceFixture 在全新隔离目录编译 ABI 夹具并复制本次已构建审计库；返回含审计及仅夹具的两个插件目录。
// 只使用第一方源码和本机平台；构建、复制或平台不支持终止测试，不修改默认产物目录，不自动删除数据。
func buildHTTPOnceFixture(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal("嵌套夹具仓库路径无效")
	}
	if err := os.MkdirAll(filepath.Join(root, ".test-data"), 0700); err != nil {
		t.Fatal("嵌套夹具验收根目录创建失败")
	}
	work, err := os.MkdirTemp(filepath.Join(root, ".test-data"), "native-http-once-")
	if err != nil {
		t.Fatal("嵌套夹具隔离目录创建失败")
	}
	withAudit, withoutAudit := filepath.Join(work, "with-audit"), filepath.Join(work, "without-audit")
	for _, dir := range []string{withAudit, withoutAudit} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal("嵌套夹具插件目录创建失败")
		}
	}
	suffix := ".so"
	if runtime.GOOS == "darwin" {
		suffix = ".dylib"
	} else if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		t.Fatal("嵌套 native 夹具未支持本平台")
	}
	fixture := filepath.Join(withoutAudit, "http-once-fixture"+suffix)
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", fixture, "./tests/fixtures/http-once-plugin")
	cmd.Dir = root
	if err := cmd.Run(); err != nil {
		t.Fatal("嵌套 native 夹具构建失败")
	}
	for source, target := range map[string]string{
		fixture: filepath.Join(withAudit, "http-once-fixture"+suffix),
		filepath.Join(root, ".build/plugins/request-input-audit"+suffix): filepath.Join(withAudit, "request-input-audit"+suffix),
	} {
		raw, err := os.ReadFile(source)
		if err != nil || os.WriteFile(target, raw, 0700) != nil {
			t.Fatal("嵌套夹具原生库复制失败")
		}
	}
	return withAudit, withoutAudit
}

// TestHTTPOnceNestedExecution 验证原 HTTP context 内两次真实模型执行只审计客户端原输入，插件 callback 不新增记录或改变响应。
func TestHTTPOnceNestedExecution(t *testing.T) {
	withAudit, withoutAudit := buildHTTPOnceFixture(t)
	received := make(chan []byte, 8)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if respondDiscovery(w, r) {
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(400)
			return
		}
		received <- raw
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, httpOnceReply)
	}))
	t.Cleanup(upstream.Close)
	pluginConfig := "    http-once-fixture:\n      enabled: true\n"
	h := startHostWithPluginDirectory(t, 1<<30, true, upstream.URL+"/v1", true, "", withAudit, pluginConfig)
	baseline := startHostWithPluginDirectory(t, 1<<30, true, upstream.URL+"/v1", true, "", withoutAudit, pluginConfig)
	h.monitorAll(t)
	_, _, gaps := snapshotCounters(t, h)
	for round := 0; round < 2; round++ {
		marker := fmt.Sprintf("HTTP-NESTED-%d-%d", round, time.Now().UnixNano())
		body := snapshotInput(t, marker)
		code, raw := h.request(t, "POST", "/v1/chat/completions", keyA, body)
		originalCode, originalRaw := baseline.request(t, "POST", "/v1/chat/completions", keyA, body)
		if code != 200 || originalCode != code || !bytes.Equal(originalRaw, raw) {
			t.Fatal("观察改变了嵌套执行后的原 HTTP 响应")
		}
		// 独立字面预期：callback 的内部输入与原客户端输入不同，并且实际各执行一次；不是私有回调计数。
		nested := []byte(fmt.Sprintf(`{"messages":[{"content":%q,"role":"user"}],"model":"audit-test-model"}`, "FORBIDDEN_NESTED_EXECUTION-"+marker))
		for _, want := range [][]byte{nested, body, nested, body} {
			select {
			case request := <-received:
				if !bytes.Equal(request, want) {
					t.Fatalf("嵌套内部执行或原始客户端执行未真实发生/正文被改变：nested=%t original=%t", bytes.Contains(request, []byte("FORBIDDEN_NESTED_EXECUTION")), bytes.Equal(request, body))
				}
			case <-time.After(3 * time.Second):
				t.Fatal("嵌套执行没有到达可控上游")
			}
		}
		waitSnapshotRecord(t, h, marker)
		assertHTTPOnceState(t, h, int64(round+1), gaps)
	}
	assertChatPrivacy(t, h, "FORBIDDEN_NESTED_EXECUTION", "FORBIDDEN_HTTP_ONCE_REPLY")
}
