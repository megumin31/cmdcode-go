# Native quality gate

`.github/workflows/build-plugin.yml` calls the reusable `quality-gate.yml` for
every branch push, pull request, manual branch run and `v*` tag push.
Only a valid tag push publishes a Release after both native gates succeed;
there is no `release.published` trigger. See [tag-driven releases](RELEASE.md).
Branches and PRs build numeric ABI version `0.0.0` with `build_tag=dev`.

## What runs

Both Linux architectures run natively, rather than cross-compiling a binary that
cannot be executed by its test runner:

| Architecture | GitHub runner | Go |
| --- | --- | --- |
| amd64 / x86-64 | `ubuntu-22.04` | `1.26.9` |
| arm64 / AArch64 | `ubuntu-22.04-arm` | `1.26.9` |

`scripts/ci-prepare.sh` prepares Go dependencies and pinned actionlint
`v1.7.12`. After preparation, `scripts/ci-check.sh` sets `GOPROXY=off` and
`GOSUMDB=off`, and runs the shared full gate:

1. actionlint for all workflows, plus Bash syntax checks.
2. Python extractor and release-publication fixture tests.
3. Go formatting, module checksum verification, vet, unit tests and race tests.
4. A native CGO C-shared build using the filename from `go/internal/buildinfo`.
5. The C ABI harness against that exact library: same-path reload, repeated
   shutdown, double-init rejection, configuration isolation and the loaded library's registration version matching the injected build metadata.

All test traffic is synthetic or loopback. No provider keys are required; the
gate clears inherited Command Code key variables and uses a temporary CLI
configuration directory. These checks do not verify a live Command Code plan,
model availability, upstream credentials or real inference behavior.

`scripts/ci-build.sh` additionally verifies the native host architecture, ELF
machine/type, expected C ABI exports, clean embedded commit, Go modules and
provenance. Build/release jobs check out the event's exact commit. Local uncommitted
changes can pass `ci-check.sh`, but cannot pass the clean-revision artifact check.

## Run locally

Use Linux with Go 1.26.9, Python 3.11+, a C compiler, binutils, Git and coreutils.
Preparation installs pinned actionlint into ignored `build/ci-tools/`:

```sh
bash scripts/ci-prepare.sh
bash scripts/ci-check.sh
```

Dependency preparation may need network access. The quality gate itself does
not fetch dependencies and fails when its cached dependencies or tools are absent.
The same gate runs on newly generated rosters on **amd64 only** before the
scheduled/manual `models.yml` job commits them. Model refresh remains the only roster-update
workflow; its old PR trigger is replaced by the central dual-architecture gate.
No `workflow_run` loop or dispatch chaining is used. GitHub-token bot commits do
not trigger another push workflow; these automated commits therefore receive
the refresh job's amd64 precommit gate, not an automatic dual-architecture run.
A later ordinary branch push, PR or tag push runs both architectures.
The updater commits exactly its tested tree; if the remote branch advances,
the non-force push fails rather than rebasing or overwriting untested changes.

To prepare a CI artifact from an already committed, clean checkout:

```sh
export TARGET_ARCH="$(go env GOHOSTARCH)"
export GITHUB_SHA="$(git rev-parse HEAD)"
export GITHUB_REPOSITORY=megumin31/cpa-cmdcode-go
export RUNNER_LABEL=local-linux
export GOOS=linux GOARCH="$TARGET_ARCH" CGO_ENABLED=1
export GOFLAGS='-mod=readonly -trimpath -buildvcs=true'
bash scripts/ci-build.sh
```

## Artifacts and permissions

Each native job retains `cmdcode-go-linux-<arch>-<commit>` for 14 days. It contains
the original `cmdcode-go-v<version>.so`, plugin identity, `LICENSE`, per-file
SHA256 sums, Go build information, ELF/glibc reports and build metadata. Separate
`build-logs-linux-<arch>-<commit>` artifacts retain available diagnostics even when
a build fails. Packages use glibc; inspect their recorded GLIBC requirements
before installing on another distribution.

Push/PR/manual build jobs have `contents: read` and no provider secrets. Only
the release asset job grants `contents: write`, and only after both native jobs
succeed. The existing scheduled roster writer separately retains its necessary
write access. Publishing a release never bypasses the quality gate. See
[release assets, safety checks and reruns](RELEASE.md).

## Official references

- [Release events and published prereleases](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#release)
- [GitHub-hosted native runners](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)
- [Reusable workflows and permission limits](https://docs.github.com/en/actions/how-tos/reuse-automations/reuse-workflows)
- [Workflow token behavior](https://docs.github.com/en/actions/how-tos/writing-workflows/choosing-when-your-workflow-runs/triggering-a-workflow)
