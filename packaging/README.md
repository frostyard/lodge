# Packaging

How lodge installs on a systemd host, such as Debian 13
([app-contract.md, Install layout](../docs/specs/app-contract.md#install-layout)).

| File | Installs as |
| --- | --- |
| `systemd/lodge.service` | `/usr/lib/systemd/system/lodge.service`: user `lodge`, hardened, no `RuntimeDirectory=` |
| `sysusers.d/lodge.conf` | user and group `lodge`; groups `lodge-apps` (for the inference socket) and `lodge-agents` (for the agent socket), with `lodge` a member of both |
| `tmpfiles.d/lodge.conf` | `/run/lodge` (lodge's: its control, inference and agent sockets), `/run/lodge/apps` (root's), `/var/lib/lodge` (0700) |
| `config.example.json` | `/etc/lodge/config.example.json`: the agents on this host ([lodge-http.md](../docs/specs/lodge-http.md#the-agent-socket)), and inference providers and aliases ([inference.md](../docs/specs/inference.md#configuration)), to copy to `/etc/lodge/config.json` |
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
   node's login URL. Later starts need no key: remove `TS_AUTHKEY` from
   `/etc/lodge/lodge.env` after the first start.
4. `sudo systemctl enable --now lodge`, then `sudo lodgectl allow login …`
   and `sudo lodgectl allow node …`.
5. An agent on this host, such as Porter: its user joins `lodge-agents`,
   its name goes in `agents` in `/etc/lodge/config.json`, then `sudo
   lodgectl reload`, and its tokens are issued with `sudo lodgectl token
   issue NAME --app ID --scope SCOPE --agent USER`. `sudo lodgectl status`
   shows the agent socket and the agents it admits.

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
     /var/lib/lodge /etc/lodge /opt/lodge/bin /etc/systemd/system/lodge.service /usr/share/lodge/shell"
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
   - Or restore the installed files from the rollback archive. This does not
     touch the state or the configuration:

     ```sh
     sudo rm -rf /usr/share/lodge/shell
     sudo tar -xzpf ARCHIVE -C / --exclude=var/lib/lodge --exclude=etc/lodge
     sudo systemctl daemon-reload
     ```
3. Start lodge: `sudo systemctl start lodge`.
4. If lodge does not start and `journalctl -u lodge` shows
   `the database is at version N, newer than this lodge's M`, the new lodge
   migrated `lodge.db`. An older lodge refuses a newer database. Stop lodge,
   restore `/var/lib/lodge` from the rollback archive, and start lodge again.
   The restore loses the tokens, the allow-list changes and the provider
   sign-ins that were made after the archive.
5. Check as in Update.

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
