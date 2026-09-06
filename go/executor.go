package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// streamIDRequest mirrors the host rpcExecutorRequest envelope just far
// enough to recover the stream id for async relay.
type streamIDRequest struct {
	pluginapi.ExecutorRequest
	StreamID string `json:"stream_id"`
}

func decodeExecutorRequest(request []byte) (pluginapi.ExecutorRequest, string, error) {
	var req pluginapi.ExecutorRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return req, "", fmt.Errorf("cmdcode-go: invalid executor request: %w", err)
	}
	var id streamIDRequest
	if err := json.Unmarshal(request, &id); err == nil {
		return req, id.StreamID, nil
	}
	return req, "", nil
}

// turnPrep bundles one validated upstream turn. errResp carries a ready-made
// error envelope for credential/model problems; err covers translation bugs.
type turnPrep struct {
	key      string
	envelope []byte
}

func prepareTurn(req pluginapi.ExecutorRequest) (turnPrep, []byte, error) {
	key := resolveAPIKey(req.AuthAttributes)
	if key == "" {
		return turnPrep{}, errorEnvelope("missing_api_key",
			"cmdcode-go: no API key (plugin api_key, COMMANDCODE_KEY env, or ~/.commandcode/auth.json)"), nil
	}
	upstream := resolveUpstreamModel(req.Model)
	if upstream == "" {
		return turnPrep{}, errorEnvelope("missing_model", "cmdcode-go: request model is empty"), nil
	}
	envelope, err := buildEnvelope(upstream, req.Payload)
	if err != nil {
		return turnPrep{}, nil, err
	}
	return turnPrep{key: key, envelope: envelope}, nil, nil
}

// upstreamErrorEnvelope maps gateway failures to host-native errors. The
// HTTPStatus rides into the host scheduler so 429s cool down and 401s do not
// retry instead of everything surfacing as a generic 500.
func upstreamErrorEnvelope(status int, body []byte) []byte {
	code := "upstream_error"
	switch {
	case status == 400:
		code = "bad_request"
	case status == 401:
		code = "unauthorized"
	case status == 402:
		code = "payment_required"
	case status == 403:
		code = "forbidden"
	case status == 404:
		code = "not_found"
	case status == 422:
		code = "unprocessable"
	case status == 429:
		code = "rate_limited"
	case status >= 500:
		code = "upstream_unavailable"
	}
	raw, _ := json.Marshal(pluginabi.Envelope{OK: false, Error: &pluginabi.Error{
		Code: "cmdcode-go:" + code,
		Message: fmt.Sprintf("cmdcode-go: upstream HTTP %d: %s",
			status, sampleBody(body)),
		HTTPStatus: status,
	}})
	return raw
}

// executeNonStream runs one upstream turn and returns a full OpenAI
// chat.completion payload. The gateway only speaks streaming NDJSON, so the
// stream is consumed server-side and buffered.
func executeNonStream(ctx context.Context, request []byte) ([]byte, error) {
	req, _, err := decodeExecutorRequest(request)
	if err != nil {
		return nil, err
	}
	prep, errResp, err := prepareTurn(req)
	if errResp != nil || err != nil {
		return errResp, err
	}
	status, body, err := postGenerate(ctx, prep.envelope, prep.key)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return upstreamErrorEnvelope(status, body), nil
	}
	acc, err := parseStream(body)
	if err != nil {
		var statusErr *upstreamStatusError
		if errors.As(err, &statusErr) {
			return upstreamErrorEnvelope(statusErr.status, []byte(statusErr.message)), nil
		}
		return nil, err
	}
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload: acc.toOpenAIResponse(req.Model),
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	})
}

// executeStream relays one upstream turn incrementally through host stream
// callbacks and returns immediately. Hosts that predate stream ids get the
// buffered fallback instead.
func executeStream(ctx context.Context, request []byte) ([]byte, error) {
	req, streamID, err := decodeExecutorRequest(request)
	if err != nil {
		return nil, err
	}
	prep, errResp, err := prepareTurn(req)
	if errResp != nil || err != nil {
		return errResp, err
	}
	if streamID == "" {
		return executeStreamBuffered(ctx, prep.key, req, prep.envelope)
	}
	resp, cancel, err := openGenerateStream(ctx, prep.envelope, prep.key)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySample))
		resp.Body.Close()
		cancel()
		return upstreamErrorEnvelope(resp.StatusCode, body), nil
	}
	model := req.Model
	go relayStream(streamID, model, resp.Body, cancel)
	return okEnvelope(map[string]any{
		"headers": map[string][]string{"content-type": {"text/event-stream"}},
		"chunks":  []pluginapi.ExecutorStreamChunk{},
	})
}

// executeStreamBuffered is the fallback for hosts without stream ids: the
// full turn is consumed first and returned as complete SSE frames.
func executeStreamBuffered(ctx context.Context, key string, req pluginapi.ExecutorRequest, envelope []byte) ([]byte, error) {
	status, body, err := postGenerate(ctx, envelope, key)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return upstreamErrorEnvelope(status, body), nil
	}
	acc, err := parseStream(body)
	if err != nil {
		var statusErr *upstreamStatusError
		if errors.As(err, &statusErr) {
			return upstreamErrorEnvelope(statusErr.status, []byte(statusErr.message)), nil
		}
		return nil, err
	}
	frames := acc.toOpenAIChunks(req.Model)
	chunks := make([]pluginapi.ExecutorStreamChunk, 0, len(frames))
	for _, frame := range frames {
		chunks = append(chunks, pluginapi.ExecutorStreamChunk{Payload: frame})
	}
	return okEnvelope(map[string]any{
		"headers": map[string][]string{"content-type": {"text/event-stream"}},
		"chunks":  chunks,
	})
}

// countTokens answers host token-count probes with a cheap heuristic. The
// gateway exposes no counting endpoint; callers only use this for budgeting.
func countTokens(request []byte) ([]byte, error) {
	req, _, err := decodeExecutorRequest(request)
	if err != nil {
		return nil, err
	}
	estimate := len(req.Payload)/4 + len(req.OriginalRequest)/4 + 8
	if estimate < 0 {
		estimate = 0
	}
	return okEnvelope(map[string]any{"total_tokens": estimate})
}

func sampleBody(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > maxErrorBodySample {
		trimmed = trimmed[:maxErrorBodySample]
	}
	if len(trimmed) == 0 {
		return "(empty body)"
	}
	return string(trimmed)
}
