# 0007 — lodge serves every app's web build from disk

- **Status:** Proposed
- **Date:** 2026-10-18

## Context

Every app has a web frontend. For notes and mail, it is the desktop app's
React UI, built for the browser. lodge loads each frontend in a frame of
its shell ([ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md)), on
one origin. Whatever serves those files must apply the browser checks of
[ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md). Its
headers, the content security policy above all, decide what a page may do.

frostnotes' M4 drafted this for notesd's hub (its draft ADR-0029, local
commit `0586e2b`): notesd served `vite build`'s output from a directory,
with a strict policy, and checked the build's protocol at start. lodge now
serves every app ([ADR-0003](0003-lodge-is-the-only-process-on-the-network.md)).

The user decided that lodge serves every app's bundle from root-owned
directories, and that no daemon serves code. A daemon is the part of an
app that handles untrusted input (mail from strangers, notes synced from
devices). If it could serve the page's script, a bug in it would become
script on the lodge origin.

Frontends build in nsl machines. The hosts that run Go gates never run
Node.

## Decision

- **An app installs its bundle at `/usr/share/lodge/apps/<id>/web/`,**
  owned by root and read-only to everyone else, beside its manifest
  `app.json`. lodge's own shell installs at `/usr/share/lodge/shell/`. lodge
  runs as `lodge` and can read these, but not write them. No app daemon
  writes there.
- **lodge serves `/app/<id>/…` from that directory,** behind ADR-0005's
  checks:
  - a path that names a regular file inside `web/` serves that file. Every
    segment matches `[A-Za-z0-9_-][A-Za-z0-9._-]*`, so there are no hidden
    names and no `..`, and no element is a symbolic link;
  - any other path serves `web/index.html`, so an app's client-side routes
    load. The exception is under `/app/<id>/assets/`, where a missing file
    is 404, so a stale hashed asset fails cleanly;
  - `/app/<id>/api` and `/app/<id>/x/…` are the app's own
    ([specs/app-contract.md](../specs/app-contract.md#public-urls)), never
    files.
- **Hashed files under `assets/` are cached for a year as immutable,** and
  everything else is `no-cache`. Content types come from the extension, and
  every response carries `nosniff`.
- **lodge reads the manifests at start and on reload** (`lodgectl reload`
  or `SIGHUP`). An app is served only when:
  - its manifest is valid;
  - its `contract` major is lodge's;
  - `web/index.html` and its icon exist.

  Otherwise the app is disabled, the rail shows why, and the log says so.
  Files are read from disk per request, so an update to an app's bundle
  needs no lodge restart.
- **lodge sets every header from one function per kind of response.** The
  shell, app frames and content each have their own policy
  ([specs/lodge-http.md](../specs/lodge-http.md#headers)).
  - **App frames.** `default-src 'none'`. Scripts and styles come only from
    the app's own bundle, `/app/<id>/assets/`, so no `x/` read can run as
    script. `connect-src` allows only the app's
    own path and its `wss://` socket. Images come from lodge and `data:`,
    plus the content origin when the manifest sets `content`, and any
    `https:` image when it sets `remoteImages`. `worker-src`, `object-src`,
    `base-uri` and `form-action` are `'none'`. `frame-ancestors` is
    `'self'`.
  - **The other headers:** `X-Content-Type-Options: nosniff`,
    `Referrer-Policy: no-referrer`, `Cross-Origin-Opener-Policy:
    same-origin`, `Cross-Origin-Resource-Policy: same-origin` and a
    `Permissions-Policy` that denies the camera, the microphone and
    geolocation.
  - **What an app sends is overridden.** lodge replaces any of these
    headers an app sends on its `x/` responses, and drops `Set-Cookie` and
    CORS headers. An app's own `Content-Security-Policy` is kept beside
    lodge's. A browser enforces both, so an app can narrow its policy but
    never widen it.
- **Building a bundle is the app's job,** in its own repository and its own
  nsl machine. The bundle is built for the base `/app/<id>/` and includes
  `@frostyard/lodge-client`. A bundle has no web manifest of its own, since
  lodge's single manifest covers every app
  ([ADR-0008](0008-lodge-installs-as-one-home-screen-app.md)).

## Consequences

- No app daemon can put script on the lodge origin, whatever it is fed.
  Only root, through a package install, changes the code a browser runs.
- Go builds and tests never need a bundle. lodge's route tests use a small
  fixture tree under the root override.
- An app's daemon and its bundle are two artifacts a deployment keeps in
  step. The manifest's contract major catches a lodge mismatch. The app's
  own handshake (notes' `rpc.hello`) catches a daemon and bundle mismatch.
- A bundle update takes effect on the next page load. A manifest change
  takes effect on reload.
- Per-app paths in `script-src` and `connect-src` keep each frame to its own
  code and socket. That is not isolation: frames share one origin, so a
  script that runs in one can reach the others
  ([ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md)).

## Alternatives considered

- **Each daemon serving its own bundle:** the daemon, which faces untrusted
  input, would serve code to the lodge origin. Its headers would also have
  to agree with lodge's in every app.
- **Embedding bundles in lodge (`go:embed`):** lodge would need every app's
  build, and every app release would be a lodge release.
- **Bundles under the app's own state directory:** writable by the app's
  user, which reopens the first alternative.
- **A separate static server:** a second place for identity, origin checks
  and headers to agree, outside lodge's node.

## References

- Shapes: [specs/lodge-http.md](../specs/lodge-http.md#app-files), [specs/app-contract.md](../specs/app-contract.md#install-layout), [design/overview.md](../design/overview.md#the-gateway), [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app)
- Builds on: [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md), [ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md); frostnotes' M4 draft ADR-0029 (local commit `0586e2b`)
- Related: [ADR-0008](0008-lodge-installs-as-one-home-screen-app.md), [ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md), [ADR-0010](0010-untrusted-bytes-come-from-a-second-node.md)
