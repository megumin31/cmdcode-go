package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// EventDecoder is the single NDJSON state machine for both executor modes.
// Streaming retains counters and usage only, not a second copy of the answer.
type EventDecoder struct {
	acc              accumulated
	framer           *streamFramer
	tools            int
	done             bool
	fatal            error
	MaxEventBytes    int
	MaxResponseBytes int64
	consumed         int64
}

func NewEventDecoder(model string, streaming bool) *EventDecoder {
	d := &EventDecoder{MaxEventBytes: 8 << 20, MaxResponseBytes: 64 << 20}
	d.acc.discard = streaming
	if streaming {
		d.framer = newFramer(model)
	}
	return d
}

func (d *EventDecoder) push(line []byte) ([][]byte, bool, error) {
	if d.fatal != nil {
		return nil, false, d.fatal
	}
	if d.done {
		return nil, true, nil
	}
	d.consumed += int64(len(line)) + 1
	if len(line) > d.MaxEventBytes || d.consumed > d.MaxResponseBytes {
		d.fatal = fmt.Errorf("cmdcode-go: upstream event/response size limit exceeded")
		return nil, false, d.fatal
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, false, nil
	}
	var ev wireEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		d.fatal = fmt.Errorf("cmdcode-go: invalid NDJSON event: %w", err)
		return nil, false, d.fatal
	}
	if ev.Type == "" {
		d.fatal = fmt.Errorf("cmdcode-go: NDJSON event missing type")
		return nil, false, d.fatal
	}
	var sink *eventSink
	if d.framer != nil {
		sink = &eventSink{framer: d.framer, tools: &d.tools}
	}
	done, err := applyEvent(&d.acc, sink, ev, line)
	if err != nil {
		d.fatal = err
		return nil, false, err
	}
	var frames [][]byte
	if sink != nil {
		frames = sink.frames
	}
	if done {
		d.done = true
		if d.framer != nil {
			if !d.framer.sentRole {
				frames = append(frames, d.framer.frame(d.framer.withRole(map[string]any{}), nil))
			}
			frames = append(frames, d.framer.finalFrame(d.acc.finish, d.acc.openAIUsage()))
		}
	}
	return frames, done, nil
}

func (d *EventDecoder) finish() ([][]byte, error) {
	if d.fatal != nil {
		return nil, d.fatal
	}
	if d.done {
		return nil, nil
	}
	noteTruncated()
	d.fatal = fmt.Errorf("cmdcode-go: upstream truncated the turn (no finish event)")
	return nil, d.fatal
}

func (d *EventDecoder) Consume(r io.Reader, emit func([]byte) error) error {
	if d.MaxEventBytes <= 0 || d.MaxResponseBytes <= 0 {
		return fmt.Errorf("invalid decoder limits")
	}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, min(64<<10, d.MaxEventBytes)), d.MaxEventBytes+2)
	for scanner.Scan() {
		frames, done, err := d.push(scanner.Bytes())
		if err != nil {
			return err
		}
		for _, f := range frames {
			if emit != nil {
				if err := emit(f); err != nil {
					return err
				}
			}
		}
		if done {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("cmdcode-go: reading NDJSON: %w", err)
	}
	_, err := d.finish()
	return err
}
