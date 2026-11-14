# Install lodge

lodge runs on 1 systemd host. The apps it serves share that host, because
they meet lodge on Unix sockets. The install writes only under `/usr`. The
configuration in `/etc/lodge` and the state in `/var/lib/lodge` belong to
the administrator.

## On a Frostyard image

Enable the lodge system extension. The
[lodge system extension page](https://frostyard.org/docs/sysexts/lodge/) on
frostyard.org has the steps: the configuration to make first, the enable
command, and the checks.

## On another systemd host

On another systemd host, such as Debian 13, install lodge from a tarball:

1. In a checkout, run `make shell-build` (in the nsl machine), then
   `make dist`. It writes `dist/lodge-<version>-linux-amd64.tar.gz`.
2. On the host, unpack the tarball and run
   `sudo ./lodge-<version>-linux-amd64/packaging/install.sh`.

The
[packaging README](https://github.com/frostyard/lodge/blob/main/packaging/README.md)
has the full steps, with the case of a host that was installed with
`PREFIX=/opt/lodge`.

## First start

1. Make the configuration directory once:

    ```sh
    sudo install -d -m 0755 /etc/lodge
    ```

2. Put a Tailscale auth key as `TS_AUTHKEY` in `/etc/lodge/lodge.env`,
   owned by root, mode `0600`. Without a key, `sudo lodgectl status` shows
   each node's login URL. Remove `TS_AUTHKEY` from the file after the first
   start: later starts need no key.
3. Start lodge and check it:

    ```sh
    sudo systemctl enable --now lodge
    sudo lodgectl status
    ```

    `lodgectl status` shows lodge's nodes, apps, sessions, providers and
    agent socket.

lodge starts without `/etc/lodge/lodge.env` and without
`/etc/lodge/config.json`. A missing `config.json` means no agents and no
providers. [Configure lodge](../guides/configure.md) adds them.

## Allow a login and a device

A browser gets in only when its tailnet login is on the allowed logins and
its device is on the allowed nodes. A tagged device cannot sign in.

```sh
sudo lodgectl allow login alice@example.com
sudo lodgectl allow node phone
```

`allow node` takes a node's ID, or a name that matches exactly 1 peer. Then
open `https://LODGE` (`lodge.<tailnet>.ts.net`) in a browser on that device.

## Update and roll back

!!! warning "Back up first"

    lodge migrates its database at start, and an older lodge refuses a newer
    database. Back up `/var/lib/lodge` before you install a newer build. A
    rollback does not undo a migration.

- On a Frostyard image, Updex updates and rolls back the system extension.
  The [lodge system extension page](https://frostyard.org/docs/sysexts/lodge/)
  has the steps, with the backup.
- On a host installed by hand, follow
  [Update](https://github.com/frostyard/lodge/blob/main/packaging/README.md#update)
  and
  [Roll back](https://github.com/frostyard/lodge/blob/main/packaging/README.md#roll-back)
  in the packaging README.

To check a release download, run
`gh attestation verify frostyard-lodge_X.Y.Z_amd64.deb --repo frostyard/lodge`.
