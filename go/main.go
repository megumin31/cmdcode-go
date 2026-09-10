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
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
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
	raw := decodeRequest(request, requestLen)
	out, errHandle := handleMethod(C.GoString(method), raw)
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
func cliproxyPluginShutdown() {}

func decodeRequest(request *C.uint8_t, requestLen C.size_t) []byte {
	if request == nil || requestLen == 0 {
		return nil
	}
	return C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
}

// hostCall invokes a host callback and treats transport failure or an explicit
// error envelope as an error. Unparseable responses are accepted as success so
// host-side shape drift cannot wedge an otherwise healthy stream.
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
	if rc := C.call_host_api(cMethod, req, C.size_t(len(payload)), &response); rc != 0 {
		return fmt.Errorf("cmdcode-go: host call %s failed", method)
	}
	defer C.free_host_buffer(response.ptr, response.len)
	if response.ptr == nil || response.len == 0 {
		return nil
	}
	raw := C.GoBytes(response.ptr, C.int(response.len))
	var env pluginabi.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	if !env.OK && env.Error != nil {
		msg := env.Error.Message
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

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		configure(request)
		return okEnvelope(registration())
	case pluginabi.MethodModelRegister:
		maybeRefreshModels()
		return okEnvelope(pluginapi.ModelRegistrationResponse{Provider: ProviderKey, Models: registeredModels()})
	case pluginabi.MethodModelStatic, pluginabi.MethodModelForAuth:
		maybeRefreshModels()
		return okEnvelope(pluginapi.ModelResponse{Provider: ProviderKey, Models: registeredModels()})
	case pluginabi.MethodModelRoute:
		return okEnvelope(routeModel(request))
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": ProviderKey})
	case pluginabi.MethodExecutorExecute:
		return executeNonStream(context.Background(), request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(context.Background(), request)
	case pluginabi.MethodExecutorCountTokens:
		return countTokens(request)
	case pluginabi.MethodResponseNormalizeBefore:
		return normalizeBefore(request)
	case pluginabi.MethodExecutorHTTPRequest:
		return okEnvelope(pluginapi.ExecutorHTTPResponse{
			StatusCode: http.StatusNotImplemented,
			Headers:    http.Header{"Content-Type": []string{"application/json"}},
			Body:       []byte(`{"error":"cmdcode-go has no generic HTTP bridge"}`),
		})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func routeModel(request []byte) pluginapi.ModelRouteResponse {
	var req pluginapi.ModelRouteRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return pluginapi.ModelRouteResponse{Handled: false}
	}
	if canonical, ok := matchModel(req.RequestedModel); ok && modelAllowed(canonical) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Reason: "cmdcode-go model"}
	}
	return pluginapi.ModelRouteResponse{Handled: false}
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
