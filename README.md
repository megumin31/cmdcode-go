# cmdcode-go

English | [中文](README.zh-CN.md)

An unofficial plugin that connects **Command Code Go to CLIProxyAPI**. Use your existing OpenAI-compatible clients to access Go-plan models through CLIProxyAPI, without running the Command Code CLI separately.

Supports streaming and non-streaming chat, tool calls, reasoning content, and image input on supported models. The plugin runs inside CLIProxyAPI as a single `.so` library; no additional service is required.

## Installation

The steps below target Linux. You need:

- A CLIProxyAPI v7 host with plugin ABI support. See [go.mod](go/go.mod) for the SDK version used by this project.
- Go 1.26+, a C compiler, and libc development headers. The build must be compatible with the host's OS, CPU architecture, and libc.
- A working Command Code Go account and its account key.

### 1. Build the plugin

```bash
git clone https://github.com/megumin31/cmdcode-go.git
cd cmdcode-go
mkdir -p build
(cd go && CGO_ENABLED=1 go build -buildmode=c-shared -o ../build/cmdcode-go.so .)
```

The output is `build/cmdcode-go.so`. You do not need to install the generated `.h` file.

### 2. Configure and install

Add this to your CLIProxyAPI configuration. If a `plugins` section already exists, merge the fields into it:

```yaml
plugins:
  dir: "/absolute/path/to/plugins"
  configs:
    cmdcode-go:
      enabled: true
      priority: 1
      api_key: "YOUR_COMMAND_CODE_KEY"
```

Replace the directory and key with your own values. Stop CLIProxyAPI, then run from the repository root:

```bash
install -Dm755 build/cmdcode-go.so /absolute/path/to/plugins/cmdcode-go.so
```

Start CLIProxyAPI using your usual service manager or startup command.

**Reuse an existing login:** If the account running CLIProxyAPI has already logged in to Command Code, omit `api_key` to use its CLI auth file. Alternatively, provide `COMMANDCODE_KEY` to the host process. For systemd or containers, set the variable in the service or container environment.

## Usage

Keep your client configured with **CLIProxyAPI's URL and client API key**. The Command Code account key is used only by the plugin to authenticate upstream.

List the models exposed by your host:

```bash
export CLIPROXY_URL="http://127.0.0.1:8317"
export CLIPROXY_API_KEY="YOUR_CLIPROXYAPI_KEY"

curl --fail-with-body -sS "$CLIPROXY_URL/v1/models" \
  -H "Authorization: Bearer $CLIPROXY_API_KEY"
```

Choose an ID belonging to this plugin from the response, replace `MODEL_ID` below, and send a streaming chat request:

```bash
curl --fail-with-body -N "$CLIPROXY_URL/v1/chat/completions" \
  -H "Authorization: Bearer $CLIPROXY_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "MODEL_ID",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

See [models.json](models.json) for the roster. Your host may apply aliases or prefixes, so use the IDs returned by `/v1/models`. Listing a model confirms registration; a successful chat request also verifies upstream account access.

## Configuration

These fields belong under `plugins.configs.cmdcode-go`. The host manages `enabled` and `priority`.

| Field | Purpose | Default behavior |
|---|---|---|
| `api_key` | Command Code account key | Falls back to environment variables and CLI auth files |
| `models` | Allow only these models; YAML list | An empty list allows the entire roster |
| `disable_models` | Exclude these models; YAML list | No exclusions; takes precedence over `models` |
| `models_file` | Local model JSON | Unset; overrides the remote URL when supplied |
| `models_url` | Remote model JSON URL | This repository's `main/models.json` |
| `models_refresh_interval` | Remote roster refresh interval | `"6h"`; `"0"` disables both remote and local refresh and uses the compiled roster |

Model filters accept full IDs, unique short names, and the `cmdcode-go/` prefix. Prefer full IDs copied from the roster to avoid ambiguity.

<details>
<summary>Advanced configuration and credential lookup</summary>

| Field | Default | Purpose |
|---|---|---|
| `base_url` | `https://api.commandcode.ai` | Upstream gateway; also reads `COMMANDCODE_BASE_URL` when unset |
| `cli_version` | `1.47.1` | CLI version sent with requests; also reads `COMMANDCODE_CLI_VERSION` when unset |
| `project_slug` | `cliproxyapi` | Project identifier sent upstream |
| `permission_mode` | `standard` | Permission mode sent upstream |

`cli_version` is separate from the package version used to generate the model roster. Roster updates do not change the request protocol or version identifier.

Credentials are resolved in this order, using the first nonempty value:

1. The `api_key` auth attribute selected by the host for the request.
2. Plugin configuration `api_key`.
3. Environment variables `COMMANDCODE_KEY`, `COMMANDCODE_API_KEY`, then `COMMAND_CODE_API_KEY`.
4. `$COMMANDCODE_CONFIG_DIR/auth.json`, `~/.commandcode/auth.json`, then `~/.config/commandcode/auth.json`.

Here, `~` belongs to the account running the host. Auth files may use either `apiKey` or `api_key`.

Invalid configuration updates are rejected as a whole. Omitted fields retain their previous values during reconfiguration. Use `null` to clear a string field or `[]` to clear a model filter.

</details>

## Model and plugin updates

**Model roster:** GitHub Actions checks the Command Code npm package every six hours. Its bundled `models.md` is the sole source of model membership and minimum plan requirements; only Go-plan models are included. models.dev supplies additional metadata such as output lengths and modalities. Generated results are committed after validation and tests pass.

The plugin fetches the roster on demand: it checks for refresh when the host invokes a plugin model registration or query method, rather than pushing updates to the host every six hours. A failed refresh retains the last good roster; an initial failure uses the compiled roster. Model updates therefore usually need no rebuild, but when clients see them depends on when the host queries the plugin again.

To pin a roster, save this repository's `models.json` locally and set `models_file`. Local files are reloaded when their modification time changes; leave refresh enabled. To use only the compiled roster, set `models_refresh_interval: "0"`.

**Plugin code:** Pull the latest code and rebuild, then stop the host, replace the `.so`, and start it again. Keep the previous library and configuration for rollback. Rolling back the library does not pin the remote roster.

## Compatibility

This project is not affiliated with Command Code / Langbase and uses the CLI's undocumented gateway protocol. Upstream protocol or account-policy changes may require a plugin update; roster updates alone cannot address every compatibility issue.

- Tool messages and results are supported, but forced tool selection is not fully supported.
- Image and reasoning capabilities depend on the model. Reasoning text is returned in `reasoning_content`.
- Output lengths from models.dev are reference metadata, not confirmed Command Code gateway limits.
- When upstream token usage is absent, usage fields contain zeros; this does not mean the request consumed no tokens.

See [ARCHITECTURE.md](ARCHITECTURE.md) for additional protocol and lifecycle boundaries.

## Troubleshooting

| Problem | Check |
|---|---|
| Plugin fails to load | Host logs, plugin directory, permissions, and the library's architecture/libc compatibility |
| Authentication fails | Whether the Command Code key is valid and visible to the host process |
| Client cannot find a model | IDs from `/v1/models`, model filters, and whether the host has queried the plugin again |
| `model refresh failed` in logs | Remote connectivity or the local JSON file; the last valid roster remains available |
| Reasoning appears but the answer is empty | Whether the response ended with `finish_reason: length` and reasoning exhausted the token budget |

## Development

Run from the repository root:

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
(cd go && go vet ./... && go test -race ./...)
```

Python tests require Python 3.11+. Tests use local data and mock servers; no upstream key is required. Also run the shared-library build above before submitting code changes.

- [ARCHITECTURE.md](ARCHITECTURE.md): component ownership, protocol translation, and concurrency design.
- [models workflow](.github/workflows/models.yml): package retrieval, generation, and validation.
- `python3 scripts/extract-models.py --help`: generator options.

## License

[GPL-3.0](LICENSE).
