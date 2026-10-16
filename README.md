# cpa-cmdcode-go

English | [中文](README.zh-CN.md)

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin for Command Code Go ($1/mo plan).

Access all Command Code Go models through standard OpenAI-compatible endpoints via CLIProxyAPI, without running the Command Code CLI in the background.

## Features

- **Full Capability**: Streaming (SSE), non-streaming completions, tool calls (function calling), reasoning content / thinking tokens, and multimodal image input.
- **Namespaced Model IDs**: Models are registered as `cmdcode-go/<model_id>` (e.g. `cmdcode-go/deepseek/deepseek-v4-flash`, `cmdcode-go/moonshotai/Kimi-K3`) to avoid naming collisions with other providers.
- **Zero-Config Credentials**: Supports explicit `api_key`, `COMMANDCODE_KEY` environment variables, or automatically reading your local CLI login state (`~/.commandcode/auth.json` from `cmd login`).
- **Dynamic Model Roster**: Includes a compiled-in model list and automatically refreshes from GitHub to stay up to date with new models.

## Quick Start

### 1. Download or build

[Published releases](https://github.com/megumin31/cpa-cmdcode-go/releases) provide native Linux `amd64` and `arm64` archives after both architectures pass CI. Verify their checksums and completion manifest, then install the inner `cmdcode-go-v0.1.0.so` without renaming it. See [release instructions](docs/RELEASE.md).

To build locally:

```bash
git clone https://github.com/megumin31/cpa-cmdcode-go.git
cd cpa-cmdcode-go
mkdir -p build
LIBRARY="$(cd go && go run ./cmd/buildmeta --field filename)"
(cd go && CGO_ENABLED=1 go build -buildmode=c-shared -o "../build/$LIBRARY" .)
```

Copy the generated `build/cmdcode-go-v0.1.0.so` to your CLIProxyAPI plugins directory.

### 2. Configure CLIProxyAPI

Add the plugin configuration to CLIProxyAPI's `config.yaml`:

```yaml
plugins:
  dir: "/path/to/plugins"
  configs:
    cmdcode-go:
      enabled: true
      priority: 1
      api_key: "YOUR_COMMAND_CODE_KEY" # Optional if you have already logged in via `cmd login`
```

Start or restart CLIProxyAPI.

### 3. Usage

Point your OpenAI-compatible client to your CLIProxyAPI instance.

List available models:
```bash
curl -s http://127.0.0.1:8317/v1/models \
  -H "Authorization: Bearer YOUR_CLIPROXY_KEY"
```

Chat completions:
```bash
curl -N http://127.0.0.1:8317/v1/chat/completions \
  -H "Authorization: Bearer YOUR_CLIPROXY_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "cmdcode-go/deepseek/deepseek-v4-flash",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

> **Note**: Requests can use either the full ID (`cmdcode-go/<model_id>`, recommended) or short names (e.g. `Kimi-K3`, `deepseek/deepseek-v4-flash`). See [models.json](models.json) for the full model catalog.

## Configuration Options

Fields under `plugins.configs.cmdcode-go`:

| Option | Description | Default |
|---|---|---|
| `api_key` | Command Code account key (`user_...`) | Reads env vars / CLI auth file if omitted |
| `models` | Allowlist of models to enable | All models |
| `disable_models` | Denylist of models to exclude | None |
| `models_refresh_interval` | Interval to check for remote roster updates | `"6h"` (`"0"` to use compiled-in roster only) |
| `models_file` | Path to a local model catalog JSON file | Unset |
| `models_url` | URL to fetch model catalog JSON from | `models.json` on `main` branch |

## Development and CI

Every branch push and pull request runs the same native Linux amd64/arm64 quality gate used by release builds: formatting, vet, tests, race detector, Python fixtures, workflow lint, C-shared build and the C ABI reload harness. Tests use local fakes and do not call live inference. See [local commands and CI artifacts](docs/CI.md) and [release retry rules](docs/RELEASE.md).

## License

This project is licensed under the [AGPL-3.0](LICENSE) License.
