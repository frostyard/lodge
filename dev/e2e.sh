#!/bin/sh
# Runs lodge's development build on loopback for a smoke test
# (docs/specs/lodge-http.md, The development build): a layout root, the
# shell, the e2e identity allowed, and three fake apps unless FAKE_APPS=0:
# one, two, and helper, which is the companion (Ctrl+Shift+`).
#
#   make e2e-build && dev/e2e.sh [ROOT]
#
# Then open http://127.0.0.1:8480/ in a browser on this machine. An app
# under test installs its manifest and bundle under ROOT/usr/share/lodge/
# apps/<id>/, runs its daemon with LODGE_ROOT=ROOT, and then
# `LODGE_ROOT=ROOT build/lodgectl reload` picks it up.
set -eu

repo=$(cd "$(dirname "$0")/.." && pwd)
build=$repo/build
root=${1:-$(mktemp -d "${TMPDIR:-/tmp}/lodge-e2e.XXXXXX")}
port=${LODGE_PORT:-8480}
cport=${LODGE_CONTENT_PORT:-8481}

for b in lodge-e2e lodgectl lodge-fakeapp; do
	[ -x "$build/$b" ] || { echo "e2e.sh: $build/$b is missing; run make e2e-build" >&2; exit 1; }
done

shell=$root/usr/share/lodge/shell
mkdir -p "$shell" "$root/usr/share/lodge/apps"
install -d -m 0755 "$root/run/lodge" "$root/run/lodge/apps"
if [ -f "$repo/shell/dist/index.html" ]; then
	cp -R "$repo/shell/dist/." "$shell/"
elif [ ! -f "$shell/index.html" ]; then
	echo "e2e.sh: shell/dist is not built (make shell-build, in the nsl machine); using a placeholder" >&2
	printf '%s\n' '<!doctype html><title>Lodge</title><p>The shell is not built. Open an app at /app/&lt;id&gt;/.</p>' >"$shell/index.html"
fi

pids=
cleanup() { [ -n "$pids" ] && kill $pids 2>/dev/null || true; }
trap cleanup EXIT INT TERM

"$build/lodge-e2e" --root "$root" --listen "127.0.0.1:$port" --content-listen "127.0.0.1:$cport" &
lodge=$!
pids=$lodge

i=0
until [ -S "$root/run/lodge/control.sock" ]; do
	i=$((i + 1))
	[ $i -le 100 ] || { echo "e2e.sh: lodge did not start" >&2; exit 1; }
	kill -0 $lodge 2>/dev/null || { echo "e2e.sh: lodge exited" >&2; exit 1; }
	sleep 0.1
done
ctl() { LODGE_ROOT=$root "$build/lodgectl" "$@"; }
ctl allow login e2e@example.com >/dev/null 2>&1 || true
ctl allow node e2e-node >/dev/null 2>&1 || true

if [ "${FAKE_APPS:-1}" = 1 ]; then
	LODGE_ROOT=$root "$build/lodge-fakeapp" --id one --name One --key o &
	pids="$pids $!"
	LODGE_ROOT=$root "$build/lodge-fakeapp" --id two --name Two --key t --content-links &
	pids="$pids $!"
	LODGE_ROOT=$root "$build/lodge-fakeapp" --id helper --name Helper --key h --content=false --companion &
	pids="$pids $!"
	i=0
	until [ -S "$root/run/lodge/apps/one.sock" ] && [ -S "$root/run/lodge/apps/two.sock" ] &&
		[ -S "$root/run/lodge/apps/helper.sock" ]; do
		i=$((i + 1))
		[ $i -le 100 ] || { echo "e2e.sh: the fake apps did not start" >&2; exit 1; }
		sleep 0.1
	done
fi
ctl reload >/dev/null

echo "lodge (development build): http://127.0.0.1:$port/   content: http://127.0.0.1:$cport/"
echo "root: $root   lodgectl: LODGE_ROOT=$root $build/lodgectl status"
wait $lodge
