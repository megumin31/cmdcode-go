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

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil || host == nil || host.abi_version != C.uint32_t(pluginabi.ABIVersion) {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	if requestLen > 64<<20 || (request == nil && requestLen != 0) {
		writeResponse(response, errorEnvelope("invalid_request", "invalid or oversized ABI request"))
		return 1
	}
	raw := decodeRequest(request, requestLen)
	out, errHandle := defaultPlugin.Handle(C.GoString(method), raw)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, out)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	defaultPlugin.Shutdown()
	C.store_host_api(nil)
}

func decodeRequest(request *C.uint8_t, requestLen C.size_t) []byte {
	if request == nil || requestLen == 0 {
		return nil
	}
	return C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
}

// hostCall invokes a host callback and treats transport failure or an explicit
// error envelope as an error. Malformed host responses are protocol failures,
// not acknowledgements that a frame was delivered.
func hostCall(method string, payload []byte) error {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var req *C.uint8_t
	if len(payload) > 0 {
		raw := C.CBytes(payload)
		defer C.free(raw)
		req = (*C.uint8_t)(raw)
	}
	var response C.cliproxy_buffer
	rc := C.call_host_api(cMethod, req, C.size_t(len(payload)), &response)
	defer C.free_host_buffer(response.ptr, response.len)
	if rc != 0 {
		return fmt.Errorf("cmdcode-go: host call %s failed", method)
	}
	if response.ptr == nil || response.len == 0 {
		return nil
	}
	if response.len > 4<<20 {
		return fmt.Errorf("cmdcode-go: oversized host callback response")
	}
	raw := C.GoBytes(response.ptr, C.int(response.len))
	var env pluginabi.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("cmdcode-go: invalid host callback response: %w", err)
	}
	if !env.OK {
		msg := ""
		if env.Error != nil {
			msg = env.Error.Message
		}
		if msg == "" {
			msg = "host call failed"
		}
		return fmt.Errorf("cmdcode-go: %s: %s", method, msg)
	}
	return nil
}

func emitFrame(streamID string, payload []byte) error {
	req, _ := json.Marshal(map[string]any{"stream_id": streamID, "payload": payload})
	return hostCall("host.stream.emit", req)
}

func closeHostStream(streamID, errMsg string) error {
	payload := map[string]any{"stream_id": streamID}
	if errMsg != "" {
		payload["error"] = errMsg
	}
	req, _ := json.Marshal(payload)
	return hostCall("host.stream.close", req)
}

func okEnvelope(v any) ([]byte, error) {
	return json.Marshal(pluginabi.Envelope{OK: true, Result: mustRaw(v)})
}

func mustRaw(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &pluginabi.Error{Code: code, Message: message}})
	return raw
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
