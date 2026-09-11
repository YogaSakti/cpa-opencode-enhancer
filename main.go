// Command opencode-enhancer builds the CLIProxyAPI native plugin that
// shapes outbound requests to OpenCode Zen / OpenCode Go upstreams:
// session header injection, client identity / user-agent rewrite, and
// zen free-tier body cleanup. This file is CGO glue only; every RPC
// method is forwarded into plugin.HandleCall.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

// C helper: Go cannot call a C function pointer stored in a variable, so the
// host call is routed through this static wrapper.
static int call_host(cliproxy_host_call_fn fn, void* ctx, const char* method, const uint8_t* data, size_t len, cliproxy_buffer* out) {
	return fn(ctx, method, data, len, out);
}

static void call_host_free(cliproxy_host_free_fn fn, void* ptr, size_t len) {
	fn(ptr, len);
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"cpa-opencode-enhancer/plugin"
)

func main() {}

// maxRequestLen bounds buffer lengths before size_t→C.int conversion:
// values >= 2^31 truncate NEGATIVE and C.GoBytes would panic.
const maxRequestLen = 1<<31 - 1

var dispatcher = plugin.NewManager()

// Host call state captured at init so the plugin can call back into the host
// (e.g. host.log) from inside request interception.
var (
	hostCallFn C.cliproxy_host_call_fn
	hostFreeFn C.cliproxy_host_free_fn
	hostCtx    unsafe.Pointer
	hostReady  bool
)

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plug *C.cliproxy_plugin_api) C.int {
	if plug == nil {
		return 1
	}
	if host == nil || host.abi_version != C.uint32_t(plugin.ABIVersion) {
		return 1
	}
	hostCallFn = host.call
	hostFreeFn = host.free_buffer
	hostCtx = host.host_ctx
	hostReady = true
	plugin.SetHostCaller(hostCall)
	plug.abi_version = C.uint32_t(plugin.ABIVersion)
	plug.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plug.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plug.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

// hostCall invokes a host RPC method (e.g. "host.log") from plugin code.
func hostCall(method string, payload []byte) ([]byte, error) {
	if !hostReady || hostCallFn == nil {
		return nil, fmt.Errorf("host call not initialized")
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var payloadPtr *C.uint8_t
	var payloadLen C.size_t
	if len(payload) > 0 {
		payloadPtr = (*C.uint8_t)(unsafe.Pointer(&payload[0]))
		payloadLen = C.size_t(len(payload))
	}
	var resp C.cliproxy_buffer
	ret := C.call_host(hostCallFn, hostCtx, cMethod, payloadPtr, payloadLen, &resp)
	if ret != 0 {
		return nil, fmt.Errorf("host call %s failed: %d", method, ret)
	}
	if resp.ptr == nil || resp.len == 0 {
		return nil, nil
	}
	out := C.GoBytes(unsafe.Pointer(resp.ptr), C.int(resp.len))
	if hostFreeFn != nil {
		C.call_host_free(hostFreeFn, resp.ptr, resp.len)
	}
	return out, nil
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, plugin.ErrEnvelope("invalid_method", "method is required"))
		return 1
	}
	var req []byte
	if request != nil && requestLen > 0 {
		if requestLen > maxRequestLen {
			if response != nil {
				writeResponse(response, plugin.ErrEnvelope("plugin_error", "request exceeds maximum size"))
			}
			return 0
		}
		req = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := dispatcher.HandleCall(C.GoString(method), req)
	if err != nil {
		writeResponse(response, plugin.ErrEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	_, _ = dispatcher.HandleCall(plugin.MethodPluginShutdown, nil)
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
