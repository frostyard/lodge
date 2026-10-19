---
name: new-lodge-app
description: Scaffolds a new lodge app in a new repository, ready to build and test before you add its own features. It has a daemon on lodge's kit that passes the app contract's socket rules, a JSON-RPC API, a web bundle on @frostyard/lodge-client, and the manifest, units and staging lodge installs it with. Use whenever asked to start, create or scaffold a new app (or "first-party app") that runs behind lodge.
---

# Scaffold a new lodge app

The goal is a new app repository that lodge can run as app `<id>`:
- its daemon passes the contract's socket rules (`make check`);
- its web bundle builds for `/app/<id>/` (`make web-build`);
- `make lodge-package` stages it in lodge's install layout.

The scaffold is thin, on purpose. It is drawn from what notes and mail
needed ([core ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)):
the kit and the client do the shared work, and the app adds only its own.
The contract it follows is
[app-contract.md](../../../docs/specs/app-contract.md).

## Steps

1. **Choose the app's names.** Each one follows the manifest's rules
   ([app-contract.md, The manifest](../../../docs/specs/app-contract.md#the-manifest-appjson)):
   - **the id** (`^[a-z][a-z0-9]{0,15}$`, not `app`, `device`, `lodge` or
     `mcp`). It is the URL segment, and the daemon is `<id>d`;
   - **a name** for the rail, of 1 to 32 characters;
   - **its own system user**, by default `frost<id>`. It is never `root` or
     `lodge`, and is unique across apps;
   - **optionally, a switcher key**: one letter that no installed app uses.
2. **Render the scaffold** into a new, empty directory, outside this
   repository:

   ```sh
   .agents/skills/new-lodge-app/render.sh ../frosttasks tasks "Tasks" frosttasks t github.com/frostyard/frosttasks
   ```

   The arguments are `DEST ID NAME [USER] [KEY] [MODULE] [LODGE_REF]`.
   `LODGE_REF` is the lodge commit the web bundle pins `lodge-client` at.
   render.sh validates every value and refuses a directory that is not
   empty.
3. **Pin the kit and test.** In the new directory:

   ```sh
   go get github.com/frostyard/lodge/kit@<pushed lodge commit>
   go mod tidy
   make check
   ```

   `cmd/<id>d/main_test.go` runs `kittest.CheckRules` against the daemon
   with the manifest it installs. It also runs a session's `/api` round trip
   and the private socket. All three must pass before anything else
   changes. Apps pin the kit and the client to a published lodge commit,
   never a local path.
4. **Build the web bundle** in an nsl machine with Node, never on the host:
   `make web-build` (the `lodge` machine, or `NSL_MACHINE=…`). Commit
   `web/pnpm-lock.yaml` after the first build. From then on the build
   installs with `--frozen-lockfile`.
5. **Stage it for lodge:** `make lodge-package` puts the daemon, the
   manifest, the web build, the units and the sysusers.d entry under
   `build/lodge-root`. `LODGE_DESTDIR=DIR` stages into a lodge root that
   other apps share, and replaces only this app's own files. lodge's
   development build runs it from there (`--root`, `LODGE_ROOT`).
6. **Grow the app.**
   - **Methods** go into `schema/rpc` and into `api/rpc.go`, which follows
     the schema by hand. To generate it instead, copy a first app's
     `tools/rpcgen` (frostmail's caller semantics are the simpler), point
     it at `schema/rpc`, and delete `api/rpc.go`
     ([ADR-0012](../../../docs/adr/0012-the-kit-shares-the-rpc-runtime-not-the-generator.md)).
   - **lodge's other routes** come from fields of `lodgeapp.Server`:
     `Content`, `MCP` (wrapped in `kit/mcpsession`) or `Device`. Turn on
     each one's manifest flag in `packaging/lodge/app.json`, and
     `CheckRules` checks the two agree.
   - **Inference** is `kit/inference`.
7. **Verify:** `make check` passes in the new repository, and so does lodge's
   `make check` here, whose `internal/scaffoldcheck` renders this scaffold
   and runs the same rules.

## Pitfalls

- **An app that listens on the network** breaks Rule 1. The daemon serves
  only Unix sockets: lodge's app socket, and with `--socket` a private one
  for its own clients.
- **Checking `Origin` on `/api`.** lodge has checked it, and the kit's
  server does not repeat it. Don't add a check.
- **A frontend without its keepalive.** lodge closes a session after 90 s
  without a frame from the browser. The bundle's `main.ts` pings every
  30 s while the page is visible. Keep that when you replace the page.
- **`history.pushState` in the frame.** An app reports routes with
  `lodge.route` and follows `navigate`
  ([lodge-client.md](../../../docs/specs/lodge-client.md#routes)).
- **Binding the switcher's shortcut.** Ctrl+\` belongs to the shell, and
  lodge-client catches it before the app does.
- **Staging with `rm -rf` on a whole root.** `stage.sh` replaces only
  `usr/share/lodge/apps/<id>`, and refuses `/`. Keep it that way when you
  add files.
- **Running pnpm, node or cargo on the host.** These run in the nsl machine
  only.
