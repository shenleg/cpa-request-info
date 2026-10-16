// 本文件是原版 CPA 隔离实验的 C ABI 探针；只在内存保留有界摘要，不修改请求，也不保存正文、请求头或凭据。
package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; cliproxy_plugin_call_fn call; cliproxy_plugin_free_fn free_buffer; cliproxy_plugin_shutdown_fn shutdown; } cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static cliproxy_host_api host_api;
static void retain_host(const cliproxy_host_api* h) { host_api = *h; }
static int call_host(const char* m, const uint8_t* b, size_t n, cliproxy_buffer* r) { return host_api.call(host_api.host_ctx,m,b,n,r); }
static void release_host(void* p,size_t n) { host_api.free_buffer(p,n); }
*/
import "C"

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"unsafe"
)

// 合成请求远小于 1 MiB；限制 ABI 复制预算并保证 C.GoBytes 长度不会溢出，不代表生产请求上限。
const fixtureRPCBytes = 1 << 20

// 两轮实验各几十个请求，每次至多记录 before/after/complete；512 条为有界余量，超出不覆盖旧证据。
const maxEvents = 512

var markerPattern = regexp.MustCompile(`PROBE-[A-Za-z0-9_-]+`)
var state = struct {
	sync.Mutex
	events  []event
	dropped uint64
}{events: make([]event, 0, maxEvents)}

type event struct {
	Phase              string   `json:"phase"`
	RequestID          string   `json:"request_id"`
	TraceID            string   `json:"trace_id"`
	SourceFormat       string   `json:"source_format"`
	Model              string   `json:"model"`
	RequestedModel     string   `json:"requested_model"`
	BodyModel          string   `json:"body_model"`
	CallerScopeHash    string   `json:"caller_scope_hash"`
	BodySHA256         string   `json:"body_sha256"`
	Markers            []string `json:"markers"`
	HasSystem          bool     `json:"has_system"`
	HasHistory         bool     `json:"has_history"`
	HasTools           bool     `json:"has_tools"`
	CredentialsPresent bool     `json:"credentials_present"`
	Outcome            string   `json:"outcome"`
}

type managementRequest struct {
	Method, Path string
	Body         []byte
	Query        map[string][]string
}

func main() {}

// cliproxy_plugin_init 校验宿主 ABI 和回调表并安装探针入口；不取得参数所有权，成功返回 0，无效参数返回 1。
//
//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || host.abi_version != 1 || host.call == nil || host.free_buffer == nil {
		return 1
	}
	C.retain_host(host)
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

// cliproxyPluginCall 在回调内读取借用输入并返回宿主须释放的成功 envelope；非法参数、方法或 JSON 返回 1，不回显输入。
//
//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, size C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil || method == nil || uint64(size) > fixtureRPCBytes || (size > 0 && request == nil) {
		return 1
	}
	response.ptr, response.len = nil, 0
	var result any = struct{}{}
	switch name := C.GoString(method); name {
	case "plugin.register", "plugin.reconfigure":
		result = map[string]any{
			"schema_version": 6,
			"metadata": map[string]string{
				"Name": "official-probe", "Version": "0.0.0", "Author": "cpa-request-info",
				"GitHubRepository": "https://github.com/shenleg/cpa-request-info",
			},
			"capabilities": map[string]bool{"request_interceptor": true, "request_lifecycle_plugin": true, "management_api": true},
		}
	case "request.intercept_before", "request.intercept_after", "request.complete":
		phase := "complete"
		if name == "request.intercept_before" {
			phase = "before"
		} else if name == "request.intercept_after" {
			phase = "after"
		}
		if !recordEvent(C.GoBytes(unsafe.Pointer(request), C.int(size)), phase) {
			return 1
		}
		// 空结果不会设置 Body、Headers 或 Terminate，确保探针不改变执行行为。
	case "management.register":
		type route struct{ Method, Path string }
		type resource struct{ Path, Menu, Description string }
		result = struct {
			Routes    []route    `json:"routes"`
			Resources []resource `json:"resources"`
		}{[]route{{"GET", "/plugins/official-probe/report"}}, []resource{{"/probe.txt", "官方探针", "无请求数据的固定实验资源"}}}
	case "management.handle":
		var req managementRequest
		if json.Unmarshal(C.GoBytes(unsafe.Pointer(request), C.int(size)), &req) != nil {
			return 1
		}
		result = management(req)
	case "plugin.quiesce", "plugin.shutdown":
		// 无后台任务、文件或连接；保留摘要直到宿主卸载，避免异步 complete 与清空状态竞争。
	default:
		return 1
	}
	raw, err := json.Marshal(map[string]any{"ok": true, "result": result})
	if err != nil {
		return 1
	}
	response.ptr = C.CBytes(raw)
	if response.ptr == nil {
		return 1
	}
	response.len = C.size_t(len(raw))
	return 0
}

// cliproxyPluginFree 释放本探针分配的响应；ptr 不可来自其他模块，size 为 ABI 长度，无返回值或异常。
//
//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, size C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

// cliproxyPluginShutdown 结束无持久资源的探针；不清空仍可能被异步 complete 使用的摘要，无参数、返回值或异常。
//
//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

// recordEvent 从官方 Go 字段名的 raw 读取 phase 所需摘要并并发安全地追加；输入仅在本次调用存活，非法 JSON 返回 false。
func recordEvent(raw []byte, phase string) bool {
	var req struct {
		RequestID, TraceID, SourceFormat, Model, RequestedModel, Outcome string
		Metadata                                                         struct {
			CallerScope string `json:"caller_scope"`
		}
	}
	if json.Unmarshal(raw, &req) != nil {
		return false
	}
	e := event{
		Phase: phase, RequestID: req.RequestID, TraceID: req.TraceID, SourceFormat: req.SourceFormat,
		Model: req.Model, RequestedModel: req.RequestedModel, Markers: []string{},
	}
	if req.Metadata.CallerScope != "" {
		e.CallerScopeHash = digest([]byte(req.Metadata.CallerScope))
	}
	if phase == "complete" {
		// 完成回调只解析 ID、模型、来源、身份摘要和结果；不读取正文、请求头或错误文本。
		e.Outcome = req.Outcome
	} else {
		var intercepted struct {
			Body    []byte
			Headers http.Header
		}
		if json.Unmarshal(raw, &intercepted) != nil {
			return false
		}
		if intercepted.Body != nil {
			e.BodySHA256 = digest(intercepted.Body)
		}
		var body struct {
			Model json.RawMessage `json:"model"`
		}
		if json.Unmarshal(intercepted.Body, &body) == nil {
			// 请求正文不一定是 JSON，model 也不一定是字符串；这些情况只表示没有 body_model。
			_ = json.Unmarshal(body.Model, &e.BodyModel)
		}
		seen := make(map[string]bool)
		for _, match := range markerPattern.FindAll(intercepted.Body, -1) {
			marker := string(match)
			if !seen[marker] {
				e.Markers = append(e.Markers, marker)
				seen[marker] = true
			}
		}
		e.HasSystem = bytes.Contains(intercepted.Body, []byte("FORBIDDEN_MATRIX_SYSTEM")) || bytes.Contains(intercepted.Body, []byte("FORBIDDEN_PROBE_SYSTEM"))
		e.HasHistory = bytes.Contains(intercepted.Body, []byte("FORBIDDEN_MATRIX_HISTORY")) || bytes.Contains(intercepted.Body, []byte("FORBIDDEN_PROBE_HISTORY"))
		e.HasTools = bytes.Contains(intercepted.Body, []byte("FORBIDDEN_PROBE_TOOL"))
		for name := range intercepted.Headers {
			switch strings.ToLower(name) {
			case "authorization", "proxy-authorization", "x-api-key", "api-key", "x-goog-api-key", "x-auth-token", "x-access-token", "cookie":
				e.CredentialsPresent = true
			}
		}
	}
	state.Lock()
	defer state.Unlock()
	if len(state.events) == maxEvents {
		state.dropped++
		return true
	}
	state.events = append(state.events, e)
	return true
}

// digest 返回 value 的 SHA-256 十六进制摘要，不保留原字节，无错误。
func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// management 处理已由宿主鉴权的 req，返回与 native 相同的响应结构；报告只含摘要，静态资源永不包含实验结果。
func management(req managementRequest) any {
	if req.Method != "GET" {
		return response(http.StatusMethodNotAllowed, "text/plain; charset=utf-8", []byte("method not allowed\n"))
	}
	if strings.HasSuffix(req.Path, "/probe.txt") {
		return response(http.StatusOK, "text/plain; charset=utf-8", []byte("official-probe static resource\n"))
	}
	if !strings.HasSuffix(req.Path, "/plugins/official-probe/report") {
		return response(http.StatusNotFound, "text/plain; charset=utf-8", []byte("not found\n"))
	}
	probe := extensionProbe()
	state.Lock()
	report := struct {
		ExtensionProbe string  `json:"extension_probe"`
		Events         []event `json:"events"`
		Dropped        uint64  `json:"dropped"`
	}{probe, append([]event{}, state.events...), state.dropped}
	state.Unlock()
	raw, err := json.Marshal(report)
	if err != nil {
		return response(http.StatusInternalServerError, "application/json", []byte(`{"error":"serialization_failed"}`))
	}
	return response(http.StatusOK, "application/json", raw)
}

// response 用 code、contentType 和 body 构造官方管理响应；无 I/O、认证或错误。
func response(code int, contentType string, body []byte) any {
	return struct {
		StatusCode int
		Headers    http.Header
		Body       []byte
	}{code, http.Header{"Content-Type": {contentType}, "Cache-Control": {"no-store"}, "X-Content-Type-Options": {"nosniff"}}, body}
}

// extensionProbe 通过官方 HostCall 检查独立扩展是否存在；只将精确的方法缺失错误判为 unsupported，其余失败返回 unavailable。
func extensionProbe() string {
	const method = "host.input_audit.capabilities"
	m := C.CString(method)
	payload := []byte(`{}`)
	b := C.CBytes(payload)
	defer C.free(unsafe.Pointer(m))
	defer C.free(b)
	var result C.cliproxy_buffer
	code := C.call_host(m, (*C.uint8_t)(b), C.size_t(len(payload)), &result)
	if result.ptr != nil {
		defer C.release_host(result.ptr, result.len)
	}
	if code != 0 || result.ptr == nil || uint64(result.len) > fixtureRPCBytes {
		return "unavailable"
	}
	var envelope struct {
		OK    bool `json:"ok"`
		Error struct {
			Code, Message string
		} `json:"error"`
	}
	if json.Unmarshal(C.GoBytes(result.ptr, C.int(result.len)), &envelope) != nil {
		return "unavailable"
	}
	if envelope.OK {
		return "present"
	}
	if envelope.Error.Code == "unknown_method" || envelope.Error.Code == "method_not_found" || envelope.Error.Code == "unsupported_method" ||
		(envelope.Error.Code == "host_call_failed" && envelope.Error.Message == "unsupported host callback "+method) {
		return "unsupported"
	}
	return "unavailable"
}
