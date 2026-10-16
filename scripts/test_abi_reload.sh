#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 || ! -f "$1" ]]; then
  echo "Usage: $0 /path/to/built-plugin.so" >&2
  exit 2
fi
case "$(uname -s)" in
  Linux) ;;
  *) echo "The shared-library reload regression requires Linux." >&2; exit 2 ;;
esac

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
library="$(cd -- "$(dirname -- "$1")" && pwd)/$(basename -- "$1")"
work="$(mktemp -d "${TMPDIR:-/tmp}/cmdcode-abi-reload.XXXXXX")"
trap 'rm -rf -- "$work"' EXIT
"${CC:-cc}" -std=c11 -Wall -Wextra -Werror -O2 \
  "$script_dir/test_abi_reload.c" -ldl -o "$work/test_abi_reload"

# Isolate all credential fallbacks even though this test performs no execution.
mkdir -p "$work/home" "$work/config"
env -u COMMANDCODE_KEY -u COMMANDCODE_API_KEY -u COMMAND_CODE_API_KEY \
  HOME="$work/home" COMMANDCODE_CONFIG_DIR="$work/config" \
  timeout 30s "$work/test_abi_reload" "$library"
