#!/usr/bin/env bash
#
# Cross-compile the two deployable binaries for the VPS.
#
# These commands used to live as comments in the Ansible defaults, to be copied
# into a shell by hand and their output paths written into booth.vars.yml. That
# file is gitignored, so the paths went wherever the shell of the day happened
# to be pointing — in practice a temp directory, which is emptied. The play then
# asserts the binary exists, fails having done nothing, and the only way back is
# to remember the three commands again.
#
# One fixed output directory, named in version control, fixes that: the vars
# file points at it once and never needs revisiting.
#
# Never on the VPS. A Go toolchain on a 2 GiB box shared with the API is a build
# competing for memory with the service it is about to replace.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
out="$root/dist"
mkdir -p "$out"

# First, and not optional. The agent embeds this bundle with //go:embed, and an
# empty directory compiles perfectly well into a booth that serves a blank
# screen — a failure that only shows up on the kiosk, after a deploy.
echo "==> kiosk bundle"
pnpm --filter @bykami/kiosk build

if [ -z "$(ls -A "$root/agent/internal/httpd/dist" 2>/dev/null | grep -v '^\.gitkeep$')" ]; then
  echo "kiosk bundle is empty — the agent would embed a blank UI" >&2
  exit 1
fi

echo "==> agent (linux/amd64)"
(cd "$root/agent" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o "$out/bykami-agent-linux" ./cmd/bykami-agent)

echo "==> api (linux/amd64)"
(cd "$root/api" && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o "$out/bykami-linux" ./cmd/bykami)

echo
ls -lh "$out"
