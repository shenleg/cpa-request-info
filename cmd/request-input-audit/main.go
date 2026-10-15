// 本文件实现官方 C ABI 入口；缓冲仅在同步 RPC 中借用，不保留认证头或协议请求指针。
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
	"errors"
	"sync"
	"unsafe"

	"github.com/shenleg/cpa-request-info/internal/native"
)

var lifecycle sync.RWMutex
var app = native.New(callHost)

func main() {}

// cliproxy_plugin_init 校验宿主 ABI 并复制回调表、填充插件表；返回非零表示不支持，不取得指针所有权。
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

// cliproxyPluginCall 处理同步方法并返回由插件拥有的响应；宿主需调用 free_buffer，不得长期保留输入。
// 发生可恢复 panic 或长度非法时只返回固定错误，不输出原始请求；native 进程故障不在保证范围内。
//
//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, size C.size_t, response *C.cliproxy_buffer) (code C.int) {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	defer func() {
		if recover() != nil {
			writeResponse(response, []byte(`{"ok":false,"error":{"code":"plugin_error","message":"plugin_error"}}`))
			code = 1
		}
	}()
	// C.GoBytes 接受有符号 int，避免截断/溢出；上限只限制审计 RPC，永不修改模型请求。
	if method == nil || uint64(size) > uint64(1<<30) || (size > 0 && request == nil) {
		return 1
	}
	lifecycle.RLock()
	defer lifecycle.RUnlock()
	var raw []byte
	if size > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(size))
	}
	result, err := app.Handle(C.GoString(method), raw)
	if err != nil {
		writeResponse(response, []byte(`{"ok":false,"error":{"code":"plugin_error","message":"plugin_error"}}`))
		return 1
	}
	payload, err := json.Marshal(struct {
		OK     bool `json:"ok"`
		Result any  `json:"result"`
	}{true, result})
	if err != nil {
		return 1
	}
	if !writeResponse(response, payload) {
		return 1
	}
	return 0
}

// cliproxyPluginFree 释放插件分配的响应；不接受宿主或其他模块分配的缓冲，无返回值。
//
//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, size C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

// cliproxyPluginShutdown 等待本插件在途回调结束并关闭写入者；宿主在卸载前停止新的回调。
//
//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { lifecycle.Lock(); defer lifecycle.Unlock(); app.Close() }

func writeResponse(response *C.cliproxy_buffer, raw []byte) bool {
	ptr := C.CBytes(raw)
	if ptr == nil {
		return false
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
	return true
}

func callHost(method string, payload any) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("host_request_invalid")
	}
	m := C.CString(method)
	b := C.CBytes(raw)
	defer C.free(unsafe.Pointer(m))
	defer C.free(b)
	var result C.cliproxy_buffer
	code := C.call_host(m, (*C.uint8_t)(b), C.size_t(len(raw)), &result)
	if result.ptr != nil {
		defer C.release_host(result.ptr, result.len)
	}
	if code != 0 || result.ptr == nil || uint64(result.len) > 4<<20 {
		return nil, errors.New("host_unavailable")
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(C.GoBytes(result.ptr, C.int(result.len)), &env); err != nil {
		return nil, errors.New("host_unavailable")
	}
	if !env.OK {
		// 旧宿主只给 host_call_failed；只匹配固定 unknown-method 消息，不把传输故障冒充缺扩展。
		if env.Error.Code == "unknown_method" || env.Error.Code == "method_not_found" || env.Error.Code == "unsupported_method" || (env.Error.Code == "host_call_failed" && env.Error.Message == "unsupported host callback "+method) {
			return nil, errors.New("host_method_unsupported")
		}
		return nil, errors.New("host_unavailable")
	}
	return env.Result, nil
}
