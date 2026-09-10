#!/bin/sh
# Build dist/id.aeon.memory.wasm: the one-line guest build that
# 'aiisdk build' runs, kept visible and usable without the CLI.
#
# Each flag is required by the host's worker:
#   -target=wasm-unknown   bare core wasm, no WASI; the worker's import
#                          wall admits the plugin ABI only
#   -scheduler=none        one plugin, one thread, host-driven entry
#   -gc=conservative       a long-lived plugin must not leak; the
#                          collector is non-moving, so pointers handed
#                          to the host stay valid
#   -no-debug              no DWARF in a shipped artifact
set -eu
cd "$(dirname "$0")"

TINYGO="${TINYGO:-}"
if [ -z "$TINYGO" ]; then
  if command -v tinygo >/dev/null 2>&1; then TINYGO=tinygo
  elif [ -x /opt/tinygo/bin/tinygo ]; then TINYGO=/opt/tinygo/bin/tinygo
  else
    echo "build.sh: tinygo not found (set TINYGO=/path/to/tinygo)" >&2
    exit 1
  fi
fi

mkdir -p dist
GOFLAGS=-buildvcs=false "$TINYGO" build -o dist/id.aeon.memory.wasm \
  -target=wasm-unknown -scheduler=none -gc=conservative -no-debug .
ls -l dist/id.aeon.memory.wasm
