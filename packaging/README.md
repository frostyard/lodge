# Packaging

How lodge installs on a systemd host, such as Debian 13
([app-contract.md, Install layout](../docs/specs/app-contract.md#install-layout)).

| File | Installs as |
| --- | --- |
| `systemd/lodge.service` | `/usr/lib/systemd/system/lodge.service`: user `lodge`, hardened, no `RuntimeDirectory=` |
| `sysusers.d/lodge.conf` | user and group `lodge`, group `lodge-apps` |
| `tmpfiles.d/lodge.conf` | `/run/lodge` (lodge's), `/run/lodge/apps` (root's), `/var/lib/lodge` (0700) |
| `install.sh` | installs the binaries, the shell's bundle at `/usr/share/lodge/shell/`, and the files above |
| `app-template/` | a socket unit, a service and a sysusers.d entry for an app to copy |

Build and install:

1. `make shell-build` (in the nsl machine), then `make dist`, which writes
   `dist/lodge-<version>-linux-amd64.tar.gz` (`DIST_ARCH=arm64` for arm64).
2. On the host: unpack it and run `sudo ./lodge-<version>-linux-amd64/packaging/install.sh`.
   `PREFIX=/opt/lodge` puts the binaries in `/opt/lodge/bin`, and the
   units, sysusers.d and tmpfiles.d entries under `/etc`.
   From a checkout, `sudo make install` does the same from `build/` and
   `shell/dist/`, and `make install DESTDIR=/tmp/stage` stages it.
3. The first start reads `TS_AUTHKEY` from `/etc/lodge/lodge.env` (root,
   0600; never committed). Without one, `sudo lodgectl status` shows each
   node's login URL.
4. `sudo systemctl enable --now lodge`, then `sudo lodgectl allow login …`
   and `sudo lodgectl allow node …`.

`internal/packagecheck` stages an install and runs `systemd-analyze verify`
on the units.
