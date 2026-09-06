package main

// OpenAI chat-completions <-> CommandCode /alpha/generate translation.
//
// Wire facts reverse-engineered from the command-code 1.47.1 bundle:
//   - POST {base}/alpha/generate, Bearer user_ key, CLI fingerprint headers
//   - body {config, memory:null, taste:null, skills:null, permissionMode,
//     threadId, mode, params{model, system, messages, tools, max_tokens,
//     stream:true, temperature?, reasoning_effort?}}
//   - messages are Vercel AI SDK ModelMessage, NOT OpenAI/Anthropic shapes
//   - response is raw NDJSON (not SSE): text-delta / reasoning-start-delta-end
//     / tool-call / finish / error / abort

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

const (
	generateRoute      = "/alpha/generate"
	defaultMaxTokens   = 64000
	upstreamTimeout    = 10 * time.Minute
	maxErrorBodySample = 2048
)

// ---------------------------------------------------------------------------
// OpenAI input
// ---------------------------------------------------------------------------

type chatRequest struct {
	Model               string        `json:"model"`
	Messages            []chatMessage `json:"messages"`
	Tools               []chatTool    `json:"tools"`
	ToolChoice          any           `json:"tool_choice"`
	Temperature         *float64      `json:"temperature"`
	MaxTokens           *int          `json:"max_tokens"`
	MaxCompletionTokens *int          `json:"max_completion_tokens"`
	ReasoningEffort     *string       `json:"reasoning_effort"`
}
type chatMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls"`
	ToolCallID string         `json:"tool_call_id"`
	Name       string         `json:"name"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ---------------------------------------------------------------------------
// Wire envelope
// ---------------------------------------------------------------------------

type wireConfig struct {
	WorkingDir    string   `json:"workingDir"`
	Date          string   `json:"date"`
	Environment   string   `json:"environment"`
	Structure     []string `json:"structure"`
	IsGitRepo     bool     `json:"isGitRepo"`
	CurrentBranch string   `json:"currentBranch"`
	MainBranch    string   `json:"mainBranch"`
	GitStatus     string   `json:"gitStatus"`
	RecentCommits []string `json:"recentCommits"`
}

type wireParams struct {
	Model           string   `json:"model"`
	System          string   `json:"system,omitempty"`
	Messages        []any    `json:"messages"`
	Tools           []any    `json:"tools,omitempty"`
	MaxTokens       int      `json:"max_tokens"`
	Stream          bool     `json:"stream"`
	Temperature     *float64 `json:"temperature,omitempty"`
	ReasoningEffort *string  `json:"reasoning_effort,omitempty"`
}

type generateRequest struct {
	Config         wireConfig `json:"config"`
	Memory         *string    `json:"memory"`
	Taste          any        `json:"taste"`
	Skills         any        `json:"skills"`
	PermissionMode string     `json:"permissionMode"`
	ThreadID       string     `json:"threadId"`
	Mode           string     `json:"mode"`
	Params         wireParams `json:"params"`
}

// ---------------------------------------------------------------------------
// Wire events
// ---------------------------------------------------------------------------

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
	text      strings.Builder
	reasoning strings.Builder
	toolCalls []accumulatedTool
	finish    string
	usage     wireUsage
	hasUsage  bool
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
// ---------------------------------------------------------------------------

func buildEnvelope(upstreamModel string, payload []byte) ([]byte, error) {
	var chat chatRequest
	if err := json.Unmarshal(payload, &chat); err != nil {
		return nil, fmt.Errorf("cmdcode-go: invalid openai chat payload: %w", err)
	}
	system, messages := toWireMessages(chat.Messages)
	tools := toWireTools(chat.Tools)
	if toolChoiceNone(chat.ToolChoice) {
		tools = []any{}
	}
	params := wireParams{
		Model:           upstreamModel,
		System:          system,
		Messages:        messages,
		Tools:           tools,
		MaxTokens:       maxTokens(chat, upstreamModel),
		Stream:          true, // the gateway rejects stream:false
		Temperature:     chat.Temperature,
		ReasoningEffort: normalizeReasoningEffort(chat.ReasoningEffort),
	}
	req := generateRequest{
		Config: wireConfig{
			WorkingDir:    ".",
			Date:          time.Now().Format("2006-01-02"),
			Environment:   runtime.GOOS,
			Structure:     []string{},
			CurrentBranch: "",
			MainBranch:    "",
			GitStatus:     "",
			RecentCommits: []string{},
		},
		PermissionMode: permissionMode(),
		ThreadID:       newUUID(),
		Mode:           "agent",
		Params:         params,
	}
	return json.Marshal(req)
}

func maxTokens(chat chatRequest, upstreamModel string) int {
	want := defaultMaxTokens
	if chat.MaxTokens != nil && *chat.MaxTokens > 0 {
		want = *chat.MaxTokens
	} else if chat.MaxCompletionTokens != nil && *chat.MaxCompletionTokens > 0 {
		want = *chat.MaxCompletionTokens
	}
	// Never promise more output tokens than the model can produce: oversized
	// budgets on small-output models risk gateway rejections mid-dialogue,
	// exactly when histories are longest.
	if cap := modelOutputCap(upstreamModel); cap > 0 && want > cap {
		want = cap
	}
	return want
}

// modelOutputCap reports the registered output limit for a canonical gateway
// id, or 0 when the model is unknown so callers keep the requested budget
// and let the gateway report the authoritative error.
func modelOutputCap(canonical string) int {
	for _, def := range modelTable {
		if def.id == canonical {
			return int(def.output)
		}
	}
	return 0
}

// toWireMessages converts OpenAI messages to Vercel ModelMessage parts.
// A single OpenAI user turn carrying tool results fans out to a tool-role
// message followed by a user-role message, mirroring the CLI mapping.
func toWireMessages(messages []chatMessage) (string, []any) {
	var system strings.Builder
	out := []any{}
	toolNames := map[string]string{}
	for _, msg := range messages {
		switch msg.Role {
		case "system", "developer":
			if text := messageText(msg.Content); text != "" {
				if system.Len() > 0 {
					system.WriteString("\n\n")
				}
				system.WriteString(text)
			}
		case "assistant":
			parts := []any{}
			if text := messageText(msg.Content); text != "" {
				parts = append(parts, map[string]any{"type": "text", "text": text})
			}
			for _, call := range msg.ToolCalls {
				input := parseToolInput(call.Function.Arguments)
				id := call.ID
				if id == "" {
					id = newID()
				}
				toolNames[id] = call.Function.Name
				parts = append(parts, map[string]any{
					"type": "tool-call", "toolCallId": id,
					"toolName": call.Function.Name, "input": input,
				})
			}
			out = append(out, map[string]any{"role": "assistant", "content": parts})
		case "tool":
			name := msg.Name
			if name == "" {
				name = toolNames[msg.ToolCallID]
			}
			if name == "" {
				name = "unknown"
			}
			out = append(out, map[string]any{"role": "tool", "content": []any{
				map[string]any{
					"type": "tool-result", "toolCallId": msg.ToolCallID,
					"toolName": name,
					"output":   map[string]any{"type": "text", "value": toolResultText(msg.Content)},
				},
			}})
		default: // user and anything else
			userParts := []any{}
			text, images := messageRichText(msg.Content)
			for _, t := range text {
				userParts = append(userParts, map[string]any{"type": "text", "text": t})
			}
			for _, img := range images {
				userParts = append(userParts, img)
			}
			if len(userParts) > 0 {
				out = append(out, map[string]any{"role": "user", "content": userParts})
			}
		}
	}
	if out == nil {
		out = []any{}
	}
	return system.String(), out
}

// messageText flattens string or part-array content to plain text.
func messageText(content any) string {
	texts, _ := messageRichText(content)
	return strings.Join(texts, "")
}

func messageRichText(content any) ([]string, []any) {
	var texts []string
	var images []any
	switch c := content.(type) {
	case nil:
	case string:
		if c != "" {
			texts = append(texts, c)
		}
	case []any:
		for _, item := range c {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			switch part["type"] {
			case "text":
				if s, _ := part["text"].(string); s != "" {
					texts = append(texts, s)
				}
			case "image_url":
				if img := toWireImage(part["image_url"]); img != nil {
					images = append(images, img)
				}
			case "input_text":
				if s, _ := part["text"].(string); s != "" {
					texts = append(texts, s)
				}
			}
		}
	}
	return texts, images
}

// toWireImage maps an OpenAI image_url to a ModelMessage image part. Data
// URLs pass through with their media type; remote URLs are forwarded as-is
// best-effort since the CLI bundle shows no client-side fetch.
func toWireImage(raw any) any {
	url := ""
	switch v := raw.(type) {
	case string:
		url = v
	case map[string]any:
		url, _ = v["url"].(string)
	}
	if url == "" {
		return nil
	}
	mime := "image/png"
	if strings.HasPrefix(url, "data:") {
		if semi := strings.Index(url, ";"); semi > 5 {
			mime = url[5:semi]
		}
	}
	return map[string]any{"type": "image", "image": url, "mimeType": mime}
}

func toolResultText(content any) string {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, item := range c {
			part, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if s, _ := part["text"].(string); s != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(s)
			}
		}
		return sb.String()
	default:
		raw, err := json.Marshal(content)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

func parseToolInput(args string) any {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		// Never fail a turn on unparseable arguments; the CLI coerces
		// unknown shapes server-side, so wrap the raw string instead.
		return map[string]any{"_raw": args}
	}
	return v
}

func toWireTools(tools []chatTool) []any {
	out := []any{}
	for _, tool := range tools {
		if tool.Type != "" && tool.Type != "function" {
			continue
		}
		if tool.Function.Name == "" {
			continue
		}
		schema := normalizeSchema(tool.Function.Parameters)
		out = append(out, map[string]any{
			"name": tool.Function.Name, "description": tool.Function.Description,
			"input_schema": schema,
		})
	}
	return out
}

// normalizeSchema coerces tool parameter schemas into the record the gateway
// demands: a JSON Schema with type "object". Missing, null, and non-object
// schemas (arrays, strings, numbers) become {"type":"object","properties":{}}
// (accept anything); double-encoded string schemas are unwrapped when they
// hold an object. Object schemas keep their fields and gain type:"object"
// when the type is absent: the gateway rejects typeless records outright
// ("schema must be a JSON Schema of 'type: \"object\"', got 'type: null'"),
// which previously broke every no-arg tool call whose client sent
// parameters:null. Anything unparseable also becomes
// {"type":"object","properties":{}} rather than failing the whole turn —
// mirroring the CLI's coerce-not-reject stance.
func normalizeSchema(raw json.RawMessage) json.RawMessage {
	empty := json.RawMessage(`{"type":"object","properties":{}}`)
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return empty
	}
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return empty
	}
	switch t := v.(type) {
	case map[string]any:
		if typ, ok := t["type"].(string); ok && strings.EqualFold(strings.TrimSpace(typ), "object") {
			return trimmed
		}
		if typ, ok := t["type"].(string); ok && strings.TrimSpace(typ) != "" {
			// Explicit non-object type: the gateway only accepts objects,
			// so fall back to accepting anything instead of 400ing the turn.
			return empty
		}
		t["type"] = "object"
		out, err := json.Marshal(t)
		if err != nil {
			return empty
		}
		return out
	case string:
		var inner any
		if err := json.Unmarshal([]byte(t), &inner); err != nil {
			return empty
		}
		if _, ok := inner.(map[string]any); ok {
			return normalizeSchema(json.RawMessage(t))
		}
		return empty
	default:
		return empty
	}
}

// toolChoiceNone reports an explicit "no tools" directive, either the string
// form or {"type": "none"}. Anything else (auto/required/specific) keeps the
// declared tools: the gateway has no forced-call mode, so "required" degrades
// to offering them like the official CLI does.
func toolChoiceNone(choice any) bool {
	switch c := choice.(type) {
	case nil:
		return false
	case string:
		return strings.EqualFold(strings.TrimSpace(c), "none")
	case map[string]any:
		if t, _ := c["type"].(string); strings.EqualFold(strings.TrimSpace(t), "none") {
			return true
		}
	}
	return false
}

// normalizeReasoningEffort clamps the effort to the gateway enum
// (low|medium|high|xhigh|max, per its 400 message). OpenAI-side values
// outside it degrade gracefully instead of failing the turn: "minimal"
// maps to the closest level, disable/default spellings are omitted so the
// gateway falls back to the model default — mirroring what the official CLI
// sends (it only ever emits supported levels).
func normalizeReasoningEffort(effort *string) *string {
	if effort == nil {
		return nil
	}
	switch normalized := strings.ToLower(strings.TrimSpace(*effort)); normalized {
	case "low", "medium", "high", "xhigh", "max":
		return &normalized
	case "minimal":
		low := "low"
		return &low
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Upstream transport
// ---------------------------------------------------------------------------

func generateHTTPRequest(ctx context.Context, envelope []byte, apiKey string) (*http.Request, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, upstreamTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolveBaseURL()+generateRoute, bytes.NewReader(envelope))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-command-code-version", cliVersion())
	req.Header.Set("x-project-slug", projectSlug())
	req.Header.Set("x-taste-learning", "false")
	req.Header.Set("x-session-id", newUUID())
	req.Header.Set("Authorization", "Bearer "+apiKey)
	return req, cancel, nil
}

func postGenerate(ctx context.Context, envelope []byte, apiKey string) (int, []byte, error) {
	req, cancel, err := generateHTTPRequest(ctx, envelope, apiKey)
	if err != nil {
		return 0, nil, err
	}
	defer cancel()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("cmdcode-go: upstream unreachable: %w", err)
	}
	defer resp.Body.Close()
	// Tool outputs can make single NDJSON lines huge; do not cap the reader.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("cmdcode-go: reading upstream body: %w", err)
	}
	return resp.StatusCode, body, nil
}

// openGenerateStream starts one upstream turn and hands the open body to the
// caller for incremental NDJSON consumption. The cancel func must be called

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
			acc.text.WriteString(text)
			emit(map[string]any{"content": text})
		}
	case "reasoning-start", "reasoning-end":
	case "reasoning-delta":
		if text := eventText(ev); text != "" {
			acc.reasoning.WriteString(text)
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
		acc.toolCalls = append(acc.toolCalls, accumulatedTool{id: id, name: ev.ToolName, input: input})
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

// once the body is fully consumed or abandoned.
func openGenerateStream(ctx context.Context, envelope []byte, apiKey string) (*http.Response, context.CancelFunc, error) {
	req, cancel, err := generateHTTPRequest(ctx, envelope, apiKey)
	if err != nil {
		return nil, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("cmdcode-go: upstream unreachable: %w", err)
	}
	return resp, cancel, nil
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

func parseStream(body []byte) (*accumulated, error) {
	acc := &accumulated{}
	for _, line := range bytes.Split(body, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var ev wireEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			continue // keep-alive blanks / partial flushes are skippable
		}
		if _, err := applyEvent(acc, nil, ev, line); err != nil {
			return nil, err
		}
	}
	if acc.finish == "" && !acc.hasUsage && acc.text.Len() == 0 && len(acc.toolCalls) == 0 {
		noteTruncated()
		return nil, fmt.Errorf("cmdcode-go: upstream returned no usable events (%d bytes)", len(bytes.TrimSpace(body)))
	}
	if acc.finish == "" {
		// The official harness treats a missing finish event as a truncated
		// turn (502 retryable), never a normal stop: without finish there is
		// no authoritative usage or stop reason to report.
		noteTruncated()
		return nil, fmt.Errorf("cmdcode-go: upstream truncated the turn (no finish event)")
	}
	return acc, nil
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
	if calls := a.openAIToolCalls(); len(calls) > 0 {
		message["tool_calls"] = calls
	}
	usage := map[string]any{
		"prompt_tokens": a.promptTokens(), "completion_tokens": a.completionTokens(),
		"total_tokens": a.promptTokens() + a.completionTokens(),
	}
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
	usage := map[string]any{
		"prompt_tokens": a.promptTokens(), "completion_tokens": a.completionTokens(),
		"total_tokens": a.promptTokens() + a.completionTokens(),
	}
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
