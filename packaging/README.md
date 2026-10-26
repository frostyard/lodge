# Packaging

How lodge installs on a systemd host, such as Debian 13
([app-contract.md, Install layout](../docs/specs/app-contract.md#install-layout)).

| File | Installs as |
| --- | --- |
| `systemd/lodge.service` | `/usr/lib/systemd/system/lodge.service`: user `lodge`, hardened, no `RuntimeDirectory=` |
| `sysusers.d/lodge.conf` | user and group `lodge`; groups `lodge-apps` (for the inference socket) and `lodge-agents` (for the agent socket), with `lodge` a member of both |
| `tmpfiles.d/lodge.conf` | `/run/lodge` (lodge's: its control, inference and agent sockets), `/run/lodge/apps` (root's), `/var/lib/lodge` (0700) |
| `config.example.json` | `/usr/share/lodge/config.example.json`: the agents on this host ([lodge-http.md](../docs/specs/lodge-http.md#the-agent-socket)), and inference providers and aliases ([inference.md](../docs/specs/inference.md#configuration)), to copy to `/etc/lodge/config.json` |
| `install.sh` | installs the binaries (`lodge` and `lodgectl` in `/usr/bin/`), the shell's bundle at `/usr/share/lodge/shell/`, and the files above |
| `app-template/` | a socket unit, a service and a sysusers.d entry for an app to copy |

With the default `PREFIX=/usr` the install writes only under `/usr`, so a
systemd sysext can carry it ([core ADR-0058](https://github.com/frostyard/core/blob/main/docs/adr/0058-ship-suite-desktop-apps-as-flatpaks-and-services-as-sysexts.md)).
It never creates `/etc/lodge`: that directory, `lodge.env` and
`config.json` belong to the administrator.

Build and install:

1. `make shell-build` (in the nsl machine), then `make dist`, which writes
   `dist/lodge-<version>-linux-amd64.tar.gz` (`DIST_ARCH=arm64` for arm64).
2. On the host: unpack it and run `sudo ./lodge-<version>-linux-amd64/packaging/install.sh`.
   **A host that was installed with `PREFIX=/opt/lodge`** (its unit is in
   `/etc/systemd/system`) keeps that prefix. A unit in `/etc/systemd/system`
   overrides the one in `/usr/lib/systemd/system`, so a default install
   there would restart the old binary. Run:
   `sudo ./lodge-<version>-linux-amd64/packaging/install.sh PREFIX=/opt/lodge`.
   That puts the binaries in `/opt/lodge/bin`, and the units, sysusers.d and
   tmpfiles.d entries under `/etc`.
   From a checkout, `sudo make install` does the same from `build/` and
   `shell/dist/`, and `make install DESTDIR=/tmp/stage` stages it.
3. Make the configuration directory once: `sudo install -d -m 0755
   /etc/lodge`. The first start reads `TS_AUTHKEY` from `/etc/lodge/lodge.env` (root,
   0600; never committed). Without one, `sudo lodgectl status` shows each
   node's login URL. Later starts need no key: remove `TS_AUTHKEY` from
   `/etc/lodge/lodge.env` after the first start. lodge starts without
   `/etc/lodge/lodge.env` and `/etc/lodge/config.json`: a missing
   `config.json` means no agents and no providers.
   `/etc/lodge/config.example.json` from an older install may stay or go.
4. `sudo systemctl enable --now lodge`, then `sudo lodgectl allow login …`
   and `sudo lodgectl allow node …`.
5. An agent on this host, such as Porter: its user joins `lodge-agents`,
   its name goes in `agents` in `/etc/lodge/config.json`, then `sudo
   lodgectl reload`, and its tokens are issued with `sudo lodgectl token
   issue NAME --app ID --scope SCOPE --agent USER`. `sudo lodgectl status`
   shows the agent socket and the agents it admits.

To move a hand-installed host to the `/usr` layout (optional):

1. Check the effective unit: `systemctl cat lodge.service`, with its drop-ins.
2. Keep local settings as a drop-in in `/etc/systemd/system/lodge.service.d/`.
3. Remove the old full unit in `/etc/systemd/system` and the old copies in
   `/etc/sysusers.d` and `/etc/tmpfiles.d`.
4. Install with the default `PREFIX`, then `sudo systemctl daemon-reload`
   and `sudo systemctl restart lodge`.
5. Check `systemctl show -p ExecStart lodge.service` and `sudo lodgectl status`.
6. Only after these checks pass, remove the old binaries. `/usr/local/bin`
   comes before `/usr/bin` on `PATH`, so a stale `lodgectl` there shadows the
   new one.

`internal/packagecheck` stages an install and runs `systemd-analyze verify`
on the units.
