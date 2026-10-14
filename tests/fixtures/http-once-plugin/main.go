// 本文件是仅在隔离系统测试加载的 native 插件：原请求拦截中调用官方 host.model.execute，再原样放行。
// 不注册审计观察者，不提供生产故障 API，不保存输入、认证头、执行结果或请求标识。
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
	"encoding/json"
	"strings"
	"unsafe"
)

// 合成请求及固定回复远小于 1 MiB；限制 ABI 借用/复制预算，也保证 C.GoBytes 长度不会溢出，非生产协议上限。
const fixtureRPCBytes = 1 << 20

func main() {}

// cliproxy_plugin_init 校验 ABI 与宿主回调表并注册夹具入口；成功返回0，无效指针或 ABI 返回1，不取得参数所有权。
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

// cliproxyPluginCall 同步处理真实注册/拦截 RPC；输入只在回调期间借用，返回宿主须释放的响应。
// 不支持、编码或嵌套执行失败返回固定非零结果，不输出请求/响应或凭据。
//
//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, size C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil || method == nil || uint64(size) > fixtureRPCBytes || (size > 0 && request == nil) {
		return 1
	}
	response.ptr, response.len = nil, 0
	result := any(struct{}{})
	switch C.GoString(method) {
	case "plugin.register", "plugin.reconfigure":
		result = map[string]any{"schema_version": 6, "metadata": map[string]string{"Name": "HTTP once native fixture", "Version": "0.0.0", "Author": "cpa-request-info", "GitHubRepository": "https://github.com/shenleg/cpa-request-info"}, "capabilities": map[string]bool{"request_interceptor": true}}
	case "request.intercept_before":
		if !executeNested(C.GoBytes(unsafe.Pointer(request), C.int(size))) {
			return 1
		}
	case "request.intercept_after", "plugin.quiesce", "plugin.shutdown":
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

// cliproxyPluginFree 释放夹具分配的响应；ptr 仅可来自本插件，size 为 ABI 传入长度，无返回值或异常。
//
//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, size C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

// cliproxyPluginShutdown 结束无状态夹具；无参数、返回值、持久化资源或异常。
//
//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

// executeNested 对唯一合成前缀通过官方 callback 在原 HTTP context 中发起内部模型执行；返回是否成功。
// raw 是借用的原生拦截 RPC；无关请求原样通过，解析、callback 或执行失败返回false，不保留输入。
func executeNested(raw []byte) bool {
	var req struct {
		Body           []byte
		Model          string
		HostCallbackID string `json:"host_callback_id"`
	}
	if json.Unmarshal(raw, &req) != nil {
		return false
	}
	var body struct {
		Messages []struct{ Role, Content string }
	}
	if json.Unmarshal(req.Body, &body) != nil || len(body.Messages) == 0 {
		return true
	}
	last := body.Messages[len(body.Messages)-1]
	if last.Role != "user" || !strings.HasPrefix(last.Content, "HTTP-NESTED-") {
		return true
	}
	if req.HostCallbackID == "" {
		return false
	}
	nested, err := json.Marshal(map[string]any{"model": req.Model, "messages": []map[string]string{{"role": "user", "content": "FORBIDDEN_NESTED_EXECUTION-" + last.Content}}})
	if err != nil {
		return false
	}
	payload, err := json.Marshal(map[string]any{"entry_protocol": "openai", "exit_protocol": "openai", "model": req.Model, "stream": false, "body": nested, "host_callback_id": req.HostCallbackID})
	if err != nil {
		return false
	}
	// callback ID 绑定当前宿主请求，并让宿主跳过调用者拦截器；不自行调用 handler 或伪造客户端提交。
	method := C.CString("host.model.execute")
	request := C.CBytes(payload)
	defer C.free(unsafe.Pointer(method))
	defer C.free(request)
	var result C.cliproxy_buffer
	code := C.call_host(method, (*C.uint8_t)(request), C.size_t(len(payload)), &result)
	if result.ptr != nil {
		defer C.release_host(result.ptr, result.len)
	}
	if code != 0 || result.ptr == nil || uint64(result.len) > fixtureRPCBytes {
		return false
	}
	var envelope struct {
		OK     bool `json:"ok"`
		Result struct {
			StatusCode int `json:"status_code"`
		} `json:"result"`
	}
	return json.Unmarshal(C.GoBytes(result.ptr, C.int(result.len)), &envelope) == nil && envelope.OK && envelope.Result.StatusCode == 200
}
