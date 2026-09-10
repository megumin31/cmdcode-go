# DEPLOY.md — deploying cmdcode-go

This guide gets the plugin running on a host. It is written for an AI
agent with shell access, but every step is copy-pasteable by a human too.

Goal state: `cmdcode-go.so` built **on the host's own OS/arch**, installed
into the host's `plugins/` dir, host restarted, plugin loaded with a fresh
model roster.

## 0. Concepts (30 seconds)

- The plugin is a c-shared `.so`. It loads ONLY on the platform it was
  built for. macOS build on Linux → `invalid ELF header`, plugin dead.
- Two update kinds:
  - **New plugin code** (this guide): rebuild + replace + restart.
  - **New model list only**: no rebuild needed. The plugin refetches
    `models.json` every 6h on its own; restart (or `touch` the host config
    to trigger hot-reload) picks it up immediately.

## 1. Prerequisites

- [ ] Shell access to the host (below uses `ssh <host>`; run locally if
      the host IS this machine).
- [ ] This repo present on the **build machine** (must be the host itself,
      or an identical OS/arch box).
- [ ] Build tools on the build machine: Go 1.26+, gcc + libc headers.
      - Debian/Ubuntu: `sudo apt-get install -y gcc libc6-dev`, plus Go
        from https://go.dev/dl/ (`go1.27+` linux tarball is fine).
      - macOS: `brew install go` (Xcode CLT provides the C toolchain).
- [ ] The CLIProxyAPI host installed, with a writable `plugins/` dir and
      a way to restart it (below: user systemd unit `cliproxyapi`).

## 2. Build (ON the host)

```bash
git -C ~/cmdcode-go pull --rebase origin main   # or: git clone https://github.com/megumin31/cmdcode-go ~/cmdcode-go
cd ~/cmdcode-go/go
go build -buildmode=c-shared -o cmdcode-go.so .
file cmdcode-go.so
# expect the HOST's format, e.g.:
#   ELF 64-bit LSB shared object, x86-64      (Linux x86_64 host)
#   Mach-O 64-bit arm64 dynamically linked shared library   (Apple Silicon Mac)
```

If `file` disagrees with `uname -m` on the host: STOP, wrong machine.

## 3. Install + restart

```bash
cp <cliproxyapi>/plugins/cmdcode-go.so <cliproxyapi>/plugins/cmdcode-go.so.bak
cp ~/cmdcode-go/go/cmdcode-go.so <cliproxyapi>/plugins/cmdcode-go.so
systemctl --user restart cliproxyapi.service   # or however the host runs
sleep 8
```

## 4. Verify (all must hold)

```bash
journalctl --user -u cliproxyapi.service --no-pager --since '5 minutes ago' \
  | grep -iE 'plugin (loaded|registered)|invalid ELF|models refreshed|panic|fatal'
```

- [ ] `plugin loaded plugin_id=cmdcode-go` and `plugin registered` appear.
- [ ] `cmdcode-go: models refreshed from url:...: N models (cli X.Y.Z)`
      appears (needs outbound HTTPS; offline hosts log `keeping compiled
      roster` instead — also healthy).
- [ ] No `invalid ELF header`, no `panic`/`fatal`.
- [ ] `systemctl --user is-active cliproxyapi.service` → `active`.

Then confirm the roster: query the host's `/v1/models` and check for
`cmdcode-go/`-prefixed ids (e.g. `cmdcode-go/deepseek/deepseek-v4-flash`).

## 5. Minimal host config

```yaml
plugins:
  dir: "plugins"
  configs:
    cmdcode-go:
      enabled: true
      priority: 1
      # api_key: "user_..."  # or COMMANDCODE_KEY env, or ~/.commandcode/auth.json
```

Optional roster keys: `models_url` (default: this repo's `models.json`
on main), `models_file` (local override, reloaded on change),
`models_refresh_interval` (default `6h`, `"0"` disables remote refresh),
plus `models` / `disable_models` allow/deny lists. Full sample in
README.md "Configuration".

## 6. Troubleshooting

| Symptom | Cause → fix |
|---|---|
| `invalid ELF header`, plugin unloaded | `.so` built for another OS/arch → rebuild ON the host (§2) |
| `model registrar ... failed: context deadline exceeded` at shutdown | Leftover of a dying host; harmless if the new process registers cleanly right after |
| Roster stuck on old models | TTL (6h) not expired → wait, `touch` host config (hot-reload), or restart |
| `models refresh ... failed`, `keeping compiled roster` | No network / bad URL → offline fallback, still serves baked-in roster; fix network or set `models_file` |
| `rejected (schema_version ...)` / `missing anchor model` | `models_url` points at a foreign file → point it back at this repo or unset |
| Need to undo a deploy | `cp plugins/cmdcode-go.so.bak plugins/cmdcode-go.so` + restart |

## 7. Rollback

```bash
cp <cliproxyapi>/plugins/cmdcode-go.so.bak <cliproxyapi>/plugins/cmdcode-go.so
systemctl --user restart cliproxyapi.service
# then re-run §4
```
