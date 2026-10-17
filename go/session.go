package main

import (
	"bytes"
	"container/list"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const sessionTTL = 24 * time.Hour
const sessionCapacity = 4096

// No keys, payloads, or identities are logged or persisted. Host metadata is
// trusted only as part of ExecutorRequest; client body metadata is not a scope.
type sessionKey struct{ caller, auth, id string }
type sessionEntry struct {
	key     sessionKey
	id      string
	touched time.Time
	busy    bool
}
type sessionStore struct {
	mu       sync.Mutex
	entries  map[sessionKey]*list.Element
	lru      list.List
	now      func() time.Time
	ttl      time.Duration
	capacity int
}

func newSessionStore() *sessionStore {
	return &sessionStore{entries: make(map[sessionKey]*list.Element), now: time.Now, ttl: sessionTTL, capacity: sessionCapacity}
}

// acquire never queues simultaneous turns onto stateful upstream conversations.
// Active entries cannot expire or be evicted. Saturation fails closed instead
// of silently changing an existing conversation's upstream identity.
func (s *sessionStore) acquire(key sessionKey) (string, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for e := s.lru.Back(); e != nil; {
		prev := e.Prev()
		v := e.Value.(*sessionEntry)
		if !v.busy && now.Sub(v.touched) >= s.ttl {
			delete(s.entries, v.key)
			s.lru.Remove(e)
		}
		e = prev
	}
	e := s.entries[key]
	if e != nil && e.Value.(*sessionEntry).busy {
		return "", nil, false
	}
	if e == nil {
		if len(s.entries) >= s.capacity {
			var victim *list.Element
			for v := s.lru.Back(); v != nil; v = v.Prev() {
				if !v.Value.(*sessionEntry).busy {
					victim = v
					break
				}
			}
			if victim == nil {
				return "", nil, false
			}
			delete(s.entries, victim.Value.(*sessionEntry).key)
			s.lru.Remove(victim)
		}
		e = s.lru.PushFront(&sessionEntry{key: key, id: newUUID()})
		s.entries[key] = e
	}
	v := e.Value.(*sessionEntry)
	v.busy = true
	v.touched = now
	s.lru.MoveToFront(e)
	var once sync.Once
	release := func() {
		once.Do(func() { s.mu.Lock(); defer s.mu.Unlock(); v.busy = false; v.touched = s.now(); s.lru.MoveToFront(e) })
	}
	return v.id, release, true
}

func validSessionValue(v string) bool {
	if v == "" || len(v) > 256 || !utf8.ValidString(v) || strings.TrimSpace(v) != v {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// objectFields rejects duplicate keys rather than choosing first/last identity.
func objectFields(raw []byte) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, false
	}
	m := make(map[string]json.RawMessage)
	for d.More() {
		t, err = d.Token()
		if err != nil {
			return nil, false
		}
		k, ok := t.(string)
		if !ok {
			return nil, false
		}
		if _, exists := m[k]; exists {
			return nil, false
		}
		var v json.RawMessage
		if d.Decode(&v) != nil {
			return nil, false
		}
		m[k] = v
	}
	if _, err = d.Token(); err != nil {
		return nil, false
	}
	if _, err = d.Token(); err != io.EOF {
		return nil, false
	}
	return m, true
}

// explicitSessionKey accepts only conversation/session/thread identifiers, not
// cache keys, user IDs, parent IDs, request IDs, or CPA's inferred canonical/LCP
// identifiers. Disagreement or invalid evidence disables cross-request reuse.
func explicitSessionKey(req pluginapi.ExecutorRequest) (sessionKey, bool) {
	caller, ok := req.Metadata["caller_scope"].(string)
	if !ok || !validSessionValue(caller) || !validSessionValue(req.AuthID) {
		return sessionKey{}, false
	}
	var identity string
	add := func(v any) bool {
		str, ok := v.(string)
		if !ok || !validSessionValue(str) || (identity != "" && identity != str) {
			return false
		}
		identity = str
		return true
	}
	seenHeaders := make(map[string]bool)
	for name, values := range req.Headers {
		switch strings.ToLower(name) {
		case "x-session-id", "session-id", "session_id", "x-http-session-id", "x-claude-code-session-id", "x-conversation-id", "x-thread-id", "thread-id":
			normalized := strings.ToLower(name)
			if seenHeaders[normalized] || len(values) != 1 || strings.Contains(values[0], ",") || !add(values[0]) {
				return sessionKey{}, false
			}
			seenHeaders[normalized] = true
		}
	}
	if v, exists := req.Metadata["execution_session_id"]; exists && !add(v) {
		return sessionKey{}, false
	}
	raw := req.OriginalRequest
	if len(raw) == 0 {
		raw = req.Payload
	}
	if len(raw) > 0 {
		m, ok := objectFields(raw)
		if !ok {
			return sessionKey{}, false
		}
		read := func(m map[string]json.RawMessage, names []string) bool {
			for _, name := range names {
				if v, exists := m[name]; exists {
					var value any
					if json.Unmarshal(v, &value) != nil || !add(value) {
						return false
					}
				}
			}
			return true
		}
		if !read(m, []string{"session_id", "sessionId", "sessionID", "thread_id", "threadId", "conversation_id", "conversationId", "chat_id", "chatId"}) {
			return sessionKey{}, false
		}
		for _, container := range []string{"metadata", "extra_body"} {
			if r, exists := m[container]; exists && string(r) != "null" {
				nested, ok := objectFields(r)
				if !ok || !read(nested, []string{"session_id", "thread_id", "conversation_id"}) {
					return sessionKey{}, false
				}
			}
		}
	}
	if identity == "" {
		return sessionKey{}, false
	}
	return sessionKey{caller: caller, auth: req.AuthID, id: identity}, true
}
