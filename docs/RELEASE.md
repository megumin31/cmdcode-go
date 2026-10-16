# Automatic release assets

Publishing a GitHub Release runs the existing **Build Linux plugins** workflow
for native Linux `amd64` (x86-64) and `arm64` (AArch64). Both jobs use the same
reusable `quality-gate.yml`, `scripts/ci-build.sh` and complete offline gates as
push/PR builds. Only after both
succeed does a separate job package and attach their verified outputs to that
specific release. Stable releases and published prereleases are supported.

## Preparing a release

1. Commit the intended source, including this workflow and its scripts. A release
   of an older tag uses that older commit's workflow, not the current branch's.
2. Set the numeric plugin version in `go/internal/buildinfo/buildinfo.go` and commit
   it. This remains the only source for the plugin's version and library name.
3. Create the corresponding tag and publish its GitHub Release. A stable tag
   must be `vX.Y.Z` matching buildinfo. A prerelease may use `vX.Y.Z` or a suffix
   such as `vX.Y.Z-rc.1`, and must be marked **pre-release** on GitHub. Its plugin
   version and inner library filename remain numeric, for CPA compatibility.
4. Wait for both build jobs and **Attach verified release assets** to succeed.
   Check the completion manifest and checksums before installing.

The workflow checks out the event's exact commit SHA, not a branch or a mutable
tag name. Before each upload it checks that the release ID, tag, published state
and tag's resolved commit still agree with that event. It never creates tags,
creates releases, changes release notes or changes repository settings.

This published-event flow requires a **mutable release**. GitHub immutable
releases lock their assets when published, so the upload step explicitly fails
if immutability is enabled. Supporting that policy would require a separate
build-and-upload-to-draft flow before publication; this workflow does not disable
immutability. A release created by another workflow's `GITHUB_TOKEN` does not
trigger another workflow automatically; publish through the GitHub UI or an
appropriately authorized external flow instead.

## Download and install

For a `v0.1.0` release, the expected asset set is:

- `cmdcode-go-v0.1.0-linux-amd64.tar.gz`
- `cmdcode-go-v0.1.0-linux-arm64.tar.gz`
- `cmdcode-go-v0.1.0-SHA256SUMS`
- `cmdcode-go-v0.1.0-release.json` (completion manifest)

Download the archive for the host's architecture and the checksum file. Verify
its listed SHA256 before extracting. For example, with both archives downloaded:

```sh
sha256sum -c cmdcode-go-v0.1.0-SHA256SUMS
mkdir cmdcode-go-release
tar -xzf cmdcode-go-v0.1.0-linux-amd64.tar.gz -C cmdcode-go-release
(cd cmdcode-go-release && sha256sum -c SHA256SUMS)
```

Each archive retains the host-compatible `cmdcode-go-v0.1.0.so` name, `LICENSE`,
plugin metadata, per-file checksums, Go build information, ELF/glibc requirements
and provenance. The outer archive distinguishes architectures; do not rename the
inner `.so` to add an architecture or prerelease suffix. Install only the correct
architecture, and do not leave conflicting versions in CPA's plugin directory.
See [CI and local validation](CI.md).

The completion manifest records the release ID, tag, commit, plugin version,
architecture library digests, and uploaded archive/checksum digests. SHA256
detects corruption; these files are not an independent signature or attestation.
Inspect `required_glibc_versions` in the archived build metadata when checking
host compatibility. These are native glibc builds, not universal Linux binaries.

## Failures and reruns

- A failed build prevents the publish job; a missing or mismatched architecture
  prevents asset uploads.
  Archives are prepared only after both inputs pass metadata, checksum, ELF and
  exact-clean-revision verification.
- GitHub asset uploads are not atomic. A network failure can leave some assets
  visible. The completion manifest is uploaded last, only after all payloads
  have verified server-side digests (or downloaded hashes when the API supplies
  no digest). Any upload or final verification failure fails the job. A response
  can be lost after the server accepted an upload, so job failure does not imply
  that the manifest is absent; always verify the complete set.
- Rerunning the failed **publish job** reuses the successful build artifacts.
  Identical existing assets are retained, missing assets are added, and only
  zero-byte failed-upload `starter` records may be removed. Any differing valid
  asset causes a hard error before uploading anything. There is no `--clobber`.
- Archives use stable ordering, modes and timestamps. The build run URL remains
  in their provenance; its attempt number is omitted from packaged metadata so
  a retry alone does not change package identity. A rebuilt artifact with a
  different runner image, toolchain or binary may still differ and is rejected
  rather than silently replacing a release. Prefer retrying only failed jobs;
  changed source should receive a new version/tag.
- Same-release runs are serialized and cannot be cancelled by a tag push. The
  workflow's normal build jobs have `contents: read`; only the asset job has
  `contents: write`. The `GH_TOKEN` environment variable is set only on the
  publication step; checkout uses the job token without persisting credentials.

The release helpers have offline unit tests with synthetic ELF inputs and a
fake GitHub API, included in `python3 -m unittest discover -s scripts` and
`scripts/ci-check.sh`. They do not
contact GitHub or claim to validate a real arm64 native build. The real native
matrix and actual attachment permissions must still succeed on GitHub.

## GitHub behavior references

- [Release event and published prereleases](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows#release)
- [Workflow permissions](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#permissions)
- [Release asset API and failed-upload starters](https://docs.github.com/en/rest/releases/assets?apiVersion=2022-11-28)
- [Immutable releases](https://docs.github.com/en/code-security/concepts/supply-chain-security/immutable-releases)
- [Re-running workflows and jobs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs)
- [Triggering workflows and token recursion](https://docs.github.com/en/actions/how-tos/writing-workflows/choosing-when-your-workflow-runs/triggering-a-workflow)
