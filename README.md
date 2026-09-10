# cmdcode-go

Unofficial [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that serves the **CommandCode Go ($1/mo) plan** through the CLI's own `POST /alpha/generate` gateway. The Go plan has no Provider API access (`/provider/v1/*` returns `403 upgrade_required`), so this plugin replays the CLI wire protocol and translates OpenAI chat traffic into it — exactly what the official `command-code` CLI does on every turn.

> Reverse-engineered from the `command-code` bundle (`models.json` records the exact CLI version each roster came from). Not affiliated with Command Code / Langbase. The endpoint is undocumented and can drift; when it does, update `cli_version` first.

## Features

- **Executor** (`both` scope, `chat-completions` in/out): OpenAI chat → gateway NDJSON (`text-delta` / `reasoning-*` / `tool-call` / `finish` / `error` / `abort`) → OpenAI response. Reasoning travels as OpenRouter-style `reasoning_content` deltas so strict OpenAI clients keep working.
- **Streaming + buffered**: incremental SSE relay chunk-by-chunk; non-streaming calls are served by consuming the NDJSON server-side. Hosts without stream ids get a buffered fallback.
- **`response_before_translator`**: adds the missing `data:` prefix for OpenAI→Claude translation (that converter drops unprefixed payloads; the OpenAI HTTP layer frames itself, so raw JSON is required there).
- **`model_registrar` + `model_provider`**: Go-plan roster extracted from the official CLI bundle (see `models.json` for the current list). An [Action](.github/workflows/models.yml) refreshes it from `command-code@latest` every 6 hours; the plugin picks it up via `models_url` (6h TTL) with the compiled table as offline fallback — registration never needs the CLI installed.
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

Paste this to your agent (replace `<host>` with your machine — SSH alias or address):

```text
Deploy the cmdcode-go plugin to my host <host> following
https://github.com/megumin31/cmdcode-go/blob/main/DEPLOY.md:
1. Always build on the target machine itself — never copy a .so across platforms
2. Back up the old plugin, install, restart the host
3. Verify item by item per section 4 and paste me the `models refreshed` line plus the is-active result
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
- Default output budget is `64000` tokens when the client sends none (same as the official CLI), clamped down to each model's registered output cap. Explicit client budgets are respected.
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
| `go/gateway.go` | OpenAI ↔ `/alpha/generate` translation, usage framing, output budgets |
| `go/stream.go` | Incremental NDJSON→SSE relay (`liveStream`), truncation handling |
| `go/executor.go` | Stream / non-stream / buffered execution paths |
| `go/models.go` | Go-plan roster lookups, allow/deny filtering, routing (compiled table + remote snapshot via `activeModelTable`) |
| `go/models_generated.go` | Compiled fallback table — generated, do not edit (see below) |
| `go/models_remote.go` | `models.json` fetch/validation/TTL snapshot, offline fallback |
| `go/config.go` | Host YAML config, key / URL / version resolution |
| `go/normalize.go` | `response_before_translator` hook (OpenAI→Claude `data:` prefix) |
| `go/gateway_test.go` | Regression suite (remote tests use `httptest`, no external network) |
| `models.json` | Published roster contract (`schema_version`, `source_cli_version`, entitled models) |
| `scripts/extract-models.py` | CLI-bundle extractor → `models.json` + `go/models_generated.go` |
| `.github/workflows/models.yml` | 6-hour roster refresh (extract → fmt/vet/test → auto-commit) |
| `DEPLOY.md` | Deployment guide: build matrix, install, verify, troubleshoot, rollback |

## Model roster automation

The Go plan exposes no server-side model list, so the roster mirrors the official CLI's own gating (`opensource` category minus `blockedModels`, uncategorized ids fail open — same as `evaluateModelAccess`):

```bash
git clone --depth 1 --filter=blob:none --sparse https://github.com/anomalyco/models.dev /tmp/models.dev
git -C /tmp/models.dev sparse-checkout set models
python3 scripts/extract-models.py \
  --cli "$(npm root -g)/command-code/dist/cli.mjs" \
  --package "$(npm root -g)/command-code/package.json" \
  --prev models.json --models-dev /tmp/models.dev/models \
  --models-dev-rev "$(git -C /tmp/models.dev rev-parse --short HEAD)" \
  --out models.json --gen go/models_generated.go
```

Output budgets resolve as bundle value → [anomalyco/models.dev](https://github.com/anomalyco/models.dev) `[limit].output` (exact stem match, `-free` aliases included; values exceeding the context window beyond unit tolerance are rejected) → carried from `--prev` → 32768 for ≤204800-context models → 65536 default. Contexts resolve as Tr override → catalog value → models.dev (only when the CLI falls back to its 200000 default) → 200000. Every entry records `output_source`/`context_source` (`bundle`/`modelsdev`/`carry`/`heuristic`, `cli`/`modelsdev`/`default`). An Action runs the extractor against `command-code@latest` every 6 hours, gates on `gofmt`/`go vet`/`go test`, and commits `models.json` + `go/models_generated.go`.
