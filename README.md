# cmdcode-go

English | [中文](README.zh-CN.md)

An unofficial [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin for Command Code Go accounts. It translates OpenAI chat requests to the CLI's `/alpha/generate` gateway, with streaming, tool calls, reasoning content and image input.

Not affiliated with Command Code / Langbase. The upstream protocol is undocumented; compatibility may change independently of the model list.

## Quick start

These commands target **Linux**. Build for the host's OS, architecture and libc; building on the host is the simplest option, but a compatible build environment also works. Do not copy a macOS library onto Linux.

Requirements: a CLIProxyAPI v7 host compatible with the SDK version in [go/go.mod](go/go.mod), the Go toolchain specified there, a C compiler and libc development headers, and a Command Code account key. The official CLI is not required when you supply the key directly.

### 1. Build

```bash
git clone https://github.com/megumin31/cmdcode-go.git
cd cmdcode-go
mkdir -p build
(cd go && CGO_ENABLED=1 go build -buildmode=c-shared -o ../build/cmdcode-go.so .)
file build/cmdcode-go.so
```

For an existing checkout, update with `git pull --ff-only origin main` from a clean `main` branch instead of cloning again. Check that `file` reports a Linux shared library for the host's architecture.

### 2. Configure the host

Merge this into the host's configuration; retain other plugins:

```yaml
plugins:
  dir: "plugins"
  configs:
    cmdcode-go:
      enabled: true
      priority: 1
      # api_key: "user_..."  # Or provide COMMANDCODE_KEY to the host process.
```

The account key must be available to the **running host**, not just your interactive shell. For a service, use its environment configuration, plugin configuration, or a CLI auth file readable by the service account.

Key precedence: host auth attributes → plugin `api_key` → `COMMANDCODE_KEY` / `COMMANDCODE_API_KEY` / `COMMAND_CODE_API_KEY` → CLI auth files. File search order is `$COMMANDCODE_CONFIG_DIR/auth.json`, `~/.commandcode/auth.json`, then `~/.config/commandcode/auth.json`.

### 3. Install and restart

Run from the repository root. Set the **actual plugin directory** first. This example assumes a user systemd unit named `cliproxyapi.service`; use your real service manager/unit otherwise.

```bash
PLUGIN_DIR=/absolute/path/to/cliproxyapi/plugins
(
  set -e
  mkdir -p "$PLUGIN_DIR"
  systemctl --user stop cliproxyapi.service
  if [ -f "$PLUGIN_DIR/cmdcode-go.so" ]; then
    cp -p "$PLUGIN_DIR/cmdcode-go.so" "$PLUGIN_DIR/cmdcode-go.so.bak"
  fi
  install -m 755 build/cmdcode-go.so "$PLUGIN_DIR/cmdcode-go.so.new"
  mv "$PLUGIN_DIR/cmdcode-go.so.new" "$PLUGIN_DIR/cmdcode-go.so"
  systemctl --user start cliproxyapi.service
)
```

Stop before replacing a loaded library. The conditional backup also works on first installation. Only the shared library is needed at runtime; the generated C header is not installed.

### 4. Verify

For the systemd example:

```bash
systemctl --user is-active cliproxyapi.service
journalctl --user -u cliproxyapi.service --since "5 minutes ago" --no-pager
```

Check that the host loads/registers the plugin and reports no configuration or library-loading errors. A successful model refresh does **not** print a required success line; a working compiled fallback is also valid.

Query the host using its own client API key, **not** the Command Code account key:

```bash
CLIPROXY_URL=http://127.0.0.1:8317  # Replace with the host's address.
curl --fail-with-body -sS "$CLIPROXY_URL/v1/models" \
  -H "Authorization: Bearer $CLIPROXY_API_KEY"
```

Set `CLIPROXY_API_KEY` to your host client key before running. Compare the returned IDs with [models.json](models.json); host configuration may add prefixes or aliases, so do not require every ID to start with `cmdcode-go/`. Registration verifies loading, not upstream account access. To verify inference, send a small chat request using an ID returned by the host; that makes a real upstream call.

## Configuration reference

All plugin-specific options go under `plugins.configs.cmdcode-go`.

| Option | Default / behavior |
|---|---|
| `api_key` | Optional; credential precedence is described above |
| `base_url` | `https://api.commandcode.ai`; `COMMANDCODE_BASE_URL` overrides the default |
| `cli_version` | `COMMANDCODE_CLI_VERSION`, otherwise `1.47.1`; protocol fingerprint, separate from catalog version |
| `project_slug` | `cliproxyapi` |
| `permission_mode` | `standard` |
| `models` | Empty = all roster models; accepts YAML lists |
| `disable_models` | Empty = none; takes precedence over `models` |
| `models_url` | This repository's `main/models.json` on raw.githubusercontent.com |
| `models_file` | Local JSON override; takes precedence over `models_url` |
| `models_refresh_interval` | `"6h"`; `"0"` selects the compiled roster and disables URL/file refresh |

Names accept canonical IDs, unique short names, and the `cmdcode-go/` prefix. Invalid configuration updates are rejected as a whole. Omitted keys retain their previous values during reconfiguration; use `null` or an empty list to clear a value.

Do not automatically set `cli_version` to the model catalog version: a catalog update does not establish wire-protocol compatibility.

## Model updates

The [models workflow](.github/workflows/models.yml) runs every six hours. It resolves an exact npm version, reads that package's `models.md`, and includes only documented Go models. [models.dev](https://github.com/anomalyco/models.dev) adds metadata without changing membership. Validated results update both JSON and the compiled fallback.

Runtime refresh is **lazy**: model registration/list calls check the six-hour TTL; there is no independent background timer. Local files are checked by modification time. Failures retain the last good roster, cool down for five minutes, and never replace it with an empty list. The fetch timeout is eight seconds.

Model-list changes normally need no rebuild. A host restart resets the in-memory refresh state; simply touching an unchanged host config does not guarantee a refresh. For reproducible/offline lists, use `models_file`; see [ARCHITECTURE.md](ARCHITECTURE.md) for schema and validation details.

## Upgrade and rollback

For code changes: update a clean checkout, repeat **Build**, then **Install and restart**, then **Verify**. Keep the backup until verification succeeds.

To restore the previous binary, first set `PLUGIN_DIR` as above:

```bash
test -f "$PLUGIN_DIR/cmdcode-go.so.bak" &&
systemctl --user stop cliproxyapi.service &&
cp -p "$PLUGIN_DIR/cmdcode-go.so.bak" "$PLUGIN_DIR/cmdcode-go.so.restore" &&
mv "$PLUGIN_DIR/cmdcode-go.so.restore" "$PLUGIN_DIR/cmdcode-go.so" &&
systemctl --user start cliproxyapi.service
```

Verify again after rollback. This restores the binary only; restore configuration separately if you changed it. Remote models still follow `main` unless you pin a local file or disable refresh.

## Troubleshooting

| Symptom | What to check |
|---|---|
| `invalid ELF header`, wrong architecture, missing libc symbols | Build OS/architecture/libc compatibility and CGO toolchain |
| Plugin absent from host | Actual plugin directory, enabled configuration, file permissions and host logs |
| Missing key / authentication error | Service account's environment and auth files; distinguish host client key from upstream account key |
| Model list unchanged | Lazy refresh trigger, TTL, file modification time, and five-minute failure cooldown |
| `model refresh failed` | URL/network or JSON validation; the previous/compiled roster remains available |
| Version-related upstream rejection | Configured fingerprint and current upstream response; do not infer it from catalog version |
| Empty answer with `finish_reason: length` | Reasoning may consume the budget; inspect reasoning content and requested token limit |
| Shutdown takes time / truncated stream | Inspect logs; shutdown waits for callbacks, and missing finish events are errors |

## Limits and development

Missing upstream usage is reported as zeros, not a measured zero-cost turn. Fallback output budgets choose defaults; explicit budgets are clamped only by a known `gateway_output_limit`. Forced tool selection is not fully supported. Client disconnection is detected on a failed host emit; an idle upstream may continue until the request timeout. See [architecture and protocol limits](ARCHITECTURE.md).

From the repository root:

```bash
python3 -m unittest discover -s scripts -p 'test_*.py'
(cd go && go vet ./... && go test -race ./...)
```

Python tests need Python 3.11+. Go tests use local servers and injected callbacks; they do not require an upstream key. Downloading build/test dependencies requires network access on first use. CI also builds the c-shared library. These checks do not establish live gateway compatibility.

For model generation options, run `python3 scripts/extract-models.py --help`; the workflow contains the full package-fetch and generation procedure. The five-component design and source layout are documented in [ARCHITECTURE.md](ARCHITECTURE.md).
