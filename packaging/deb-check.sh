#!/bin/sh
# Checks a built .deb (frostyard/core ADR-0011, ADR-0058):
#   deb-check.sh FILE PACKAGE VERSION
# It fails unless the metadata is right, every path is under ./usr/, every
# entry is root/root and none is group- or world-writable, and the payload
# is there. It prints dpkg-deb -c, so that CI logs show it.
set -eu

[ $# -eq 3 ] || { echo "usage: deb-check.sh FILE PACKAGE VERSION" >&2; exit 2; }
deb=$1 pkg=$2 version=$3

fail() {
	echo "deb-check: $deb: $*" >&2
	exit 1
}

[ -f "$deb" ] || fail "is missing"
[ "$(dpkg-deb -f "$deb" Package)" = "$pkg" ] || fail "Package is not $pkg"
[ "$(dpkg-deb -f "$deb" Version)" = "$version" ] || fail "Version is not $version"
[ "$(dpkg-deb -f "$deb" Architecture)" = amd64 ] || fail "Architecture is not amd64"

list=$(dpkg-deb -c "$deb")
echo "$list"

# A line is: perms owner/group size date time path [-> target].
echo "$list" | awk -v deb="$deb" '
	{
		path = $6
		if (path != "./usr/" && index(path, "./usr/") != 1) { print "deb-check: " deb ": path outside ./usr/: " path > "/dev/stderr"; bad = 1 }
		if ($2 != "root/root") { print "deb-check: " deb ": not root/root: " path > "/dev/stderr"; bad = 1 }
		if ($1 !~ /^l/ && substr($1, 6, 1) == "w") { print "deb-check: " deb ": group-writable: " path > "/dev/stderr"; bad = 1 }
		if ($1 !~ /^l/ && substr($1, 9, 1) == "w") { print "deb-check: " deb ": world-writable: " path > "/dev/stderr"; bad = 1 }
	}
	END { exit bad }
' || exit 1

for p in \
	./usr/bin/lodge \
	./usr/bin/lodgectl \
	./usr/lib/systemd/system/lodge.service \
	./usr/lib/sysusers.d/lodge.conf \
	./usr/lib/tmpfiles.d/lodge.conf \
	./usr/share/lodge/shell/index.html \
	./usr/share/lodge/config.example.json; do
	echo "$list" | awk -v p="$p" '$6 == p { found = 1 } END { exit !found }' || fail "$p is missing"
done
echo "deb-check: $deb is ok"
