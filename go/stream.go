package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// liveStream converts upstream NDJSON events to SSE frames incrementally
// while accumulating the same state the buffered path reports.
type liveStream struct {
	framer *streamFramer
	acc    accumulated
	tools  int
	done   bool
}

func newLiveStream(model string) *liveStream {
	return &liveStream{framer: newFramer(model)}
}

// push feeds one raw NDJSON line. Frames must be emitted in order; done
// reports the terminal finish event; fatal reports a turn-level failure.
func (s *liveStream) push(line []byte) (frames [][]byte, done bool, fatal error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, false, nil
	}
	var ev wireEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil, false, nil // keep-alive blanks / partial flushes are skippable
	}
	sink := &eventSink{framer: s.framer, tools: &s.tools}
	done, fatal = applyEvent(&s.acc, sink, ev, line)
	if done {
		s.done = true
		return append(sink.frames, s.terminalTail()...), true, nil
	}
	return sink.frames, false, fatal
}

// terminalTail renders the closing frame after a finish event. Usage is
// always present (zeros when the gateway omitted totalUsage), matching the
// non-streaming response and the official CLI harness default.
func (s *liveStream) terminalTail() [][]byte {
	usage := map[string]any{
		"prompt_tokens": s.acc.promptTokens(), "completion_tokens": s.acc.completionTokens(),
		"total_tokens": s.acc.promptTokens() + s.acc.completionTokens(),
	}
	return [][]byte{s.framer.finalFrame(s.acc.finish, usage)}
}

// finish closes a turn whose upstream body ended without a finish event.
// Like the official harness, a missing finish is a truncated turn (callers
// surface it as a failure so the host can retry), never a normal stop: even
// when partial content arrived, there is no authoritative usage or stop
// reason to close the turn with.
func (s *liveStream) finish() ([][]byte, error) {
	if s.done {
		return nil, nil
	}
	noteTruncated()
	if s.acc.text.Len() == 0 && len(s.acc.toolCalls) == 0 && s.acc.reasoning.Len() == 0 {
		return nil, fmt.Errorf("cmdcode-go: upstream returned no usable events")
	}
	return nil, fmt.Errorf("cmdcode-go: upstream truncated the turn (no finish event)")
}

// relayStream pumps one upstream turn into host stream callbacks. It owns the
// body lifetime and always terminates the host stream exactly once.
func relayStream(streamID, model string, body io.ReadCloser, cancel context.CancelFunc) {
	defer cancel()
	defer body.Close()
	ls := newLiveStream(model)
	reader := bufio.NewReader(body) // ReadBytes has no line-length cap; NDJSON tool lines can be huge
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			frames, done, fatal := ls.push(line)
			if fatal != nil {
				_ = closeHostStream(streamID, fatal.Error())
				return
			}
			if errEmit := emitAll(streamID, frames); errEmit != nil {
				_ = closeHostStream(streamID, errEmit.Error())
				return
			}
			if done {
				_ = closeHostStream(streamID, "")
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				_ = closeHostStream(streamID, fmt.Sprintf("cmdcode-go: reading upstream: %v", err))
				return
			}
			frames, errFinish := ls.finish()
			if errFinish != nil {
				_ = closeHostStream(streamID, errFinish.Error())
				return
			}
			if errEmit := emitAll(streamID, frames); errEmit != nil {
				_ = closeHostStream(streamID, errEmit.Error())
				return
			}
			_ = closeHostStream(streamID, "")
			return
		}
	}
}

// emitAll relays frames in order, stopping at the first emit failure.
func emitAll(streamID string, frames [][]byte) error {
	for _, frame := range frames {
		if err := emitFrame(streamID, frame); err != nil {
			return err
		}
	}
	return nil
}
