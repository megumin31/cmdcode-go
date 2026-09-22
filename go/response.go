package main

// Event folding and OpenAI response/chunk formatting. Parsing and terminal
// state ownership live in EventDecoder, not in individual output paths.
import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"
)

type wireEvent struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Text         string          `json:"text"`
	Delta        string          `json:"delta"`
	ToolName     string          `json:"toolName"`
	ToolCallID   string          `json:"toolCallId"`
	Input        json.RawMessage `json:"input"`
	Args         json.RawMessage `json:"args"`
	FinishReason string          `json:"finishReason"`
	TotalUsage   *wireUsage      `json:"totalUsage"`
	Err          any             `json:"error"`
}

type wireUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
	Details      struct {
		CacheReadTokens  int `json:"cacheReadTokens"`
		CacheWriteTokens int `json:"cacheWriteTokens"`
	} `json:"inputTokenDetails"`
}

type accumulated struct {
	discard     bool
	seenContent bool
	text        strings.Builder
	reasoning   strings.Builder
	toolCalls   []accumulatedTool
	finish      string
	usage       wireUsage
	hasUsage    bool
}

type accumulatedTool struct {
	id    string
	name  string
	input json.RawMessage
}

// usageTurns counts terminal turn outcomes for local accounting audits. The
// upstream gateway only sends totalUsage on some finish events, so the
// with/without split measures how often downstream usage is real versus the
// zero fallback shared with the official CLI harness (which defaults usage
// to zeros and never reports null). truncated counts turns abandoned without
// any finish event. Explicit error/abort turns are not counted: they already
// carry their own failure signal. Summaries go to stderr (host journal)
// every usageStatEvery turns; per-turn logging would drown bursts.
const usageStatEvery = 100

var usageTurns struct {
	with      atomic.Uint64
	without   atomic.Uint64
	truncated atomic.Uint64
}

func noteTerminal(hasUsage bool) {
	if hasUsage {
		usageTurns.with.Add(1)
	} else {
		usageTurns.without.Add(1)
	}
	maybeLogUsageTurns()
}

func noteTruncated() {
	usageTurns.truncated.Add(1)
	maybeLogUsageTurns()
}

func maybeLogUsageTurns() {
	with := usageTurns.with.Load()
	without := usageTurns.without.Load()
	truncated := usageTurns.truncated.Load()
	if total := with + without + truncated; total > 0 && total%usageStatEvery == 0 {
		log.Printf("cmdcode-go: usage turns=%d with_totalUsage=%d zero_fallback=%d truncated=%d",
			total, with, without, truncated)
	}
}

// ---------------------------------------------------------------------------
// Request translation
// eventText reads incremental text from either wire field. The bundle shows
// .text; .delta stays as a fallback for gateway drift.
func eventText(ev wireEvent) string {
	if ev.Text != "" {
		return ev.Text
	}
	return ev.Delta
}

// eventSink collects incremental SSE frames; a nil sink only accumulates.
type eventSink struct {
	framer *streamFramer
	tools  *int
	frames [][]byte
}

// applyEvent folds one wire event into the accumulator, optionally emitting
// incremental frames. It returns finished on the terminal finish event and
// fatal on turn-level failure. Unknown types are ignored forward-compatibly.
func applyEvent(acc *accumulated, sink *eventSink, ev wireEvent, line []byte) (finished bool, fatal error) {
	emit := func(delta map[string]any) {
		if sink == nil || sink.framer == nil {
			return
		}
		sink.frames = append(sink.frames, sink.framer.frame(sink.framer.withRole(delta), nil))
	}
	switch ev.Type {
	case "text-delta":
		if text := eventText(ev); text != "" {
			acc.seenContent = true
			if !acc.discard {
				acc.text.WriteString(text)
			}
			emit(map[string]any{"content": text})
		}
	case "reasoning-start", "reasoning-end":
	case "reasoning-delta":
		if text := eventText(ev); text != "" {
			acc.seenContent = true
			if !acc.discard {
				acc.reasoning.WriteString(text)
			}
			emit(map[string]any{"reasoning_content": text})
		}
	case "tool-call":
		input := ev.Input
		if len(input) == 0 {
			input = ev.Args
		}
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		id := ev.ToolCallID
		if id == "" {
			id = newID()
		}
		input = compactJSON(input)
		acc.seenContent = true
		if !acc.discard {
			acc.toolCalls = append(acc.toolCalls, accumulatedTool{id: id, name: ev.ToolName, input: input})
		}
		if sink != nil && sink.tools != nil {
			emit(toolDelta(*sink.tools, id, ev.ToolName, string(input)))
			*sink.tools++
		}
	case "finish":
		acc.finish = mapFinishReason(ev.FinishReason)
		if ev.TotalUsage != nil {
			acc.usage = *ev.TotalUsage
			acc.hasUsage = true
		}
		noteTerminal(ev.TotalUsage != nil)
		return true, nil
	case "error":
		return false, upstreamEventError(ev.Err, line)
	case "abort":
		return false, fmt.Errorf("cmdcode-go: upstream aborted the turn")
	default:
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// NDJSON -> OpenAI
// ---------------------------------------------------------------------------

// upstreamStatusError carries a gateway-reported failure with its own HTTP
// status so executors can surface 429/503 (retryable) instead of a generic
// 500. The gateway sends these as NDJSON error events, e.g.
// {"isRetryable":true,"message":"...","statusCode":503,"type":"server_error"}.
type upstreamStatusError struct {
	status  int
	message string
}

func (e *upstreamStatusError) Error() string { return e.message }

func upstreamEventError(errVal any, line []byte) error {
	msg := "cmdcode-go: upstream error: " + wireErrorText(errVal, line)
	status := 0
	if m, ok := errVal.(map[string]any); ok {
		if code, ok := m["statusCode"].(float64); ok && code >= 400 && code < 600 {
			status = int(code)
		}
	}
	if status == 0 {
		return errors.New(msg)
	}
	return &upstreamStatusError{status: status, message: msg}
}

func compactJSON(raw json.RawMessage) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return json.RawMessage(`{}`)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}

func mapFinishReason(reason string) string {
	switch reason {
	case "tool-calls":
		return "tool_calls"
	case "length":
		return "length"
	case "stop", "":
		return "stop"
	default:
		return "stop"
	}
}

func wireErrorText(v any, fallback []byte) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	if v != nil {
		if raw, err := json.Marshal(v); err == nil && string(raw) != "null" {
			return string(raw)
		}
	}
	sample := bytes.TrimSpace(fallback)
	if len(sample) > maxErrorBodySample {
		sample = sample[:maxErrorBodySample]
	}
	return string(sample)
}

func (a *accumulated) promptTokens() int {
	if !a.hasUsage {
		return 0
	}
	return a.usage.InputTokens
}

func (a *accumulated) completionTokens() int {
	if !a.hasUsage {
		return 0
	}
	return a.usage.OutputTokens
}

func (a *accumulated) cachedTokens() int {
	if !a.hasUsage {
		return 0
	}
	return a.usage.Details.CacheReadTokens
}

func (a *accumulated) openAIToolCalls() []any {
	out := []any{}
	for _, call := range a.toolCalls {
		id := call.id
		if id == "" {
			id = newID()
		}
		args := string(call.input)
		if args == "" {
			args = "{}"
		}
		out = append(out, map[string]any{
			"id": id, "type": "function",
			"function": map[string]any{"name": call.name, "arguments": args},
		})
	}
	return out
}

// toOpenAIResponse renders a full chat.completion object. The model field
// echoes the client-requested model so host accounting stays consistent.
func (a *accumulated) toOpenAIResponse(model string) []byte {
	message := map[string]any{"role": "assistant", "content": a.text.String()}
	if r := a.reasoning.String(); r != "" {
		message["reasoning_content"] = r
	}
	if calls := a.openAIToolCalls(); len(calls) > 0 {
		message["tool_calls"] = calls
	}
	usage := a.openAIUsage()
	if cached := a.cachedTokens(); cached > 0 {
		usage["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	resp := map[string]any{
		"id": newChatID(), "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{
			"index": 0, "message": message, "finish_reason": a.finish,
		}},
		"usage": usage,
	}
	raw, _ := json.Marshal(resp)
	return raw
}

// streamFramer builds OpenAI SSE frames sharing one completion id. Reasoning
// travels in the OpenRouter-style reasoning_content delta so OpenAI-strict
// clients that ignore unknown fields keep working.
type streamFramer struct {
	model    string
	id       string
	created  int64
	sentRole bool
}

func newFramer(model string) *streamFramer {
	return &streamFramer{model: model, id: newChatID(), created: time.Now().Unix()}
}

// withRole tags the first emitted delta with the assistant role.
func (f *streamFramer) withRole(delta map[string]any) map[string]any {
	if !f.sentRole {
		f.sentRole = true
		delta["role"] = "assistant"
	}
	return delta
}

func (f *streamFramer) frame(delta map[string]any, finish *string) []byte {
	choice := map[string]any{"index": 0, "delta": delta}
	if finish != nil {
		choice["finish_reason"] = *finish
	} else {
		choice["finish_reason"] = nil
	}
	// Raw JSON only: the host adds `data:` framing and the terminal
	// `data: [DONE]` itself. Pre-framed payloads would double up.
	raw, _ := json.Marshal(map[string]any{
		"id": f.id, "object": "chat.completion.chunk",
		"created": f.created, "model": f.model, "choices": []any{choice},
	})
	return raw
}

func (f *streamFramer) finalFrame(finish string, usage map[string]any) []byte {
	if usage == nil {
		return f.frame(map[string]any{}, &finish)
	}
	raw, _ := json.Marshal(map[string]any{
		"id": f.id, "object": "chat.completion.chunk",
		"created": f.created, "model": f.model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}},
		"usage":   usage,
	})
	return raw
}

// toOpenAIChunks renders buffered SSE chunks.
func (a *accumulated) toOpenAIChunks(model string) [][]byte {
	f := newFramer(model)
	chunks := [][]byte{}
	if r := a.reasoning.String(); r != "" {
		chunks = append(chunks, f.frame(f.withRole(map[string]any{"reasoning_content": r}), nil))
	}
	if t := a.text.String(); t != "" {
		chunks = append(chunks, f.frame(f.withRole(map[string]any{"content": t}), nil))
	}
	for i, call := range a.toolCalls {
		chunks = append(chunks, f.frame(f.withRole(toolDelta(i, call.id, call.name, string(call.input))), nil))
	}
	if !f.sentRole {
		chunks = append(chunks, f.frame(f.withRole(map[string]any{}), nil))
	}
	// Usage is always present (zeros when the gateway omitted totalUsage),
	// matching the non-streaming response and the official CLI harness,
	// which defaults usage to zeros instead of reporting null.
	usage := a.openAIUsage()
	chunks = append(chunks, f.finalFrame(a.finish, usage))
	return chunks
}

func toolDelta(index int, id, name, args string) map[string]any {
	if id == "" {
		id = newID()
	}
	if args == "" {
		args = "{}"
	}
	return map[string]any{"tool_calls": []any{map[string]any{
		"index": index, "id": id, "type": "function",
		"function": map[string]any{"name": name, "arguments": args},
	}}}
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// newUUID returns an RFC 4122 v4 UUID. The gateway validates threadId and
// session-id as UUIDs and rejects plain hex with a 400.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012d", time.Now().UnixNano()%1e12)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	hexStr := hex.EncodeToString(b[:])
	return hexStr[:8] + "-" + hexStr[8:12] + "-" + hexStr[12:16] + "-" + hexStr[16:20] + "-" + hexStr[20:]
}

func newChatID() string {
	return "chatcmpl-" + newID()[:12]
}

// openAIUsage keeps cached-token accounting identical across all modes.
func (a *accumulated) openAIUsage() map[string]any {
	u := map[string]any{"prompt_tokens": a.promptTokens(), "completion_tokens": a.completionTokens(), "total_tokens": a.promptTokens() + a.completionTokens()}
	if cached := a.cachedTokens(); cached > 0 {
		u["prompt_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	return u
}
