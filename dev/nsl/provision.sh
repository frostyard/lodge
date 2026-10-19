#!/usr/bin/env bash
# Provision the nsl `lodge` machine (Debian 13) with the Node toolchain that
# client/ and shell/ build and test with. The host never runs node, pnpm or
# cargo (AGENTS.md, Gates); the Go gates run on the host.
#
# Run from the repository root on the host (idempotent; safe to re-run):
#   nsl create lodge --distro debian:13
#   nsl run -m lodge --root bash dev/nsl/provision.sh "$(id -un)"
#
# Downloads are version-pinned and checksum-verified (frostyard/core
# ADR-0023); bump a pin and its checksum together. pnpm comes from corepack,
# pinned with its hash by each package.json's packageManager.
set -euo pipefail

NODE_VERSION=24.21.0
NODE_SHA256=fd8e59d5a511510f6a298afb548f18c7d2b1be404d8b4a27d94fbe49f56cb2d6

TARGET_USER=${1:?usage: provision.sh USER (the nsl account that builds the frontends)}
id "$TARGET_USER" >/dev/null

STATE=/var/tmp/lodge
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

fetch() { # url sha256 dest
	curl -fsSL --retry 3 -o "$3" "$1"
	echo "$2  $3" | sha256sum -c --quiet -
}

export DEBIAN_FRONTEND=noninteractive
apt-get update -q
apt-get install -y -q --no-install-recommends ca-certificates curl git make xz-utils

if [[ "$(/opt/node/bin/node --version 2>/dev/null)" != "v$NODE_VERSION" ]]; then
	fetch "https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-x64.tar.xz" \
		"$NODE_SHA256" "$TMP/node.tar.xz"
	rm -rf "/opt/node-v$NODE_VERSION"
	mkdir -p "/opt/node-v$NODE_VERSION"
	tar -xJf "$TMP/node.tar.xz" -C "/opt/node-v$NODE_VERSION" --strip-components=1
	ln -sfn "/opt/node-v$NODE_VERSION" /opt/node
fi
ln -sf /opt/node/bin/node /opt/node/bin/npm /opt/node/bin/npx /opt/node/bin/corepack /usr/local/bin/
corepack enable --install-directory /usr/local/bin

# The package store stays machine-local (client/pnpm-workspace.yaml):
# virtiofs is slow for it.
install -d -o "$TARGET_USER" "$STATE" "$STATE/pnpm-store"

cat >/etc/profile.d/lodge.sh <<'PROFILE'
# Written by lodge dev/nsl/provision.sh.
export PATH=/opt/node/bin:$PATH
export COREPACK_ENABLE_DOWNLOAD_PROMPT=0
# Host file edits raise no inotify events inside nsl machines; watchers poll.
export NSL=1
PROFILE

echo "lodge machine provisioned: node $NODE_VERSION"
