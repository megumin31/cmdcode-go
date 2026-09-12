# cmdcode-go

English | [中文版](README.zh-CN.md)

Unofficial [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that serves the **CommandCode Go ($1/mo) plan** through the CLI's own `POST /alpha/generate` gateway. The Go plan has no Provider API access (`/provider/v1/*` returns `403 upgrade_required`), so this plugin replays the CLI wire protocol and translates OpenAI chat traffic into it — exactly what the official `command-code` CLI does on every turn.

> Reverse-engineered from the `command-code` bundle (`models.json` records the exact CLI version each roster came from). Not affiliated with Command Code / Langbase. The endpoint is undocumented and can drift; when it does, update `cli_version` first.

## Features

- **Executor** (`both` scope, `chat-completions` in/out): OpenAI chat → gateway NDJSON (`text-delta` / `reasoning-*` / `tool-call` / `finish` / `error` / `abort`) → OpenAI response. Reasoning travels as OpenRouter-style `reasoning_content` deltas so strict OpenAI clients keep working.
- **Streaming + buffered**: incremental SSE relay chunk-by-chunk; non-streaming calls are served by consuming the NDJSON server-side. Hosts without stream ids get a buffered fallback.
- **`response_before_translator`**: adds the missing `data:` prefix for OpenAI→Claude translation (that converter drops unprefixed payloads; the OpenAI HTTP layer frames itself, so raw JSON is required there).
- **`model_registrar` + `model_provider`**: Go-plan roster extracted solely from the official package’s models.md (see `models.json` for the current list). An [Action](.github/workflows/models.yml) refreshes it from `command-code@latest` every 6 hours; the plugin picks it up via `models_url` (6h TTL) with the compiled table as offline fallback — registration never needs the CLI installed.
- **`model_router`**: claims `cmdcode-go/<id>`, canonical and short names; everything else falls through to other providers.
- **Tool round-trip**: tool schemas are coerced to the `{type: object}` record the gateway demands (null/missing schemas become `{"type":"object","properties":{}}` instead of failing the turn); `tool_choice: none` strips tools.
- **Usage accounting**: `usage` is always present on both paths. When a `finish` event omits `totalUsage`, downstream gets zeros (the official CLI harness default), never null. Every 100 turns a summary goes to stderr for local audits:
  `cmdcode-go: usage turns=N with_totalUsage=X zero_fallback=Y truncated=Z`
- **Truncation is a failure**: a turn ending without a `finish` event surfaces as an error so the host can retry (mirroring the official 502), never a synthesized `stop`.

## Requirements

- CLIProxyAPI v7 host (`github.com/router-for-me/CLIProxyAPI/v7` v7.2.149)
- Go 1.26+ (build only)
- A CommandCode account key (`user_...`)

## Install

```bash
cd go && go build -buildmode=c-shared -o cmdcode-go.so .
cp cmdcode-go.so <cliproxyapi>/plugins/
# then restart the host (plugins load at startup; roster-only updates
# need no rebuild — see "Model roster automation" and DEPLOY.md)
```

> Build ON the machine that runs the host: the `.so` is OS/arch-specific
> (a macOS build on Linux fails with `invalid ELF header`). Needs Go 1.26+,
> CGO, gcc + libc headers. Step-by-step deploy guide: [DEPLOY.md](DEPLOY.md).

Run the translation regression tests (no network, no key needed):

```bash
cd go && go test ./...
```

### Deploy with your agent

Paste this to your agent:

```text
Deploy the cmdcode-go plugin following
https://github.com/megumin31/cmdcode-go/blob/main/DEPLOY.md:
1. Find where the CLIProxyAPI host runs; always build on that machine itself — never copy a .so across platforms
2. Back up the old plugin, install, restart the host
3. Verify item by item per section 4 and paste me the `models refreshed` line plus the service status
4. On any problem, check the section 6 troubleshooting table first; ask me only if stuck
```

## Configuration

```yaml
plugins:
  dir: "plugins"
  configs:
    cmdcode-go:
      enabled: true
      priority: 1
      # api_key: "user_..."    # or COMMANDCODE_KEY env / ~/.commandcode/auth.json
      # base_url: "https://api.commandcode.ai"
      # cli_version: "1.47.1"  # must track the installed CLI; stale versions get rejected
      # project_slug: "cliproxyapi"
      # permission_mode: "standard"  # forwarded as permissionMode
      # models: ["deepseek/deepseek-v4-flash"]  # allowlist, empty = all
      # disable_models: ["xai/grok-4.5"]        # denylist
      # models_url: "https://raw.githubusercontent.com/megumin31/cmdcode-go/main/models.json"
      # models_file: "/etc/cmdcode-go/models.json"  # local override, reloaded on change
      # models_refresh_interval: "6h"  # Go duration; "0" disables remote refresh
```

Key resolution order: host auth attributes → plugin `api_key` → `COMMANDCODE_KEY` (also `COMMANDCODE_API_KEY`, `COMMAND_CODE_API_KEY`) → CLI auth file (`~/.commandcode/auth.json`, `$COMMANDCODE_CONFIG_DIR/auth.json`).

## Behavior notes

- The gateway rejects `stream:false`; the plugin always sends `stream:true`.
- An omitted output budget uses the smaller of `64000` and the model's reference/default budget. Explicit budgets are clamped only by a known `gateway_output_limit`, never by a metadata fallback.
- `reasoning_effort` is clamped to the gateway enum (`low|medium|high|xhigh|max`); unknown values degrade to the model default instead of failing the turn.
- Reasoning-heavy models spend `max_tokens` on thinking first — tiny caps return `finish_reason: length` with empty content. Give generous budgets (models were observed using 300+ thinking tokens).
- Chunk payloads stay raw JSON — the host adds `data:` framing and the terminal `data: [DONE]` itself. Pre-framed payloads would double up.
- Upstream 503s surface as HTTP 503 with `isRetryable` preserved (free-tier models shed load under concurrency; clients should retry).
- Model roster: `model.register`/`model.static` refresh from `models_file` (on change) or `models_url` (TTL `models_refresh_interval`, default 6h, 8s fetch timeout). Any fetch/parse/validation failure keeps the previous snapshot or the compiled table — never an empty list. Fetched payloads must be `schema_version: 1` with the anchor model `deepseek/deepseek-v4-flash` present.

## Verification status

- `go vet` + `go test` green (33 tests, `-race` clean: translation, tool round-trip, `tool_choice:none`, adversarial inputs, host-YAML config, NDJSON edges, incremental-stream parser, framing hook, effort sanitizer, schema coercion, model allow/deny filtering, routing contract, key precedence, UUID shape, error-status mapping, zero-fallback usage, truncation contract, usage-turn counters, remote roster fetch/cache/file/disabled, roster config keys).
- Stress verified through a real host binary: 12-way parallel mixed-model burst, 12× sequential sustained (12/12), 4× mid-stream cancels with instant recovery, 3787-token single-turn output, 200KB tool_result input, image input, scripted agentic tool loop, and official `openai` + `anthropic` SDK suites on all three protocols.

## Project layout

| File | Role |
|---|---|
| `go/main.go` | c-shared exports, host RPC dispatch |
| `go/request.go` + `go/response.go` | OpenAI ↔ `/alpha/generate` translation, usage framing, output budgets |
| `go/stream.go` | Incremental NDJSON→SSE relay (`EventDecoder`), truncation handling |
| `go/executor.go` | Stream / non-stream / buffered execution paths |
| `go/models.go` | Go-plan roster lookups, allow/deny filtering, routing (compiled table + remote snapshot via immutable snapshots) |
| `go/models_generated.go` | Compiled fallback table — generated, do not edit (see below) |
| `go/models_remote.go` | `models.json` fetch/validation/TTL snapshot, offline fallback |
| `go/config.go` | Host YAML config, key / URL / version resolution |
| `go/normalize.go` | `response_before_translator` hook (OpenAI→Claude `data:` prefix) |
| `go/gateway_test.go` | Regression suite (remote tests use `httptest`, no external network) |
| `models.json` | Published roster contract (`schema_version`, `source_cli_version`, entitled models) |
| `scripts/extract-models.py` | models.md extractor → `models.json` + `go/models_generated.go` |
| `.github/workflows/models.yml` | 6-hour roster refresh (extract → fmt/vet/test → auto-commit) |
| `DEPLOY.md` | Deployment guide: build matrix, install, verify, troubleshoot, rollback |

## Model roster automation

Membership, IDs, names, minimum plans and reasoning efforts come solely from
`dist/bundled/command-code-knowledge/reference/models.md` in an exact npm package.
Only Go rows are included. No cli.mjs parsing or execution; models.dev enriches only.

Every six hours the Action resolves latest to an exact version, downloads with
`npm pack --ignore-scripts`, and extracts only package.json and models.md.
Package integrity, document SHA-256 and the full models.dev commit are recorded.
Metadata can refresh even when the CLI version is unchanged. Identical inputs are idempotent.

Context uses the documented deployment window (decimal K/M), then models.dev,
then a labelled 200000 default. Another provider's precise integer does not override
Command Code's advertised window. Output uses models.dev if within context, then a
previous models.dev-sourced value, then a labelled fallback (32768 for small windows,
otherwise 65536, capped at context). These budgets are not verified gateway limits.
Unknown modalities remain null.

Matching is case-insensitive exact full ID, then a globally unique exact basename.
Punctuation and free/fast/preview suffixes are preserved; ambiguous matches are skipped.
Missing columns, unknown plans, duplicate IDs, short-name collisions and abnormal counts
fail without publishing. Large changes require review and workflow_dispatch's
allow_large_drift override. Fixed-version fixtures cover extraction, while fixed
model fixtures test routing and budgets independently of live plans.
JSON schema 1 remains compatible; the updated runtime allows the documented roster
to retire the legacy anchor model.

Local generation (PKG is the unpacked package directory; META is the models.dev clone):

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
python3 scripts/extract-models.py \
  --models-md "$PKG/dist/bundled/command-code-knowledge/reference/models.md" \
  --package "$PKG/package.json" --prev models.json \
  --models-dev "$META/models" --models-dev-rev "$(git -C "$META" rev-parse HEAD)" \
  --out models.json --gen go/models_generated.go
```

## Component design

The single-package, c-shared plugin has five concrete components: Plugin,
ConfigStore, ModelRegistry, GatewayClient and EventDecoder.
See [ARCHITECTURE.md](ARCHITECTURE.md) for ownership, limits and cancellation details.

Shutdown cancels and waits for streams and host callbacks. Refreshes are throttled,
single-flight and configuration-generation checked. Typed YAML updates are atomic.
Each turn uses a coherent config/model snapshot; text/image ordering is preserved.
Malformed NDJSON fails the turn; all output modes share terminal and usage semantics.

Vision and reasoning efforts survive both compiled and dynamic model registration.
Fallback output budgets choose defaults, not fabricated hard limits. Explicit client
budgets are clamped only by a provided gateway_output_limit. Current ABI client
cancellation is detected on the next failed host emit, not instantaneously during
upstream silence. CI runs race tests and builds the actual shared library.
