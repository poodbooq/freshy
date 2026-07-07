#!/usr/bin/env bash
# freshy's own installer — invoked by freshy itself when its tracked
# repository gets a new commit. This file lives in the source repo so
# users can read what their local copy does; AFTER it's been copied
# out to ~/.local/share/freshy/installers/freshy.sh by an
# `freshy add`, freshy invokes THAT copy (which is yours to edit).
#
# This default implementation:
#   1. runs `go build` from the cloned repo,
#   2. drops the binary into the stage dir already populated by freshy
#      (so the existing deploy pipeline finishes the atomic swap into
#      ~/bin/freshy).
#
# The cwd is the cloned repo (freshy sets it). At runtime, freshy
# will additionally copy THIS file out to:
#   ~/.local/share/freshy/installers/freshy.sh
# so users can edit it freely without affecting upstream.

set -euo pipefail

# We expect cwd to be the repo root freshy just pulled.
cd "$(pwd)"

# Sanity check: make sure we ARE inside a freshy checkout. If your
# team forks freshy and forgets to update this check, drop a guard
# here of your own.
test -f go.mod || { echo "go.mod missing in $PWD; not a freshy checkout?" >&2; exit 2; }

# Build. Inject the version from git so `freshy version` is honest.
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
go build \
  -ldflags "-s -w -X main.version=$VERSION" \
  -o ./freshy \
  ./cmd/freshy

# Ensure the binary is executable (Windows checkouts can lose +x).
chmod +x ./freshy

# Verify it actually runs at all.
./freshy version >/dev/null

echo "[freshy installer] built ./freshy ($VERSION) — freshy will atomic-swap it into ~/bin/freshy"
