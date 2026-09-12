package main

import (
	"context"
	"io"
)

// relayStream owns an already-open body. Its parent task remains registered
// until every host callback has returned, including the terminal close.
func (p *Plugin) relayStream(ctx context.Context, streamID, model string, body io.ReadCloser, cancel context.CancelFunc) {
	defer cancel()
	defer body.Close()
	d := NewEventDecoder(model, true)
	d.MaxEventBytes, d.MaxResponseBytes = p.Gateway.MaxEventBytes, p.Gateway.MaxResponseBytes
	err := d.Consume(body, func(frame []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return p.host.emit(streamID, frame)
	})
	message := ""
	if err != nil {
		message = err.Error()
	}
	_ = p.host.close(streamID, message)
}
