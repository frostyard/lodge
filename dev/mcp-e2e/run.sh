#!/bin/sh
# Runs the MCP gateway's end-to-end tests (docs/specs/lodge-http.md, The
# MCP gateway; ADR-0024) in the nsl machine: the development build on
# loopback with hearth's example app, then 2 MCP clients against
# /gateway/mcp, each through a sign-in, a request_access approval, the tools
# change on the open session, and calls:
#
# - pi-mcp.mjs: pi-mcp, the MCP client of Pi Pocket's mcp module;
# - inspector.mjs: MCP Inspector's connection CLI, mcpdo, with tap.mjs in
#   its daemon to read the notifications its MCP client receives.
#
# Both are pinned in package.json and pnpm-lock.yaml.
#
#   make e2e-build && make mcp-e2e
set -eu

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
port=${LODGE_PORT:-8490}
cport=${LODGE_CONTENT_PORT:-8491}
origin=http://127.0.0.1:$port
root=$(mktemp -d "${TMPDIR:-/tmp}/lodge-mcp-e2e.XXXXXX")
log=$root/lodge.log

for b in lodge-e2e lodgectl lodge-hearthapp; do
	[ -x "$repo/build/$b" ] || { echo "run.sh: build/$b is missing; run make e2e-build" >&2; exit 1; }
done

e2e=
status=1
# cleanup stops the development build and removes its root, and keeps this
# script's own status: the build's exit by the signal is not a failure.
cleanup() {
	trap - EXIT INT TERM
	if [ -n "$e2e" ]; then
		kill "$e2e" 2>/dev/null || true
		wait "$e2e" 2>/dev/null || true
	fi
	rm -rf "$root"
	exit $status
}
trap cleanup EXIT INT TERM

LODGE_PORT=$port LODGE_CONTENT_PORT=$cport "$repo/dev/e2e.sh" "$root" >"$log" 2>&1 &
e2e=$!
i=0
until grep -q '^lodge (development build)' "$log" 2>/dev/null; do
	i=$((i + 1))
	kill -0 "$e2e" 2>/dev/null && [ $i -le 300 ] || { echo "run.sh: lodge did not start" >&2; cat "$log" >&2; exit 1; }
	sleep 0.1
done
LODGE_ROOT=$root "$repo/build/lodgectl" allow admin e2e@example.com >/dev/null

cd "$here"
pnpm install --frozen-lockfile --silent
status=0
LODGE_ORIGIN=$origin node pi-mcp.mjs || status=1
LODGE_ORIGIN=$origin node inspector.mjs || status=1
if [ $status -ne 0 ]; then
	echo "run.sh: failed; lodge's log:" >&2
	cat "$log" >&2
fi
