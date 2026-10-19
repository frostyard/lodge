#!/bin/sh
# Installs lodge on a systemd host (Debian 13 and the like): its binaries,
# the shell's bundle at /usr/share/lodge/shell/, the unit, and sysusers.d
# and tmpfiles.d entries (docs/specs/app-contract.md, Install layout).
#
#   sudo ./install.sh                     from an unpacked `make dist` tree
#   sudo make install                     from a checkout, after make build and make shell-build
#   make install DESTDIR=/tmp/root        staged, without touching the host
#
# Variables (environment or VAR=value arguments):
#   DESTDIR      a staging root; empty installs on this host
#   PREFIX       /usr; BINDIR is PREFIX/bin. With another PREFIX, such as
#                /opt/lodge, units and sysusers/tmpfiles go under /etc.
#   BINDIR, UNITDIR, SYSUSERSDIR, TMPFILESDIR  override one directory
#   ACTIVATE     yes (default, on this host as root): create the user and
#                directories, reload systemd, and restart lodge if it runs
#   BIN_SRC, SHELL_SRC, PACKAGING_SRC  where to read from; by default bin/,
#                shell/ and packaging/ beside this script's tree, or build/,
#                shell/dist/ and packaging/ in a checkout
#
# The install layout's own paths (/usr/share/lodge, /run/lodge,
# /var/lib/lodge, /etc/lodge) are fixed by the app contract; PREFIX moves
# only the binaries.
set -eu

for arg in "$@"; do
	case $arg in
	*=*) export "${arg%%=*}=${arg#*=}" ;;
	-h | --help)
		sed -n '2,/^set -eu/p' "$0" | sed '$d; s/^# \{0,1\}//'
		exit 0
		;;
	*) echo "install.sh: unknown argument $arg" >&2 && exit 2 ;;
	esac
done

here=$(cd "$(dirname "$0")" && pwd)
top=$(cd "$here/.." && pwd)
if [ -d "$top/bin" ]; then
	BIN_SRC=${BIN_SRC:-$top/bin}
	SHELL_SRC=${SHELL_SRC:-$top/shell}
else
	BIN_SRC=${BIN_SRC:-$top/build}
	SHELL_SRC=${SHELL_SRC:-$top/shell/dist}
fi
PACKAGING_SRC=${PACKAGING_SRC:-$here}

DESTDIR=${DESTDIR:-}
PREFIX=${PREFIX:-/usr}
BINDIR=${BINDIR:-$PREFIX/bin}
if [ "$PREFIX" = /usr ]; then
	UNITDIR=${UNITDIR:-/usr/lib/systemd/system}
	SYSUSERSDIR=${SYSUSERSDIR:-/usr/lib/sysusers.d}
	TMPFILESDIR=${TMPFILESDIR:-/usr/lib/tmpfiles.d}
else
	UNITDIR=${UNITDIR:-/etc/systemd/system}
	SYSUSERSDIR=${SYSUSERSDIR:-/etc/sysusers.d}
	TMPFILESDIR=${TMPFILESDIR:-/etc/tmpfiles.d}
fi
if [ -z "$DESTDIR" ] && [ "$(id -u)" = 0 ]; then
	ACTIVATE=${ACTIVATE:-yes}
else
	ACTIVATE=${ACTIVATE:-no}
fi

die() {
	echo "install.sh: $*" >&2
	exit 1
}

for f in "$BIN_SRC/lodge" "$BIN_SRC/lodgectl"; do
	[ -x "$f" ] || die "$f is missing: run make build (or make dist)"
done
[ -f "$SHELL_SRC/index.html" ] || die "$SHELL_SRC/index.html is missing: run make shell-build (in the nsl machine)"
for f in systemd/lodge.service sysusers.d/lodge.conf tmpfiles.d/lodge.conf config.example.json; do
	[ -f "$PACKAGING_SRC/$f" ] || die "$PACKAGING_SRC/$f is missing"
done
case $BINDIR in /*) ;; *) die "BINDIR must be absolute: $BINDIR" ;; esac

echo "install.sh: installing lodge into ${DESTDIR:-/} (binaries in $BINDIR)"

install -d -m 0755 "$DESTDIR$BINDIR" "$DESTDIR$UNITDIR" "$DESTDIR$SYSUSERSDIR" "$DESTDIR$TMPFILESDIR"
for b in lodge lodgectl; do
	# Replace, never write into, a running binary.
	install -m 0755 "$BIN_SRC/$b" "$DESTDIR$BINDIR/.$b.new"
	mv -f "$DESTDIR$BINDIR/.$b.new" "$DESTDIR$BINDIR/$b"
done

sed "s|^ExecStart=/usr/bin/lodge\$|ExecStart=$BINDIR/lodge|" "$PACKAGING_SRC/systemd/lodge.service" >"$DESTDIR$UNITDIR/.lodge.service.new"
chmod 0644 "$DESTDIR$UNITDIR/.lodge.service.new"
mv -f "$DESTDIR$UNITDIR/.lodge.service.new" "$DESTDIR$UNITDIR/lodge.service"
install -m 0644 "$PACKAGING_SRC/sysusers.d/lodge.conf" "$DESTDIR$SYSUSERSDIR/lodge.conf"
install -m 0644 "$PACKAGING_SRC/tmpfiles.d/lodge.conf" "$DESTDIR$TMPFILESDIR/lodge.conf"

# An example configuration, beside the real one, which the owner writes
# (docs/specs/inference.md, Configuration).
install -d -m 0755 "$DESTDIR/etc/lodge"
install -m 0644 "$PACKAGING_SRC/config.example.json" "$DESTDIR/etc/lodge/config.example.json"

# The shell's bundle, root's and read-only to lodge, replaced whole.
share=$DESTDIR/usr/share/lodge
install -d -m 0755 "$share" "$share/apps" "$DESTDIR/etc/lodge"
rm -rf "$share/.shell.new" "$share/.shell.old"
cp -R "$SHELL_SRC" "$share/.shell.new"
find "$share/.shell.new" -type d -exec chmod 0755 {} +
find "$share/.shell.new" -type f -exec chmod 0644 {} +
if [ -d "$share/shell" ]; then mv "$share/shell" "$share/.shell.old"; fi
mv "$share/.shell.new" "$share/shell"
rm -rf "$share/.shell.old"

if [ "$ACTIVATE" = yes ]; then
	systemd-sysusers "$SYSUSERSDIR/lodge.conf"
	systemd-tmpfiles --create "$TMPFILESDIR/lodge.conf"
	systemctl daemon-reload
	if systemctl --quiet is-active lodge.service; then
		systemctl restart lodge.service
		echo "install.sh: restarted lodge"
	fi
fi

cat <<EOF
install.sh: done. Next, on this host:
  1. Put a tagged, pre-approved auth key in /etc/lodge/lodge.env (root, 0600):
       TS_AUTHKEY=tskey-auth-…
     or start without one and log the nodes in from \`sudo lodgectl status\`.
  2. sudo systemctl enable --now lodge
  3. sudo lodgectl allow login <your login>; sudo lodgectl allow node <your device>
  4. Install each app under /usr/share/lodge/apps/<id>/ with its socket
     unit (packaging/app-template/), then: sudo lodgectl reload
  5. For inference, write /etc/lodge/config.json from config.example.json,
     set any provider key with: sudo lodgectl provider key NAME (stdin),
     then: sudo lodgectl reload
EOF
