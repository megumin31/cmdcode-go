#!/usr/bin/env bash
# Run the complete offline gate and verify artifact provenance. No real upstream calls.
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
mkdir -p build/ci-logs
exec > >(tee build/ci-logs/build.log) 2>&1
trap 'result=$?; if ((result != 0)); then printf "Build failed (exit %s) at line %s\n" "$result" "$LINENO"; fi' EXIT

: "${GO:=go}"
export GO
: "${TARGET_ARCH:?TARGET_ARCH must be amd64 or arm64}"
: "${GITHUB_SHA:?GITHUB_SHA must identify the checked-out commit}"
: "${RUNNER_LABEL:?RUNNER_LABEL must describe this build host}"
case "$TARGET_ARCH" in amd64|arm64) ;; *) exit 2 ;; esac
[[ "$("$GO" env GOHOSTOS)" == linux ]]
[[ "$("$GO" env GOHOSTARCH)" == "$TARGET_ARCH" ]]
[[ "$("$GO" env GOOS)" == linux && "$("$GO" env GOARCH)" == "$TARGET_ARCH" ]]
[[ "$("$GO" env CGO_ENABLED)" == 1 ]]
[[ "$(git rev-parse HEAD)" == "$GITHUB_SHA" ]]

"$GO" version
gcc -dumpfullversion
"$GO" env -json GOOS GOARCH GOHOSTARCH CGO_ENABLED CC GOFLAGS
# Preparation is the only dependency-download phase. The gate itself is offline.
bash scripts/ci-prepare.sh
bash scripts/ci-check.sh
git diff --exit-code -- go/go.mod go/go.sum

python3 - <<'PY'
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import struct
import subprocess


def output(*args):
    return subprocess.check_output(args, text=True).strip()


def require(condition, message):
    if not condition:
        raise SystemExit(message)


release = json.loads(Path("build/ci-logs/plugin-metadata.json").read_text())
plugin = release["provider"]
version = release["version"]
library = Path("build") / release["filename"]
binary = library.read_bytes()
require(len(binary) >= 20 and binary[:6] == b"\x7fELF\x02\x01", "Expected a 64-bit little-endian ELF")
elf_type, machine = struct.unpack_from("<HH", binary, 16)
require(elf_type == 3, "Expected a shared library")
require(machine == {"amd64": 62, "arm64": 183}[os.environ["TARGET_ARCH"]], "Wrong ELF architecture")

exports = {line.split()[-1] for line in output("nm", "-D", "--defined-only", str(library)).splitlines()}
required = {"cliproxy_plugin_init", "cliproxyPluginCall", "cliproxyPluginFree", "cliproxyPluginShutdown"}
require(required <= exports, f"Missing C ABI exports: {required - exports}")
build_info = output(os.environ["GO"], "version", "-m", str(library)) + "\n"
require(f"vcs.revision={os.environ['GITHUB_SHA']}" in build_info, "Wrong embedded commit")
require("vcs.modified=false" in build_info, "Build must use an unchanged checkout")
elf_report = output("readelf", "-h", "-d", "--version-info", str(library)) + "\n"
glibc_versions = sorted(set(re.findall(r"GLIBC_(\d+(?:\.\d+)+)", elf_report)), key=lambda v: tuple(map(int, v.split("."))))
github_actions = os.environ.get("GITHUB_ACTIONS") == "true"
metadata = {
    "repository": os.environ.get("GITHUB_REPOSITORY"),
    "commit": os.environ["GITHUB_SHA"],
    "tree": output("git", "rev-parse", "HEAD^{tree}"),
    "commit_time": output("git", "show", "-s", "--format=%cI", "HEAD"),
    "plugin": plugin,
    "plugin_version": version,
    "build_tag": release["build_tag"],
    "library": library.name,
    "library_sha256": hashlib.sha256(binary).hexdigest(),
    "os": "linux",
    "architecture": os.environ["TARGET_ARCH"],
    "elf_machine": machine,
    "cgo_enabled": True,
    "go_version": output(os.environ["GO"], "version"),
    "gcc_version": output("gcc", "-dumpfullversion"),
    "build_host_libc": output("getconf", "GNU_LIBC_VERSION"),
    "required_glibc_versions": glibc_versions,
    "runner": os.environ["RUNNER_LABEL"],
    "runner_image_version": os.environ.get("ImageVersion"),
    "run_url": f"{os.environ['GITHUB_SERVER_URL']}/{os.environ['GITHUB_REPOSITORY']}/actions/runs/{os.environ['GITHUB_RUN_ID']}" if github_actions else None,
    "run_attempt": os.environ["GITHUB_RUN_ATTEMPT"] if github_actions else None,
    "verified_c_abi_exports": sorted(required),
    "validation": "Complete offline gate: Go format/vet/tests/race, Python extractor and release fixtures, actionlint, shell syntax, native C ABI same-path reload/repeated shutdown/double-init/config isolation; ELF architecture, C ABI exports, module checksums and exact embedded clean revision. Tests use fake transports or loopback servers; live upstream inference is excluded.",
}
artifact = Path("build/ci-artifact")
if artifact.exists():
    shutil.rmtree(artifact)
artifact.mkdir()
shutil.copy2(library, artifact / library.name)
shutil.copy2("build/ci-logs/plugin-metadata.json", artifact / "plugin-metadata.json")
shutil.copy2("LICENSE", artifact / "LICENSE")
(artifact / "build-info.txt").write_text(build_info)
(artifact / "elf-report.txt").write_text(elf_report)
(artifact / "build-metadata.json").write_text(json.dumps(metadata, indent=2) + "\n")
checksums = "".join(f"{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n" for p in sorted(artifact.iterdir()) if p.is_file() and p.name != "SHA256SUMS")
(artifact / "SHA256SUMS").write_text(checksums)
print(json.dumps(metadata, indent=2))
print(checksums)
if summary_path := os.environ.get("GITHUB_STEP_SUMMARY"):
    with open(summary_path, "a") as summary:
        summary.write(f"Linux **{metadata['architecture']}**: `{library.name}`\n\n")
        summary.write(f"Commit: `{metadata['commit']}`\n\nSHA256: `{metadata['library_sha256']}`\n\n")
        summary.write("Complete offline gate, C ABI reload/lifecycle regression, ELF architecture and C ABI exports verified. This build does not validate live upstream access or Command Code account eligibility.\n")
PY
