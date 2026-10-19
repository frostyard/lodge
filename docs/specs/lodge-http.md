# Spec: lodge's HTTP surface

Every route lodge serves, who may call it, with what credential, what lodge
checks before it proxies, and what it answers. This is the contract that
`internal/tailnet`, `internal/gateway`, `internal/sessions`,
`internal/tokens` and `lodgectl` implement, that browsers, agents and
devices rely on, and that lodge's security review checks. What an app
serves behind these routes is [app-contract.md](app-contract.md). The
messages inside the shell are [lodge-client.md](lodge-client.md) and
[shell.md](shell.md). Rationale:
[ADR-0004](../adr/0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md)
(the nodes),
[ADR-0005](../adr/0005-browsers-sign-in-with-their-tailnet-identity.md)
(sign-in and sessions),
[ADR-0006](../adr/0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)
(agents),
[ADR-0007](../adr/0007-lodge-serves-every-apps-web-build-from-disk.md)
(files and headers),
[ADR-0010](../adr/0010-untrusted-bytes-come-from-a-second-node.md)
(content). Where it fits: [design/overview.md](../design/overview.md).

frostnotes' M4 drafted this for notesd's hub (its `docs/specs/hub-http.md`,
local commit `0586e2b`). It is generalized here to every app, with the
fixes from M4's review.

**Status:** written on 2026-10-18 for the
[roadmap's](../plans/0001-roadmap.md) Phase 2, which binds every route as
it lands.

## Interface

### Listeners

| Listener | Flag | Serves |
| --- | --- | --- |
| the `lodge` node, port 443, TLS with the node's certificate | `--node NAME` (default `lodge`) | the shell, browser, agent and device routes |
| the `lodge-content` node, port 443, TLS with its certificate | `--content-node NAME` (default `lodge-content`) | the content routes |
| plain HTTP on a loopback address | `--listen ADDR` | the `lodge` node's routes. Browser and agent requests are refused, since no peer on it has a tailnet identity (except in the development build) |
| plain HTTP on a loopback address | `--content-listen ADDR` | the content routes, refused likewise |
| `/run/lodge/control.sock` | — | `lodgectl` ([below](#the-control-socket-and-lodgectl)) |
| `/run/lodge/inference.sock` | — | app daemons ([app-contract.md](app-contract.md#inference)) |

- `LODGE` is the `lodge` node's full DNS name, the first of `tsnet`'s
  `CertDomains` (`lodge.<tailnet>.ts.net`). `CONTENT` is the
  `lodge-content` node's.
- Both nodes listen on no port but 443, and never through Funnel.
- Every listener serves **HTTP/1.1 only**: `tsnet`'s `ListenTLS` sets no
  ALPN, and lodge offers none.
- `--listen` and `--content-listen` refuse, at start, an address that is
  not loopback (`127.0.0.0/8`, `::1`).
- Tailscale's log upload is disabled (`logtail.Disable()`) before either
  node starts. The node's state is under `/var/lib/lodge/tailnet/<node>/`.
  First starts read `TS_AUTHKEY`, or `TS_AUTHKEY_CONTENT` for the content
  node when that is set.
- `--root DIR` moves every path of
  [app-contract.md's layout](app-contract.md#install-layout) under `DIR`.

### The development build

A binary built with the `e2e` tag (`make e2e-build`, into
`build/lodge-e2e`). Apps run it to test against lodge on loopback, as
notes' Firefox runs do.

- **It joins no tailnet.** It runs with `--listen 127.0.0.1:PORT`,
  `--content-listen 127.0.0.1:PORT2` and `--root DIR`, and with no
  `--node`.
- **One fixed identity.** Every peer on its loopback listeners is the same
  untagged node:
  - login `e2e@example.com`;
  - display name `E2E`;
  - node name `e2e`, node ID `e2e-node`.

  That identity passes through every check after WhoIs, including the
  allow-lists, so a test allows it with `lodgectl` like any other.
- **The names on loopback:**

  | Production | Development build |
  | --- | --- |
  | `Host`: `LODGE` | `127.0.0.1:PORT` |
  | the lodge origin, `https://LODGE` | `http://127.0.0.1:PORT` |
  | `wss://LODGE` in `connect-src` | `ws://127.0.0.1:PORT` |
  | `Host`: `CONTENT` | `127.0.0.1:PORT2` |
  | the content origin, `https://CONTENT` | `http://127.0.0.1:PORT2` |

  The two loopback origins are cross-origin and on one site, as the two
  nodes are. Every other rule of this spec holds, with these values read in
  place of the production ones.
- **Absent from release builds.** The identity lives in `internal/e2eid`,
  whose every file carries `//go:build e2e`.
  - A test builds `cmd/lodge` without tags, and fails if `go tool nm` lists
    any symbol of `internal/e2eid`.
  - No flag or variable of a release build gives a plain listener an
    identity.

### Routes

On `LODGE`:

| Path | Methods | Kind | Credential | Answer |
| --- | --- | --- | --- | --- |
| `/`, `/<id>`, `/<id>/<route>` | `GET`, `HEAD` | browser | tailnet identity | the shell page |
| `/lodge/<path>` | `GET`, `HEAD` | browser | tailnet identity | the shell's files, and `/lodge/apps` |
| `/app/<id>/api` | `GET` (WebSocket) | browser | tailnet identity, `Origin` | the app's `GET /api`, as a session |
| `/app/<id>/x/<path>` | `GET`, `HEAD` | browser | tailnet identity | the app's `GET /x/<path>` |
| `/app/<id>/<path>` | `GET`, `HEAD` | browser | tailnet identity | the app's `web/` |
| `/mcp/<id>` | `POST`, `GET`, `DELETE` | agent | `Bearer` MCP token | the app's `/mcp` |
| `/device/<id>/<path>` | any | device | the app's own | the app's `/device/<path>` |

On `CONTENT`:

| Path | Methods | Kind | Credential | Answer |
| --- | --- | --- | --- | --- |
| `/<id>/<path>` | `GET`, `HEAD` | content | tailnet identity | the app's `GET /content/<path>` |

- Any other path answers 404. `<id>` is an installed app's ID. A path whose
  `<id>` names no app is 404, and one whose app is disabled is 503 with the
  reason.
- **A route's kind decides its checks, and no check of one kind applies to
  another.**
- Error responses are `text/plain; charset=utf-8`, one line saying why,
  with no detail about other users, nodes, apps or tokens. They carry their
  kind's headers.
- An app that cannot be reached on its socket answers 502.

### Shell routes

- `/`, `/<id>` and `/<id>/<route>` serve the shell's `index.html` for an
  installed `<id>`, enabled or not, and for `/`. The shell reads the path
  and opens the app ([shell.md](shell.md#addresses-and-history)).
- `/lodge/<path>` serves the shell's bundle from
  `/usr/share/lodge/shell/`, by [app files](#app-files)' path rules, with no
  `index.html` fallback. The bundle includes the web manifest,
  `/lodge/manifest.webmanifest`, and its icons.
- `GET /lodge/apps` answers `Cache-Control: no-store` with the rail's data:

  ```json
  { "contract": 1, "contentOrigin": "https://CONTENT",
    "apps": [ { "id": "notes", "name": "Notes", "icon": "/app/notes/icon.svg",
                "key": "n", "content": true, "companion": false,
                "state": "up", "reason": "" } ] }
  ```

  `state` is `up`, `degraded`, `down` or `disabled`, and `reason` says why
  when it is not `up`.

### Browser checks

Every request on a browser route passes these, in order. A failure is 403,
and a WebSocket request fails before its upgrade.

1. **Host:** `Host` is `LODGE` or `LODGE:443`.
2. **Origin:** an `Origin` header, when present, is exactly
   `https://LODGE`. On a WebSocket upgrade it must be present.
3. **Fetch metadata:** `Sec-Fetch-Site`, when present, is `same-origin` or
   `none`. On a WebSocket upgrade, when present, it is `same-origin`. One
   exception: a top-level navigation to a shell page (`GET`,
   `Sec-Fetch-Mode: navigate`, `Sec-Fetch-Dest: document`) passes from any
   site.
4. **Method:** `GET` or `HEAD`, else 405. `/app/<id>/api` takes `GET` with
   a WebSocket upgrade. A `GET` without one answers as an upgrade would, up
   to the upgrade itself: a failed check with its line, else 426 Upgrade
   Required. A page whose WebSocket fails, which a browser does not tell
   why, learns the reason this way.
5. **Identity:** the [identity checks](#identity).

### Identity

WhoIs on the request's remote address, in the node that accepted it, gives
the peer:

- the node's StableNodeID, its name (the first label of its DNS name) and
  its Tailscale addresses;
- whether it is tagged;
- its owner's login and display name.

A browser or content request is admitted only when every check below
holds. Each failure is its own line:

| # | Check | Failure line |
| --- | --- | --- |
| a | WhoIs names a peer | `lodge cannot name this peer` |
| b | the remote IP is one of that node's own addresses | `forwarded traffic is refused` |
| c | the node is not tagged | `tagged nodes cannot sign in` |
| d | the login is on the allowed logins, compared byte for byte | `login LOGIN is not allowed: lodgectl allow login LOGIN` |
| e | the node's ID is on the allowed nodes | `node NAME (ID) is not allowed: lodgectl allow node ID` |

- **Identity comes from WhoIs alone.** lodge never reads
  `Tailscale-User-*`, `Tailscale-App-Capabilities`, `X-Forwarded-*`,
  `Forwarded` or `X-Real-IP`, nor a cookie, a query parameter or a token,
  for identity.
- **Caching.** A WhoIs answer may be reused for requests on the same TCP
  connection for at most 60 s. Checks d and e run on every request against
  the current lists, so a `lodgectl` change applies to the next request.
- Agent requests need checks a and b only. Device requests need none
  ([device routes](#device-routes)).

### Headers

Every browser response, errors included, carries these:

| Header | Shell routes | App routes (`/app/<id>/…`) |
| --- | --- | --- |
| `Content-Security-Policy` | the shell policy | the app policy |
| `X-Frame-Options` | `DENY` | `SAMEORIGIN` |
| `X-Content-Type-Options` | `nosniff` | `nosniff` |
| `Referrer-Policy` | `no-referrer` | `no-referrer` |
| `Cross-Origin-Opener-Policy` | `same-origin` | `same-origin` |
| `Cross-Origin-Resource-Policy` | `same-origin` | `same-origin` |
| `Permissions-Policy` | `camera=(), microphone=(), geolocation=()` | the same |

**The shell policy:**

```
default-src 'none'; script-src https://LODGE/lodge/; style-src https://LODGE/lodge/ 'unsafe-inline'; img-src 'self' data:; font-src https://LODGE/lodge/ data:; connect-src https://LODGE/lodge/; manifest-src https://LODGE/lodge/; frame-src https://LODGE/app/; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'
```

**The app policy,** for app `<id>`, with the bracketed parts present only
when the manifest sets that flag:

```
default-src 'none'; script-src https://LODGE/app/<id>/; style-src https://LODGE/app/<id>/ 'unsafe-inline'; img-src 'self' data: [content: https://CONTENT] [remoteImages: https:]; media-src 'self' [content: https://CONTENT]; font-src https://LODGE/app/<id>/ data:; connect-src https://LODGE/app/<id>/ wss://LODGE/app/<id>/api; frame-src https://LODGE/app/<id>/ [content: https://CONTENT]; manifest-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'
```

- **Content responses** carry their own set ([content
  routes](#content-routes)).
- **Overrides.** lodge sets these headers, replacing any of the same name
  that an app's response carries. The one exception is
  `Content-Security-Policy`: an app's own is kept beside lodge's, and a
  browser enforces both.
- No browser or content response carries `Set-Cookie` or
  `Access-Control-*`.

### App files

`GET` or `HEAD` `/app/<id>/<path>`, except `api` and `x/…`, from the app's
`web/`:

- `<path>` serves the file at that path under `web/` when every one of these
  holds:
  - the path matches `^(/[A-Za-z0-9_-][A-Za-z0-9._-]*)+$`, so no segment
    starts with `.` and none is empty;
  - no element of it is a symbolic link;
  - it names a regular file.
- `/app/<id>/`, and any other path, serves `web/index.html`, except under
  `/app/<id>/assets/`, where it is 404.
- `Cache-Control`: `private, max-age=31536000, immutable` under `assets/`,
  and `no-cache` for everything else, `index.html` included.
- Content types by extension:

  | Extension | Type |
  | --- | --- |
  | `.html` | `text/html; charset=utf-8` |
  | `.js`, `.mjs` | `text/javascript; charset=utf-8` |
  | `.css` | `text/css; charset=utf-8` |
  | `.json` | `application/json` |
  | `.svg` | `image/svg+xml` |
  | `.png` | `image/png` |
  | `.ico` | `image/x-icon` |
  | `.webp` | `image/webp` |
  | `.woff2` | `font/woff2` |
  | `.woff` | `font/woff` |
  | `.ttf` | `font/ttf` |
  | `.wasm` | `application/wasm` |
  | `.webmanifest` (the shell's only) | `application/manifest+json` |
  | anything else | `application/octet-stream` |

The shell's files under `/lodge/` follow the same rules from
`/usr/share/lodge/shell/`.

### Sessions

`GET /app/<id>/api` with a WebSocket upgrade, after the browser checks:

- lodge opens the app's `GET /api` upgrade over the app's socket, with the
  session's headers. It upgrades the browser's side only after the app's
  side is up, else it answers 502.
- **The session** has:
  - an ID of 16 lowercase hex digits from `crypto/rand`;
  - its app;
  - the peer (login, display name, node ID, node name, remote address);
  - when it started, and `seenAt`, the time of the browser's last frame.

  lodge keeps it in memory only.
- **Frames** are relayed one to one, and lodge never parses them. Text
  frames only: a binary frame closes the session (1003). A frame larger
  than 16 MiB closes it (1009). When either side closes, lodge closes the
  other.
- **Liveness.** A session with no frame from the browser for 90 s is
  closed (1001). lodge sends no pings. The app's frontend sends a message
  at least every 30 s while visible
  ([app-contract.md](app-contract.md#sessions)).
- **Re-checks.** Every 60 s lodge asks WhoIs again for each session's
  remote address, and re-runs the identity checks. It closes (1008) a
  session whose peer has changed or no longer passes.
  - `lodgectl disallow login` and `disallow node` close the sessions they
    affect at once.
  - `lodgectl session end ID` closes one.
- **Limits.**
  - A node may hold at most 8 sessions per app. A new session past that
    first closes (1008) that node's session of the app with the oldest
    `seenAt`.
  - lodge holds at most 64 in all. Past that, a new session is refused with
    429 before the upgrade.

### App reads, `/app/<id>/x/`

`GET` or `HEAD` `/app/<id>/x/<path>`, after the browser checks, proxies
to the app's `GET /x/<path>` with the query. It carries caller `session`
and the identity headers, without `Lodge-Session`. The response takes the
app policy's headers.

### Content routes

On `CONTENT`, every request passes these, in order. A failure is 403
unless it says otherwise.

1. **Host:** `Host` is `CONTENT` or `CONTENT:443`.
2. **Method:** `GET` or `HEAD`, else 405.
3. **Fetch metadata:** `Sec-Fetch-Site`, when present, is `same-site`,
   `same-origin` or `none`. Frames and images from lodge's pages are
   same-site, and a content URL opened in a new tab is `same-site` or
   `none`.
4. **Identity:** the [identity checks](#identity), all of them.
5. **App:** `<id>` names an enabled app with `content: true`, else 404,
   or 503 with the reason when the app is disabled, as on every route.

lodge then proxies to the app's `GET /content/<path>`, with the query and
any `Range`, as caller `content` with the identity headers. To the
response it adds:

| Header | Value |
| --- | --- |
| `Content-Security-Policy` | `default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; sandbox; frame-ancestors https://LODGE` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `no-referrer` |
| `Cross-Origin-Resource-Policy` | `same-site` |
| `Cross-Origin-Opener-Policy` | `same-origin` |

- For an app whose manifest sets `contentLinks`, `sandbox` becomes
  `sandbox allow-popups allow-popups-to-escape-sandbox`, so a link in its
  content opens in a new window, outside the sandbox
  ([app-contract.md](app-contract.md#the-manifest-appjson)).
- The app's `Content-Type`, `Content-Length`, `Content-Range`,
  `Accept-Ranges`, `Content-Disposition`, `Cache-Control`, `ETag` and
  `Last-Modified` pass through, and so does its own
  `Content-Security-Policy`, beside lodge's.
- `Cache-Control` is `private, no-cache` when the app sends none.
- A content URL may be opened as a top-level page in a new tab, and is
  sandboxed there too.
- **PDFs:** no exception is pinned. *Verify review item 12 in Phase 2.* If
  Chrome's viewer refuses a sandboxed PDF, this section gains an exception
  for `application/pdf` that keeps `default-src 'none'` and drops
  `sandbox`. Until then apps offer a download link (`Content-Disposition:
  attachment`).

### `/mcp/<id>`

The checks, in order:

1. **Host:** `Host` is `LODGE` or `LODGE:443`, else 403.
2. **Origin:** a request with an `Origin` header is 403.
3. **Method:** `POST`, `GET` or `DELETE`, else 405.
4. **Peer:** identity checks a and b, else 403. Tags and allow-lists are
   not consulted.
5. **Token:** `Authorization: Bearer <token>` names a live token issued
   for app `<id>`. Otherwise, whether the token is missing, malformed,
   unknown, revoked or for another app, the answer is 401 with
   `WWW-Authenticate: Bearer` and no detail.
6. **App:** an app that is disabled, or has no `mcp: true`, is 503 or 404.

lodge then proxies to the app's `/mcp`:

- It removes `Authorization`, and sets `Lodge-Caller: agent`,
  `Lodge-Token` and `Lodge-Scope`.
- `Mcp-Session-Id` and every other MCP header pass through both ways,
  because sessions are the app's.
- Streams (server-sent events on `GET` and `POST`) are relayed without
  buffering.
- A token's last use is recorded at most once a minute.
- lodge logs each request's node name, token name, method and status.
- **Revoking a token** refuses its next request (401), and at once closes
  every request and stream lodge is proxying for it.

### Device routes

`/device/<id>/<path>`, any method, WebSocket upgrades included:

1. **Host:** `Host` is `LODGE` or `LODGE:443`, else 403.
2. **App:** `<id>` names an enabled app with `device: true`, else 404, or
   503 when disabled.

lodge checks no identity here. The app authenticates the request. When
WhoIs names the peer, lodge adds `Lodge-Node-ID` and `Lodge-Node-Name`,
plus `Lodge-Login` for an untagged node, for display only. It proxies to
the app's `/device/<path>` with the query.

- The device's own headers pass through, `Origin` and `Authorization`
  included, except those [proxying](#proxying) removes.
- Bodies stream both ways. WebSocket frames are relayed as for sessions,
  up to 16 MiB, but with no liveness rule of lodge's.
- The response passes through. lodge adds `X-Content-Type-Options:
  nosniff` and `Cross-Origin-Resource-Policy: same-origin`, and drops
  `Set-Cookie`.

### Proxying

For every kind, before a request goes to an app's socket:

- lodge deletes these headers from the client's request:
  - every `Lodge-*` and `Tailscale-*` header;
  - `X-Forwarded-*`, `Forwarded`, `X-Real-IP` and `Cookie`;
  - hop-by-hop headers;
  - except on device routes, `Authorization` and `Origin`.
- It sets `Host: <id>`, and the caller headers of
  [app-contract.md](app-contract.md#caller-headers).
- The path is the route table's, with the query unchanged.

### Tokens, logins and nodes

| Credential | Form | Kept by lodge |
| --- | --- | --- |
| MCP token | `lgm_` + 43 base64url characters (32 random bytes from `crypto/rand`) | SHA-256, hex, with its name, app, scope, creation, last use and revocation |
| allowed login | a tailnet login, 1 to 256 bytes of UTF-8 without control characters | as given, with when it was added |
| allowed node | a StableNodeID | the ID, the node's name when it was allowed, and when |

- A token's name is 1 to 64 characters from `[A-Za-z0-9@._-]`, unique
  among live tokens. A revoked token's row stays for the log, and its name
  may be reused.
- A token's scope is 1 to 256 visible ASCII characters, which lodge does
  not interpret.
- A token is shown once, when it is issued, and never again: not by
  `lodgectl tokens`, not in a log, not in the state directory.
- `lodgectl allow node NODE` takes a StableNodeID, or a name that matches
  exactly one peer in the `lodge` node's peer list. It stores the ID.
- All of these live in lodge's state directory (`/var/lib/lodge/`, 0700).

### The control socket and `lodgectl`

`/run/lodge/control.sock` (owner `lodge`, 0600) serves JSON-RPC 2.0, one
message per line. It accepts a connection only from uid 0 or lodge's own
uid, so `lodgectl` runs as root (`sudo lodgectl …`). It is the only place
that issues credentials. lodge serves it from its start, before its nodes have
joined the tailnet, so a first start without an auth key can be checked
and allowed while the nodes wait for their login.

| Command | Method | Does |
| --- | --- | --- |
| `lodgectl status` | `lodge.status` | each node's state, and its login URL while it needs a login; every app's state; session counts; and providers' availability (Phase 3) |
| `lodgectl apps` | `lodge.apps` | each manifest, its state and its reason |
| `lodgectl reload` | `lodge.reload` | rereads the manifests, as `SIGHUP` does |
| `lodgectl logins` | `lodge.logins` | the allowed logins |
| `lodgectl allow login LOGIN` | `lodge.allowLogin` | adds one; `conflict` if listed |
| `lodgectl disallow login LOGIN` | `lodge.disallowLogin` | removes it and closes its sessions; `notFound` if absent |
| `lodgectl nodes` | `lodge.nodes` | the allowed nodes |
| `lodgectl allow node NODE` | `lodge.allowNode` | adds one; `conflict` if listed; `notFound` or `invalidParams` for a name that matches no peer or several |
| `lodgectl disallow node NODE` | `lodge.disallowNode` | removes it and closes its sessions |
| `lodgectl tokens` | `lodge.tokens` | the tokens: name, app, scope, creation, last use, revocation |
| `lodgectl token issue NAME --app ID --scope SCOPE` | `lodge.issueToken` | prints the token alone on stdout, and on stderr how to add it to Claude Code; `conflict` for a live name, `notFound` for an app without MCP |
| `lodgectl token revoke NAME` | `lodge.revokeToken` | revokes it and closes its streams; `notFound` if absent |
| `lodgectl sessions` | `lodge.sessions` | live sessions: ID, app, login, node name, start, `seenAt` |
| `lodgectl session end ID` | `lodge.endSession` | closes one; `notFound` if absent |
| `lodgectl usage [--since DATE]` | `lodge.usage` | inference usage per app and alias (Phase 3) |

The hint on stderr is
`claude mcp add --transport http <id> https://LODGE/mcp/<id> --header "Authorization: Bearer <token>"`.
It never includes `--scope project`, which would write the token into a
repository.

## Rules

- lodge serves nothing on a non-loopback address except through its two
  tailnet nodes, and nothing through Funnel.
- No browser route writes, and no request to one reads identity from
  anything but WhoIs.
- Every browser and content check has a test that fails it. Each finding
  of lodge's security review, and of M4's review that lodge took over, has a
  test.
- Forged `Lodge-*`, `Tailscale-*` and forwarding headers change nothing.
  A test sends each.
- Issuing a token and allowing a login or a node happen only on the
  control socket.
- No response, log line or state file holds an MCP token.
- A release build holds no e2e identity code, proved with `go tool nm`.
- Nothing dials Tailscale's log host. A test runs a node against the
  in-process test control server.
- Every limit in this spec has a test at the limit and one past it.

## Derived artifacts

| Artifact | Derivation |
| --- | --- |
| the policies' strings in `internal/gateway` | [Headers](#headers) and [content routes](#content-routes), with `LODGE`, `CONTENT` and `<id>` filled in |
| `cmd/lodgectl`'s commands | [the control socket](#the-control-socket-and-lodgectl) |

## References

- Rationale: [ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md), [ADR-0004](../adr/0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md), [ADR-0005](../adr/0005-browsers-sign-in-with-their-tailnet-identity.md), [ADR-0006](../adr/0006-agents-reach-each-app-through-its-own-mcp-endpoint.md), [ADR-0007](../adr/0007-lodge-serves-every-apps-web-build-from-disk.md), [ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md), [ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md), [ADR-0010](../adr/0010-untrusted-bytes-come-from-a-second-node.md)
- Context: [design/overview.md](../design/overview.md)
- Related: [app-contract.md](app-contract.md), [lodge-client.md](lodge-client.md), [shell.md](shell.md); frostnotes' [lodge-app.md](https://github.com/frostyard/frostnotes/blob/main/docs/specs/lodge-app.md) (notes' side)
- Built in: [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app)
