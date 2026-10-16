package main

import (
	"fmt"
	"sync"
)

// pluginRuntime owns the one active ABI instance. Go c-shared libraries may
// remain mapped after dlclose (NODELETE), so package initialization is not an
// instance boundary. Every successful init creates a fresh Plugin.
type pluginRuntime struct {
	lifecycle sync.Mutex
	mu        sync.RWMutex
	active    *Plugin
}

func (r *pluginRuntime) Initialize(installHost func()) bool {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		return false
	}
	if installHost != nil {
		installHost()
	}
	r.active = NewPlugin(nil, nil)
	return true
}

func (r *pluginRuntime) Handle(method string, request []byte) ([]byte, error) {
	r.mu.RLock()
	p := r.active
	r.mu.RUnlock()
	if p == nil {
		return nil, fmt.Errorf("cmdcode-go: plugin is not initialized")
	}
	// Do not hold an adapter lock across work: shutdown must be able to cancel
	// long-running requests. Plugin.begin serializes task admission with drain.
	return p.Handle(method, request)
}

func (r *pluginRuntime) Shutdown(clearHost func()) {
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	r.mu.RLock()
	p := r.active
	r.mu.RUnlock()
	if p == nil {
		return
	}
	p.Shutdown()
	r.mu.Lock()
	defer r.mu.Unlock()
	// No old callback can use the host pointer after Shutdown has drained.
	if clearHost != nil {
		clearHost()
	}
	r.active = nil
}
