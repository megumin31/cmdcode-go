# Conversation identity

The plugin preserves the full translated message history, tool results, and
reasoning history. Session reuse changes only the upstream conversation UUID.
The same UUID is sent as `threadId` and `x-session-id`, including retries of an
already prepared request. It does not diagnose or suppress repetitive prose.

## Reliable identity required

Reuse requires all three of:

- Trusted CPA executor metadata `caller_scope` (downstream caller namespace).
- CPA `AuthID` (the selected upstream credential identifier, never an API key).
- An explicit session/conversation/thread identifier.

The explicit identifier can come from CPA's `execution_session_id` metadata,
headers `X-Session-Id`, `Session-Id`, `Session_id`, `X-Http-Session-Id`,
`X-Claude-Code-Session-Id`, `X-Conversation-Id`, `X-Thread-Id`, `Thread-Id`, or
body fields `session_id`, `sessionId`, `sessionID`, `thread_id`, `threadId`,
`conversation_id`, `conversationId`, `chat_id`, `chatId`. The snake-case session,
thread and conversation fields are also accepted under `metadata` and
`extra_body`. Header names are case-insensitive. `OriginalRequest` takes
precedence over the translated payload so Responses clients' original identity
can survive conversion into chat-completions. This does not require a specific
client brand. A client that sends no reliable identifier cannot be identified
from history with certainty.

Every recognized source must agree. IDs must be strings of 1–256 UTF-8 bytes,
without leading/trailing whitespace or control characters. Multi-value and
comma-joined headers, duplicate JSON object fields at inspected levels, invalid
values and conflicting evidence disable cross-request reuse. Caller scope and
AuthID must also pass the same bounded string validation. No client-supplied
body field can supply the trusted caller scope.

CPA's `canonical_session_id` alone is deliberately insufficient: the pinned SDK
can derive it from routing/history heuristics without source attribution.
`derived_session_id`, `lcp_affinity_session_id`, cache keys, request IDs, user IDs,
and parent IDs are never used as conversation identity. A validated explicit
source can still be used when unrelated inferred metadata is present. Missing
identity/isolation or ambiguous evidence falls back to a fresh per-request UUID;
no messages are dropped. No API key is used to derive an identity, and the
mapping is neither logged nor persisted.

## Lifecycle and concurrency

The in-memory mapping is local to one plugin instance, bounded to 4096 entries,
with a 24-hour idle TTL and least-recently-used eviction of idle entries. The TTL
starts again when a turn finishes. Restart, plugin reload, eviction, expiry,
caller/account changes, or routing to a different plugin instance may create a
new upstream UUID. There is no cross-instance or cross-restart continuity claim.

Lookup, allocation, eviction, and active-turn reservation are atomic under one
mutex. Active entries cannot expire or be evicted. A second simultaneous turn
for the same scoped conversation receives `session_busy` with HTTP 409; the
client may retry after the active turn ends. A cache with all 4096 entries active
returns the same bounded-capacity conflict. There is no unbounded queue and no
silent temporary branch. The pinned CPA v7.2.149 classifies HTTP 409 as a request fault, so it does
not rotate or penalize the selected credential for this conflict. Other host
versions and automatic client retry behavior are not guaranteed by this plugin. Streaming retains the reservation through terminal callbacks, and error,
cancellation, or completion releases it. Network retries of the same prepared
request preserve its UUID; a separate host invocation after a stateless fallback
has no trustworthy request/session identity and receives a new UUID.

Forks need distinct explicit conversation IDs from the caller. If a client uses
the same ID for two branches, the plugin cannot infer the branch from their
messages; sequential calls will use the same upstream conversation. To start a
new independent conversation, send a new explicit ID. Only authentication-
account isolation, not arbitrary account failover continuity, is promised.

## Offline verification

`go/session_test.go` covers explicit and inferred source handling, conflicts and
invalid values, real executor JSON decode fields, Responses-original/header
preservation, account/caller isolation, unchanged multi-turn tool/reasoning
payloads, header/body UUID equality, repeat-send identity, busy errors, atomic
concurrency, TTL, LRU capacity, and stream/error release. The normal offline
quality gate also runs the existing protocol suite, race detector, vet, C-shared
build and repeated C ABI load/unload checks. No authenticated inference request
is needed for these tests.
