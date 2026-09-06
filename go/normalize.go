package main

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Response pre-normalization for translated streaming paths.
//
// The host passes plugin executor chunks straight through when the client
// protocol matches our output (OpenAI), where the HTTP layer adds `data:`
// framing itself — so chunks must stay raw JSON there. The OpenAI→Claude
// stream converter, however, silently drops payloads without a `data:`
// prefix. This hook bridges the two contracts: on streaming OpenAI→Claude
// (and only there) it adds the missing prefix. Every other path —
// non-streaming, other target protocols, already-framed payloads, the
// `[DONE]` tail — passes through untouched.
func normalizeBefore(request []byte) ([]byte, error) {
	var req pluginapi.ResponseTransformRequest
	if err := json.Unmarshal(request, &req); err != nil {
		return nil, err
	}
	body := req.Body
	if req.Stream && isOpenAIFormat(req.FromFormat) && isClaudeFormat(req.ToFormat) {
		trimmed := bytes.TrimSpace(body)
		if len(trimmed) > 0 && !bytes.HasPrefix(trimmed, []byte("data:")) {
			body = append([]byte("data: "), trimmed...)
		}
	}
	return okEnvelope(pluginapi.PayloadResponse{Body: body})
}

func isOpenAIFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "none":
		return false
	case "openai", "chat-completions", "chat_completions",
		"openai-chat-completions", "openai_chat_completions":
		return true
	default:
		return false
	}
}

func isClaudeFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "claude", "anthropic":
		return true
	default:
		return false
	}
}
