#!/usr/bin/env bash
# The only dependency-download phase. No real provider services are contacted.
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
: "${GO:=go}"
export GOTOOLCHAIN=local
"$GO" -C go mod download
"$GO" -C go mod verify
if [[ -z "${ACTIONLINT:-}" ]]; then
  # Keep this pinned tool separate from arbitrary preinstalled runner versions.
  mkdir -p build/ci-tools
  ACTIONLINT="$PWD/build/ci-tools/actionlint"
  if [[ ! -x "$ACTIONLINT" ]]; then
    GOFLAGS= GOBIN="$PWD/build/ci-tools" "$GO" install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
  fi
fi
VERSION="$("$ACTIONLINT" -version)"
[[ "${VERSION%%$'\n'*}" == v1.7.12 ]]
