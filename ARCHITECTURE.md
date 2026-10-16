# Architecture

The plugin remains one Go package and one c-shared library. Five concrete
components define ownership; small translation helpers are ordinary functions,
not additional frameworks or services.

| Component | Code | Contract |
|---|---|---|
| Plugin | go/plugin.go, go/dispatch.go, go/executor.go | Composition root, request orchestration, lifecycle and coherent turn snapshots |
| ConfigStore | go/config.go | Typed YAML decoding, validation, atomic updates and defensive copies |
| ModelRegistry | go/models_remote.go, go/models.go | Immutable model snapshots/indexes and generation-safe, single-flight refresh |
| GatewayClient | go/gateway_client.go | Reusable HTTP transport, authentication, timeouts, response ownership |
| EventDecoder | go/event_decoder.go | One bounded NDJSON state machine shared by streaming and buffered responses |

go/main.go is only the C ABI adapter and host callback bridge;
go/abi_runtime.go owns its initialization/teardown boundary.
go/request.go converts incoming messages; go/response.go folds events and renders
OpenAI payloads; go/stream.go connects the decoder to host callbacks.

## Lifecycle and cancellation

Every dispatched RPC is registered before work starts. Starting work and marking
shutdown share a mutex so WaitGroup.Add cannot race shutdown's Wait.
A live stream gets its own registered task because it outlives its initiating RPC.
Shutdown rejects new calls, cancels the plugin root context, waits for all tasks
and host callbacks to return, then closes owned idle HTTP connections. Only then
does the ABI adapter clear its saved host pointer.

Initialization and teardown are serialized. A second initialization while an
instance is active is rejected without changing its host pointer. Every
initialization after shutdown creates a new Plugin, config, registry and root
context. This is necessary even after dlclose: Go's Linux c-shared libraries can
remain mapped with NODELETE. Concurrent calls either join the active instance
or fail after it begins stopping; they never revive the old instance.

Client cancellation is not magically propagated across the C ABI: the current
host interface exposes no direct cancel notification to this executor. A failed
host emit stops the stream; shutdown always cancels it. If a client disconnects
while the upstream is silent, the request can remain until the next failed emit
or the 10-minute upstream deadline. Do not claim immediate per-client cancellation
without adding a supported host cancellation mechanism.

A host callback must eventually return when its host scope closes. Shutdown must
wait rather than abandon a callback and allow the host to free memory it still uses.

## Configuration and snapshots

Configuration uses yaml.v3, accepts block/flow model lists and the legacy
comma-separated scalar list. Host-owned keys such as enabled/priority are allowed.
Invalid YAML, duplicate keys, wrong types, invalid HTTP(S) URLs and invalid
refresh durations reject the whole update. Missing keys retain their values;
null or empty lists clear them.

Plugin serializes config updates with registry configuration changes. Request
preparation captures one config and model snapshot; model matching, filtering,
budget selection and outgoing headers use that snapshot even if a reconfiguration
or model download finishes later.

## Model refresh and metadata

ModelRegistry keeps the compiled fallback and last good snapshot. Registration
may synchronously initiate one refresh (8-second timeout); simultaneous callers
return their existing snapshot rather than queueing duplicate downloads.
Failed attempts, including the first ever attempt, cool down for 5 minutes.
A source change cancels the old request and advances a generation counter.
Old results cannot publish into a newer configuration. Disabling refresh
selects the compiled fallback. Remote/local input is limited to 4 MiB.

Metadata from the generator survives both compiled and downloaded paths,
including vision, effort levels and output source. Snapshot indexes use exact
case-insensitive IDs and unique short names; malformed or colliding rosters fail
validation. Schema 1 remains readable; documented rosters may retire the legacy
anchor, while legacy payloads retain the old anchor guard.

Budget semantics are intentionally separate:
- output is a reference/default budget. When output_source is fallback it is not
  advertised as a known output limit to the host.
- An omitted client budget uses min(64000, the available reference/default budget).
- An explicit client budget is not silently clamped by models.dev or fallback data.
- Only a positive gateway_output_limit in a trusted model file is treated as a
  confirmed hard cap. The current package-derived catalog does not invent one.
- The compiled catalog carries the same vision/effort/reference information as JSON.

The CLI fingerprint still has a separate configured/default version; updating a
model list does not prove that a new wire protocol fingerprint is compatible.

Model lookup, routing, allow/deny filters and execution share case-insensitive
namespace/short-name matching and terminal `model(effort)` suffix parsing.
Execution requires a model in the current snapshot. Suffix effort overrides
body `reasoning_effort`, matching the pinned CPA host. An effort must be both
supported by the model and representable by the gateway's low/medium/high/xhigh/max
enum. Only that intersection is advertised to the host. Unknown/unsupported
levels, explicit none/off, minimal, and numeric budgets fail instead of changing
the request's meaning. Auto/default/-1 select the gateway default; an omitted
or empty body effort does too, but an empty suffix is invalid. Filters refer to
the base model, so adding a suffix cannot bypass them.

## Protocol and resource boundaries

One EventDecoder handles streaming and non-streaming inputs. Blank lines and valid
unknown event types are tolerated; malformed JSON or a missing type fails the turn.
A finish event is terminal in both modes; EOF without finish is truncation.
Cached token usage has the same shape in all response modes.

GatewayClient defaults: 10-minute request deadline, 60-second response-header
timeout, 8 MiB per NDJSON event, 64 MiB per turn. These limits are explicit fields
on the client, copied into each decoder; they are not YAML settings.
Error bodies are sampled at 2048 bytes. The C request envelope is capped at 64 MiB.
Streaming retains counters/usage but does not duplicate the entire emitted answer.
Incoming text/image parts preserve their original order.

Tool-call arguments are compacted as raw JSON, never converted through float64.
Historical arguments use the same lossless path; malformed values reject the
request instead of becoming `{}` or a synthetic `_raw` parameter. Tool schema
fields remain raw JSON when adding the gateway's required object type. Missing
or null schemas become empty object schemas; explicitly invalid/non-object
schemas fail. A string-wrapped object schema is unwrapped once without parsing
its numeric values. Generic client JSON values use Decoder.UseNumber so numeric
tool results also retain their exact values.

CountTokens returns pluginapi.ExecutorResponse with a JSON Payload and a cheap
byte-length estimate (`len(original_request)/4 + 8`, translated payload fallback).
It is not a tokenizer and does not call the gateway. Response metadata and the
X-Cmdcode-Token-Count header label it as an estimate. CPA v7.2.149 sends plugin
count payloads through its ordinary completion translator and overwrites the
client format before this RPC. A reserved count ID and prompt-usage carrier
therefore preserve the count across its OpenAI-to-Claude conversion. Our narrowly
scoped nonstream response-after hook recognizes that ID and restores Claude's
exact `{"input_tokens": N}` body. Same-format callers use the top-level
input_tokens field; its ID/usage fields are transport metadata. Ordinary responses
and streams are untouched. The regression runs the actual pinned SDK translator
with the same rewritten format fields as the host.

## Testing

- Existing protocol fixtures remain in gateway_test.go.
- components_test.go covers config atomicity, snapshot isolation, refresh
  cooldown/single-flight/stale publication, shutdown cancellation/callback waiting,
  budget provenance, message ordering, decoder parity and size limits.
- compat_test.go contains test-only adapters for older regression test names;
  production code does not depend on package-global config/registry wrappers.
- Run go test -race ./..., go vet ./..., and a c-shared build from go/.
- Python fixtures validate the package-only model roster and its generator.
- regression_test.go covers lossless numeric tool arguments/history/schemas,
  suffix precedence/filtering, actual SDK count translation, and concurrent ABI
  lifecycle/draining. scripts/test_abi_reload.sh runs the real c-shared library
  through same-path dlopen/init/call/shutdown/dlclose cycles and fresh-config checks.
- scripts/ci-check.sh is the complete offline gate; see docs/CI.md and
  docs/RELEASE.md for native architecture builds and verified release packaging.

Tests use local HTTP servers and injected host callbacks. They do not spend API
credits or validate the current undocumented Command Code service end to end.
