# Audit-fix validation, 2026-10-16

Scope: the five findings against `54a0bda3d9a7e34f563076ab15fe101989aad07b`,
plus native Linux CI and Release-published packaging. CPA host code was not changed.

## Fixed and exercised

- C ABI lifecycle: fresh Plugin state after shutdown, rejection of active double
  initialization, safe concurrent admission/drain and callback completion before
  clearing or replacing the host. A native C harness passed three exact-same-path
  dlopen/init/call/shutdown/dlclose cycles, configuration isolation and repeated
  shutdown. The baseline library failed the regression.
- Tool JSON fidelity: integer values above 2^53, long decimals and extreme
  positive/negative exponents survive real-time output, buffered output, history
  arguments and schema normalization. Invalid historical arguments/schema
  constraints fail as HTTP 400 instead of silently substituting parameters.
- CountTokens: a real SDK ExecutorResponse.Payload, JSON headers and explicit
  estimate metadata. The pinned CPA v7.2.149 SDK's actual request/completion
  translators and plugin hooks produce exactly `{"input_tokens": N}` for Claude.
  Ordinary completion responses remain intact. This is a byte-length estimate,
  not a tokenizer; same-format internal responses also carry reserved ID/usage
  fields needed by the host's translation path.
- Models: case-insensitive canonical/short/namespaced lookup, terminal thinking
  suffix parsing, suffix-over-body precedence and base-model allow/deny filters.
  Unsupported efforts and unknown/disabled models fail locally as HTTP 400;
  model registration advertises only efforts the gateway can represent.
- Formatting is clean. Both native CI architectures share the same gate. Release
  assets require both successful native builds and verified metadata/checksums;
  fixtures cover prereleases, idempotent retry, collisions, moved tags, incomplete
  uploads, immutable/unknown-mutability releases and completion-manifest ordering.

## Local checks

The complete `scripts/ci-check.sh` gate passed on Linux amd64 with Go 1.26.9:

- actionlint v1.7.12 and Bash syntax
- Python extractor/release unit tests
- gofmt, module verification and go vet
- Go unit tests and race detector
- Native CGO c-shared build, real C ABI reload harness
- Lifecycle race regression repeated 20 times in an additional focused run

The default actionlint path was checked with ACTIONLINT unset; preparation and
checking use the same build/ci-tools path across separate shell processes. Local
preparation used an already cached/verified tool because the offline Go registry
cache lacks the optional module deprecation lookup; CI's dependency preparation
is explicitly network-enabled, while the test gate disables module downloads.

An independent review additionally ran race tests and checked the actual pinned
host's counting/translation path. Artifact provenance is checked separately by
`scripts/ci-build.sh` after the integration commit: it refuses dirty builds and
requires the embedded revision to equal the requested SHA.

## Unverified boundaries

- No arm64 machine was available locally. The arm64 workflow is configured for a
  native GitHub runner, but its actual execution must be verified after pushing.
- No GitHub workflow, tag, release, release-asset upload or deployment was started
  during this local validation. Offline fake API tests do not prove repository
  permissions or a future service operation will succeed.
- No real Command Code credentials were read or live inference requests made.
  The tests do not establish current upstream protocol/account/model availability.
- Published immutable releases cannot accept these post-publication assets; the
  workflow fails explicitly and does not change repository security settings.
- Scheduled model updates run an amd64 gate before bot commits. GitHub's token
  recursion rules suppress a second push workflow for those automated commits;
  this limitation is documented in CI.md.
