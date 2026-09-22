package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type GatewayClient struct {
	client           *http.Client
	owned            *http.Transport
	Timeout          time.Duration
	MaxEventBytes    int
	MaxResponseBytes int64
}

func NewGatewayClient(client *http.Client) *GatewayClient {
	g := &GatewayClient{client: client, Timeout: upstreamTimeout, MaxEventBytes: 8 << 20, MaxResponseBytes: 64 << 20}
	if client == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
		t.ResponseHeaderTimeout = 60 * time.Second
		g.owned = t
		g.client = &http.Client{Transport: t}
	}
	return g
}
func (g *GatewayClient) Close() {
	if g.owned != nil {
		g.owned.CloseIdleConnections()
	}
}

func (g *GatewayClient) Open(ctx context.Context, cfg pluginConfig, envelope []byte, key string) (*http.Response, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(ctx, g.Timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.resolveBaseURL()+generateRoute, bytes.NewReader(envelope))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "cli")
	req.Header.Set("x-cli-environment", "production")
	req.Header.Set("x-command-code-version", cfg.cliVersion())
	req.Header.Set("x-project-slug", cfg.projectSlug())
	req.Header.Set("x-taste-learning", "false")
	req.Header.Set("x-session-id", newUUID())
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := g.client.Do(req)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("cmdcode-go: upstream request: %w", err)
	}
	return resp, cancel, nil
}

func readBounded(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response exceeds %d byte limit", limit)
	}
	return b, nil
}
