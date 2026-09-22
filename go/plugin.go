package main

import (
	"context"
	"fmt"
	"net/http"
	"sync"
)

type hostCallbacks struct {
	emit  func(string, []byte) error
	close func(string, string) error
}

// Plugin is the composition root and the sole owner of task lifetime.
// The Add/Wait boundary is protected by mu: shutdown cannot race new work.
type Plugin struct {
	Config   *ConfigStore
	Models   *ModelRegistry
	Gateway  *GatewayClient
	host     hostCallbacks
	stateMu  sync.Mutex
	mu       sync.Mutex
	root     context.Context
	cancel   context.CancelFunc
	stopping bool
	tasks    sync.WaitGroup
}

func NewPlugin(client *http.Client, host *hostCallbacks) *Plugin {
	ctx, cancel := context.WithCancel(context.Background())
	callbacks := hostCallbacks{emit: emitFrame, close: closeHostStream}
	if host != nil {
		callbacks = *host
	}
	return &Plugin{
		Config: &ConfigStore{}, Models: NewModelRegistry(modelTable, client),
		Gateway: NewGatewayClient(client), host: callbacks, root: ctx, cancel: cancel,
	}
}

var defaultPlugin = NewPlugin(nil, nil)

func (p *Plugin) begin(parent context.Context) (context.Context, func(), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return nil, nil, fmt.Errorf("cmdcode-go: plugin is shutting down")
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(p.root, cancel)
	p.tasks.Add(1)
	var once sync.Once
	return ctx, func() { once.Do(func() { stop(); cancel(); p.tasks.Done() }) }, nil
}

func (p *Plugin) Shutdown() {
	p.mu.Lock()
	p.stopping = true
	p.cancel()
	p.mu.Unlock()
	p.tasks.Wait()
	p.Gateway.Close()
}

func (p *Plugin) Configure(request []byte) error {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	cfg, err := p.Config.Update(request)
	if err != nil {
		return err
	}
	p.Models.Configure(cfg)
	return nil
}

type turnSnapshot struct {
	config pluginConfig
	models *modelSnapshot
}

func (p *Plugin) snapshot() turnSnapshot {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()
	return turnSnapshot{p.Config.Snapshot(), p.Models.Snapshot()}
}
