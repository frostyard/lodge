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
| `nfpm.yaml`, `deb-check.py` | the `.deb` (see Release): `make deb` and `make deb-check` |
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

## Update

This section is for a host that was installed by hand with
`PREFIX=/opt/lodge`. Use the same `PREFIX` every time. The unit in
`/etc/systemd/system` overrides the one in `/usr/lib/systemd/system`. Without
`PREFIX`, the restart runs the old binary.

While lodge is stopped, no browser and no agent reaches any app.

1. Stop lodge: `sudo systemctl stop lodge`.
2. Take a rollback archive. It holds secrets, so keep it root-only.
   Name it with the old version and a UTC time:

   ```sh
   v=$(/opt/lodge/bin/lodge --version | cut -d' ' -f2)
   sudo sh -c "umask 077; tar -czpf /root/lodge-rollback-$v-$(date -u +%Y%m%dT%H%MZ).tar.gz \
     /var/lib/lodge /etc/lodge /opt/lodge/bin/lodge /opt/lodge/bin/lodgectl /etc/systemd/system/lodge.service /usr/share/lodge/shell"
   ```

   Keep the old `make dist` tarball too.
3. Build the new tarball (see Build and install above), unpack it on the
   host, and run
   `sudo ./lodge-<version>-linux-amd64/packaging/install.sh PREFIX=/opt/lodge`.
   The script replaces each binary by rename and the shell's bundle whole. It
   does not change `/etc/lodge`. It restarts lodge only if lodge is active,
   so run `sudo systemctl start lodge` if you stopped it.
4. Check the version: `/opt/lodge/bin/lodge --version` prints
   `lodge <version> (<commit>)`, and `journalctl -u lodge` shows
   `lodge starting` with `version` and `commit`. Then run
   `sudo lodgectl status` and check that the apps are up.

## Roll back

`install.sh` keeps no copy of the old binaries. Use the earlier `make dist`
tarball, a rebuild of the earlier commit, or the rollback archive.

1. Stop lodge: `sudo systemctl stop lodge`.
2. Install the old version in one of these ways:
   - Run `install.sh PREFIX=/opt/lodge` from the earlier tarball or the
     rebuild.
   - Or restore lodge's installed files from the rollback archive. This does not
     touch the state, the configuration or other binaries in `/opt/lodge/bin`:

     ```sh
     sudo rm -rf /usr/share/lodge/shell
     sudo tar -xzpf ARCHIVE -C / opt/lodge/bin/lodge opt/lodge/bin/lodgectl \
       etc/systemd/system/lodge.service usr/share/lodge/shell
     sudo systemctl daemon-reload
     ```
3. Start lodge: `sudo systemctl start lodge`.
4. If lodge does not start and `journalctl -u lodge` shows
   `the database is at version N, newer than this lodge's M`, the new lodge
   migrated `lodge.db`. An older lodge refuses a newer database. Stop lodge,
   then restore the state from the rollback archive:

   ```sh
   sudo rm -rf /var/lib/lodge
   sudo tar -xzpf ARCHIVE -C / var/lib/lodge
   ```

   Start lodge again. The restore loses the tokens, the allow-list changes and the provider
   sign-ins that were made after the archive.
5. Check as in Update.

## Release

`make bump` is the only way to tag a release. On `main`, clean and equal to
`origin/main`, it runs `make check`, then tags the next version with the
pinned `svu` and pushes the tag. The tag's workflow
(`.github/workflows/release.yml`) builds `frostyard-lodge_X.Y.Z_amd64.deb`
with `make deb`, attests it, and attaches it to the GitHub release.

`make deb` (after `make shell-build`) stages `install.sh` into `build/deb/root`
and packs it with nFPM (`nfpm.yaml`, pinned in `mise.toml`). Without
`DEB_VERSION=X.Y.Z` it builds a snapshot,
`dist/frostyard-lodge_0.0.0-<commit>-snapshot_amd64.deb`. `deb-check.py`
(`make deb-check`) reads the archive's exact member names. It fails unless
the control fields are right (`Depends: systemd`, no maintainer scripts), and
the package holds only root-owned, non-writable directories, files and links
under `/usr`, with the binaries (mode 0755), the unit, the sysusers.d and
tmpfiles.d entries, the shell's bundle and the example configuration.
`internal/packagecheck` feeds it bad packages, and each must fail.
CI builds a snapshot on every pull request (job `deb`).

Verify a download:

```sh
gh attestation verify frostyard-lodge_X.Y.Z_amd64.deb --repo frostyard/lodge
```

Snosi picks the `.deb` up later
([core Plan 0010](https://github.com/frostyard/core/blob/main/docs/plans/0010-suite-packaging.md),
Phase 3).

## After the sysexts

This is planned and not built. In core's
[Plan 0010](https://github.com/frostyard/core/blob/main/docs/plans/0010-suite-packaging.md),
Phase 3, lodge ships as the sysext `lodge` for Snosi images (Floe). It does
not ship for this Debian host, which stays hand-installed (Phase 4 is
deferred, 2026-10-23).

On a Snosi image, the planned commands are:

- update: `sudo updex features update --component=lodge`, then reboot;
- roll back: `sudo updex features rollback lodge`, which takes effect at the
  next boot.

A sysext rollback does not undo a database migration. Keep a backup of
`/var/lib/lodge`.
