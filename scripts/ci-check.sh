#!/usr/bin/env bash
# One reusable quality gate. Dependency preparation happens before this script.
# Tests use fake transports/loopback servers; no account or live inference calls.
set -euo pipefail
cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."

: "${GO:=go}"
: "${ACTIONLINT:=$PWD/build/ci-tools/actionlint}"
export GO CGO_ENABLED=1 GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
export GOFLAGS='-mod=readonly -trimpath -buildvcs=true'
export PYTHONDONTWRITEBYTECODE=1
# Never accidentally inherit a developer's real provider credentials.
unset COMMANDCODE_KEY COMMANDCODE_API_KEY
TEST_HOME="$(mktemp -d)"
trap 'rm -rf -- "$TEST_HOME"' EXIT
export COMMANDCODE_CONFIG_DIR="$TEST_HOME"
mkdir -p build/ci-logs

ACTIONLINT_VERSION="$("$ACTIONLINT" -version)"
[[ "${ACTIONLINT_VERSION%%$'\n'*}" == v1.7.12 ]]
"$ACTIONLINT" -color=false -shellcheck=
for script in scripts/*.sh; do bash -n "$script"; done
python3 -m unittest discover -s scripts -p 'test_*.py' -v
GOFMT="$("$GO" env GOROOT)/bin/gofmt"
UNFORMATTED="$("$GOFMT" -l go)"
if [[ -n "$UNFORMATTED" ]]; then
  printf 'Go files need formatting:\n%s\n' "$UNFORMATTED" >&2
  exit 1
fi
"$GO" -C go mod verify
"$GO" -C go vet ./...
"$GO" -C go test -count=1 ./...
"$GO" -C go test -race -count=1 ./...
"$GO" -C go run ./cmd/buildmeta > build/ci-logs/plugin-metadata.json
LIBRARY="$("$GO" -C go run ./cmd/buildmeta --field filename)"
[[ "$LIBRARY" =~ ^cmdcode-go-v[0-9]+\.[0-9]+\.[0-9]+\.so$ ]]
"$GO" -C go build -buildmode=c-shared -o "../build/$LIBRARY" .
bash scripts/test_abi_reload.sh "$PWD/build/$LIBRARY"
printf 'Complete offline quality gate passed: %s\n' "$LIBRARY"
