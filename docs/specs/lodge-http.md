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
(content),
[ADR-0014](../adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md)
(the agent socket),
[ADR-0022](../adr/0022-lodge-embeds-a-nats-broker-for-app-events.md) (the
broker, Proposed),
[ADR-0023](../adr/0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md)
(OAuth, Proposed),
[ADR-0024](../adr/0024-agents-reach-apps-on-hearth-through-one-mcp-gateway.md)
(the MCP gateway and app calls, Proposed). Where it fits: [design/overview.md](../design/overview.md).

frostnotes' M4 drafted this for notesd's hub (its `docs/specs/hub-http.md`,
local commit `0586e2b`). It is generalized here to every app, with the
fixes from M4's review.

**Status:** written on 2026-10-18 for the
[roadmap's](../plans/0001-roadmap.md) Phase 2, which binds every route as
it lands. [The agent socket](#the-agent-socket) and the binding of tokens
to their route were added on 2026-10-19, and built in the roadmap's
[Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket).
[The broker](#the-broker) was added on 2026-11-15
([ADR-0022](../adr/0022-lodge-embeds-a-nats-broker-for-app-events.md),
Proposed). [The MCP gateway](#the-mcp-gateway) and
[app calls](#app-calls-appidconnect) were added on 2026-11-16
([ADR-0024](../adr/0024-agents-reach-apps-on-hearth-through-one-mcp-gateway.md),
Proposed).

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
| `/run/lodge/agent.sock` | — | `/mcp/<id>` for the agents on lodge's host that its configuration names ([below](#the-agent-socket)) |
| `/run/lodge/nats.sock` | — | the event broker, for app daemons, whose uid the socket admits ([below](#the-broker)) |

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

  The two loopback origins are cross-origin, as the two nodes are. Every
  other rule of this spec holds, with these values read in place of the
  production ones, but one:
- **Content is `Cross-Origin-Resource-Policy: cross-origin`** in the
  development build, in place of `same-site`. In production `LODGE` and
  `CONTENT` share the registrable domain `<tailnet>.ts.net`, so `same-site`
  admits lodge's pages. Chromium does not count two ports of `127.0.0.1` as
  one site, and would refuse every content image an app frame loads
  (`net::ERR_BLOCKED_BY_RESPONSE.NotSameSite`). The wrapper is in
  `internal/e2eid`, so release builds lack it, and a test checks that a
  release build's content keeps `same-site`.
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
| `/app/<id>/connect/<service>/<method>` | `POST` | browser | tailnet identity, `Origin` | for an app on hearth, the app's `POST /connect/<service>/<method>` ([below](#app-calls-appidconnect)) |
| `/app/<id>/<path>` | `GET`, `HEAD` | browser | tailnet identity | the app's `web/` |
| `/mcp/<id>` | `POST`, `GET`, `DELETE` | agent | `Bearer` MCP token | the app's `/mcp`; none for an app on hearth |
| `/gateway/mcp` | `POST`, `GET`, `DELETE` | gateway | `Bearer` OAuth access token with scope `mcp` | lodge's MCP gateway ([below](#the-mcp-gateway)) |
| `/device/<id>/<path>` | any | device | the app's own | the app's `/device/<path>` |
| `/gateway/events`, `/gateway/events/<sub>` | `GET`; `PUT`, `DELETE` | gateway | `Bearer` OAuth access token with scope `events` | the client's event subscriptions ([below](#event-subscriptions)) |
| `/nats` | `GET` (WebSocket) | broker | the token in the `CONNECT` line | the event broker ([below](#the-broker)) |
| `/.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource`, `/.well-known/oauth-protected-resource/gateway/mcp` | `GET`, `HEAD` | OAuth | none | lodge's OAuth metadata ([below](#oauth-routes)) |
| `/oauth/register`, `/oauth/token`, `/oauth/revoke` | `POST` | OAuth | none, or the client's grant | lodge's OAuth server |
| `/oauth/authorize`, `/oauth/consent/<id>`, `/oauth/grants` | `GET`, `HEAD`; `POST` on `/oauth/consent/<id>` | consent | tailnet identity, an admin's login | the consent and grants pages |
| `/oauth/grants/revoke` | `POST` | consent | tailnet identity, an admin's login | revokes a client or a principal, or removes 1 item of a grant |

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
- Only sessions and device routes take a WebSocket upgrade. An upgrade on
  any other route that proxies (`x/`, content, `/mcp`) answers 400 before
  the app sees it.

### Connections

What a peer can hold open, whoever it is, since device routes and the
first checks run before identity is known:

- Each listener holds at most **64 connections from one remote address**
  and **1,024 in all**. A connection past either is closed at once, before
  lodge reads from it.
- A request's header is at most **64 KiB** (431 past it), and must arrive
  within 10 s.
- **A refused request closes its connection:** every 400, 401, 403, 405,
  426 and 429 carries `Connection: close`.
- **A device request's body** may be slow but not stalled: each read of
  it must arrive within **30 s**, or lodge closes the connection and the
  app's read fails.
- lodge holds at most **128 connections to one app's socket** for
  requests that are not sessions; past that, requests wait for one.
- A relayed frame is held whole, up to 16 MiB in each direction
  ([sessions](#sessions)), so 64 sessions may hold about 2 GiB at worst.

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
                "state": "up", "reason": "", "badge": 0 } ] }
  ```

  `state` is `up`, `degraded`, `down` or `disabled`, and `reason` says why
  when it is not `up`. `badge` is the app's unread count from its last
  `/health` answer, 0 when it is not `up` or set none
  ([app-contract.md](app-contract.md#what-an-app-serves-on-its-socket),
  [ADR-0020](../adr/0020-apps-report-an-unread-count-for-the-rail-in-their-health.md)).

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
   a WebSocket upgrade. `/app/<id>/connect/…` of an app on hearth takes
   `POST` only, with its own checks ([app calls](#app-calls-appidconnect)). A `GET` without one answers as an upgrade would, up
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
  ([device routes](#device-routes)). Requests on the agent
  socket need none: the peer's uid stands in for WhoIs
  ([the agent socket](#the-agent-socket)).

### Native clients

A native client on an allowed node, such as frostmail's desktop app through
its bridge, uses the browser routes as the shell does. lodge tells it from
a browser by nothing, and needs no code of its own for it.

- It opens `wss://LODGE/app/<id>/api` with `Origin: https://LODGE`, which
  an upgrade requires, and sends no `Sec-Fetch-*` header, which the
  [browser checks](#browser-checks) check only when present.
- **Identity is still WhoIs alone**, with the node and its login both on
  the allow-lists. Any program on an allowed device that sets lodge's
  `Origin` is signed in, as
  [ADR-0005's consequences](../adr/0005-browsers-sign-in-with-their-tailnet-identity.md#consequences)
  accept.
- It learns the content origin, `contentOrigin`, from `GET /lodge/apps`
  ([shell routes](#shell-routes)), sent with the same `Origin`. Deriving
  `CONTENT` from `LODGE` is only a fallback, since `--content-node` can
  rename it.
- Each of its windows may hold its own session, within the
  [session bounds](#sessions): past a node's 8 per app, lodge closes the
  least recently active, and that window reconnects.

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
when the manifest sets that flag. Scripts and stylesheets come only from
the bundle's `assets/` ([app-contract.md](app-contract.md#install-layout)),
so nothing the app's daemon serves, such as an `x/` read, can run as
script:

```
default-src 'none'; script-src https://LODGE/app/<id>/assets/; style-src https://LODGE/app/<id>/assets/ 'unsafe-inline'; img-src 'self' data: [content: https://CONTENT] [remoteImages: https:]; media-src 'self' [content: https://CONTENT]; font-src https://LODGE/app/<id>/ data:; connect-src https://LODGE/app/<id>/ wss://LODGE/app/<id>/api; frame-src https://LODGE/app/<id>/ [content: https://CONTENT]; manifest-src 'none'; worker-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'self'
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

`GET` or `HEAD` `/app/<id>/<path>`, except `api`, `x/…` and, for an app on
hearth, `connect/…`, from the app's `web/`:

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
  session's headers and the browser's query unchanged
  ([app-contract.md](app-contract.md#public-urls)). It upgrades the browser's side only after the app's
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

### App calls, `/app/<id>/connect/`

An app on hearth (one whose `app.json` names a hearth manifest,
[app-contract.md](app-contract.md#apps-on-hearth)) serves its API with the
Connect protocol. Its frame calls 1 RPC with
`POST /app/<id>/connect/<service>/<method>`
([ADR-0024](../adr/0024-agents-reach-apps-on-hearth-through-one-mcp-gateway.md)).
lodge writes nothing on this route. The checks, in order:

1. **Host:** `Host` is `LODGE` or `LODGE:443`, else 403.
2. **Origin:** an `Origin` header is present and exactly `https://LODGE`,
   else 403.
3. **Fetch metadata:** `Sec-Fetch-Site`, when present, is `same-origin`,
   else 403.
4. **Method:** `POST`, else 405 with `Allow: POST`. An upgrade is 400.
5. **Identity:** every [identity check](#identity), a to e.
6. **App:** an installed app, else 404; enabled, else 503 with the reason.
   For an app without a hearth manifest, `/app/<id>/connect/…` is an
   [app file](#app-files) path, and this list does not apply.
7. **Operation:** the path has exactly 2 segments after `connect`;
   `<service>` matches `^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)+$`,
   `<method>` matches `^[A-Za-z_][A-Za-z0-9_]*$`, and `<service>.<method>`
   is an operation of the app's hearth manifest that is exposed to `ui`.
   Else 404.
8. **Content type:** `Content-Type`, without its parameters, is
   `application/json` or `application/proto`, the Connect protocol's
   unary types, else 415. A form cannot send either.
9. **Principal:** the login matches `^[A-Za-z0-9@._-]{1,64}$` and holds
   `@`, else 403 `login LOGIN cannot be a principal of an app on hearth`.
   An OAuth principal never holds `@` ([oauth.md](oauth.md#names)), so
   the two never meet.

lodge then proxies to the app's `POST /connect/<service>/<method>`, with
the query:

- It removes what [proxying](#proxying) removes, and sets
  `Lodge-Caller: principal`, `Lodge-Principal: <login>` and
  `Lodge-Grant`: an item `{"cap": C}` for each capability `C` of the app's
  hearth manifest, in its order, as JSON in base64url without padding
  ([app-contract.md](app-contract.md#apps-on-hearth)). An item without
  `sel` holds the root path: a signed-in person may do everything in the
  app, as in every app on `/api`.
- The body is at most **4 MiB** (413 past it), and each read of it must
  arrive within 30 s ([connections](#connections)).
- The response passes through with the app policy's headers, as an `x/`
  read's.

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

**Same-site is the whole tailnet.** `ts.net` is on the Public Suffix List,
so the site of `CONTENT` is `<tailnet>.ts.net`, and every node in the
tailnet is same-site to it, a page another node serves included (with
`tailscale serve` or Funnel, from the internet). Such a page, open on an
allowed device, can embed a content URL and learn whether it loads. So an
app MUST name its content by keys no one can guess: a MAC under a secret
the app keeps, as mail's are, never a bare hash of the content, which
anyone holding the same file can compute.

lodge then proxies to the app's `GET /content/<path>`, with the query and
any `Range`, as caller `content` with the identity headers. To the
response it adds:

| Header | Value |
| --- | --- |
| `Content-Security-Policy` | `default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; base-uri 'none'; form-action 'none'; sandbox; frame-ancestors https://LODGE` |
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
- **PDFs:** no exception. A PDF gets the same policy, `sandbox`
  included, as every content response: lodge's policy does not depend on
  the `Content-Type` (`ContentPolicy` and `contentHeaders` in
  [headers.go](../../internal/gateway/headers.go)). Review item 12 is
  answered (2026-11-13,
  [frostnotes' plan 0006](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0006-m4-notes-inside-lodge.md#open-questions)):
  a 1-page PDF served with this section's headers, `sandbox` included,
  opened as a top-level page in Chromium 156 on Linux, and the viewer drew
  it as it does without the headers; only its page thumbnail stayed blank.
  That check ran on loopback, not through lodge, and not in the user's
  Chrome. An app may also offer a download link (`Content-Disposition:
  attachment`), as notes does.

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
   `WWW-Authenticate: Bearer` and no detail. A token issued
   for the agent socket (`--agent`) is refused here the same way.
6. **App:** an app that is disabled, or has no `mcp: true`, is 503 or 404.
   An app whose `app.json` names `hearth` is 404, enabled or not:
   agents reach it through
   [the MCP gateway](#the-mcp-gateway).

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
  every request and stream lodge is proxying for it. lodge knows a stream
  by the token's row, not its name, and checks the secret again once the
  stream is registered, so a new token of the same name changes nothing.
- The response carries the [confined set](#device-routes) of device
  responses, without `Cross-Origin-Resource-Policy`.

### The MCP gateway

`/gateway/mcp` on the `lodge` node is lodge's own MCP server, for the
agents of every principal
([ADR-0024](../adr/0024-agents-reach-apps-on-hearth-through-one-mcp-gateway.md)).
`internal/mcpgw` serves it on the Go MCP SDK. It is 404 when lodge's OAuth
server is off. `gateway` is a reserved app ID. The checks, in order:

1. **Host:** `Host` is `LODGE` or `LODGE:443`, else 403.
2. **Origin:** a request with an `Origin` header is 403.
3. **Method:** `POST`, `GET` or `DELETE`, else 405.
4. **Peer:** identity checks a and b, else 403. Tags and allow-lists are
   not consulted.
5. **Token:** `Authorization: Bearer <token>` is a live OAuth access token
   of a live, bound client of a live principal
   ([oauth.md](oauth.md#lodges-own-callers)). Else, whether the token is
   missing, malformed, unknown, expired, revoked or an `lgm_` token, the
   answer is 401 with no detail and
   `WWW-Authenticate: Bearer resource_metadata="https://LODGE/.well-known/oauth-protected-resource/gateway/mcp"`
   (RFC 9728).
6. **Scope:** the token's scopes and its client's scope both hold `mcp`,
   else 403 with
   `WWW-Authenticate: Bearer error="insufficient_scope", scope="mcp", resource_metadata="…"`.
7. An upgrade is 400.

Then lodge answers with the MCP Streamable HTTP transport, revision
2025-06-18:

- **Sessions** are stateful (`Mcp-Session-Id`). A session is bound to the
  OAuth client whose token opened it: a request for it with another
  client's token, even of the same principal, is 403. A session with no
  request for **1 hour** closes, and a request with its ID is then 404;
  the client opens a new one. A request ends when its token expires, so a
  stream on `GET` ends then too, and the client comes back with a
  refreshed token.
- **The server** is named `lodge`, and declares `tools` with
  `listChanged: true`.
- **The tools of a session** are its principal's, in this order:
  - for each enabled app on hearth, by ID, and each agent operation of its
    hearth manifest, by tool name, whose permission is the `cap` of at
    least 1 item of the principal's grant for that app: a tool named
    `<id>_<tool>`, with the description, `inputSchema` and `outputSchema`
    of the app's tool catalog
    ([app-contract.md](app-contract.md#apps-on-hearth)), and the hints of
    the operation's effect:

    | Effect | `readOnlyHint` | `destructiveHint` | `openWorldHint` |
    | --- | --- | --- | --- |
    | `read` | `true` | `false` | `false` |
    | `write` | `false` | `false` | `false` |
    | `outward` | `false` | `true` | `true` |

  - `request_access`, lodge's own tool.
- **`request_access`** takes `{"app": "<id>", "items": [{"cap": "…",
  "sel": {"<selector>": "<path>"}}]}`, 1 to 32 items, and makes an access
  request for the session's client ([oauth.md](oauth.md#requests)). Its
  description lists each enabled app on hearth: its ID, its name, its
  capabilities and its selectors. Its result is
  `{"request": "<id>", "url": "https://LODGE/oauth/consent/<id>",
  "expires": "<RFC 3339 time>"}`, and 1 text that asks the agent's owner
  to open the URL. A refused request (an app not on hearth, a bad item, 64
  requests pending) is a tool error with the reason.
- **A call of `<id>_<tool>`**, in order. A name that is `<id>_<tool>` of
  an agent operation of an installed app on hearth reaches these steps
  whether or not the session's tool list holds it: the list says what the
  principal may call now, and the call reads the grant again. Any other
  name is the MCP SDK's unknown-tool error.
  1. The app is installed and enabled, with that agent operation, else a
     tool error `app <id> is disabled: <reason>`.
  2. lodge reads the principal's grant for the app again. Without an item
     whose `cap` is the operation's permission, the call is a tool error
     `insufficient_scope: principal <name> holds no <cap> for app <id>:
     call request_access to ask for it`, and the app sees nothing. This is
     not an HTTP 403: the OAuth scope is right, and a new sign-in cannot
     widen a grant.
  3. lodge sends `POST /connect/<service>/<method>` to the app's socket,
     with `Content-Type: application/json`, `Connect-Protocol-Version: 1`,
     `Connect-Timeout-Ms: 60000`, `Lodge-Caller: principal`,
     `Lodge-Principal: <name>` and `Lodge-Grant`: the principal's grant for
     the app, as JSON in base64url without padding
     ([app-contract.md](app-contract.md#apps-on-hearth)). The body is the
     call's arguments, `{}` when it has none.
  4. A `200` answer with a JSON object of at most 4 MiB is the result: the
     object as `structuredContent`, and the same JSON as 1 text content. A
     Connect error `{"code": …, "message": …}` is a tool error: the
     message for `permission_denied`, else `<code>: <message>`. Any other
     answer is a tool error `app <id> answered HTTP <status>`, and no
     answer is `app <id> cannot be reached`.
- **A grant change reaches live sessions.** After an approval or the
  grants page changes a principal's grant, and after a reload changes the
  apps, lodge computes each principal's tools again. Every open session of
  a principal whose tools changed gets `notifications/tools/list_changed`,
  and stays open. The next call reads the new grant.
- **Revoking** a client or a principal ([oauth.md](oauth.md#principals-clients-and-grants))
  ends each request and stream of its clients at once, and their next
  request is 401.
- lodge logs each request's node name, principal, client, method and
  status, and never a token.
- The response carries the [confined set](#device-routes) of device
  responses, without `Cross-Origin-Resource-Policy`.

### The agent socket

`/run/lodge/agent.sock` serves
`/mcp/<id>` to agents on lodge's own host, such as Porter, with the
tailnet's token rules, and the peer's uid in place of `Host`, WhoIs and
the address check.

- **The socket.** lodge makes it at start, beside the control and
  inference sockets, and serves it before its nodes join: owner `lodge`,
  group `lodge-agents`, 0660. lodge is in group `lodge-agents` to give it
  that group. Under `--root` it is `DIR/run/lodge/agent.sock`.
- **The agents.** The `agents` member of `/etc/lodge/config.json` is an
  array of up to 16 system user names (`["frostporter"]`), read at start
  and on reload. `root` and `lodge` are refused. A member that does not
  parse is refused whole, and lodge keeps the list it had (none at start);
  `lodgectl status` says why. A name with no system user is skipped, with
  a log line.
- **The peer check.** On each connection, lodge reads the peer's uid
  (`SO_PEERCRED`), and closes the connection before reading from it unless
  the uid is a listed agent's. Under `--root`, lodge's own uid passes too.
  A refused uid is logged at most once a minute. Removing an agent from
  the list, and reloading, closes its connections.
- **Routes.** `/mcp/<id>` alone. Any other path is 404, and an upgrade is
  400. The checks, in order:
  1. **Origin:** a request with an `Origin` header is 403.
  2. **Method:** `POST`, `GET` or `DELETE`, else 405.
  3. **Token:** `Authorization: Bearer <token>` names a live token issued
     for app `<id>` and for this peer's user (`--agent USER`). Otherwise,
     a tailnet token included, the answer is 401 with
     `WWW-Authenticate: Bearer` and no detail.
  4. **App:** an app that is disabled, or has no `mcp: true`, is 503 or
     404.

  There is no `Host` check, no WhoIs and no address check.
- **Then everything is as on [`/mcp/<id>`](#mcpid):** the headers set and
  removed, sessions passed through, streams relayed without buffering,
  last use recorded, revocation closing streams at once, and the confined
  response headers. The log line names the agent's user in place of a
  node.
- **Connections.** The [limits](#connections) apply to the socket as to a
  listener, counting a peer uid as a remote address.

### The broker

lodge embeds an event broker, a NATS server with JetStream
([ADR-0022](../adr/0022-lodge-embeds-a-nats-broker-for-app-events.md)).
It is **on unless** the `broker` member of `/etc/lodge/config.json` is
`{"enabled": false}`; a missing file, member or `enabled` leaves it on.
lodge reads it at start, and a change takes effect at the next start. A broken `broker` member, or a broker that cannot start,
leaves it off and logs why; the rest of lodge keeps serving. Off, there is
no socket and no store, and `/nats` answers 404. On, it has 2 doors and no
listener of its own.

- **The store.** JetStream keeps `/var/lib/lodge/jetstream/`, made 0700,
  or `DIR/var/lib/lodge/jetstream/` under `--root`.
- **The app socket.** `/run/lodge/nats.sock`, owner `lodge`, group
  `lodge-apps`, 0660, or `DIR/run/lodge/nats.sock` under `--root`, where
  the group is the developer's. A connection is admitted only when its
  peer uid (`SO_PEERCRED`) is an enabled app's manifest `user`; any other
  is closed before lodge reads from it, and its refusal is logged at most
  once a minute per uid. Under `--root` lodge's own uid passes too, and
  the app names itself in the `CONNECT` line's `user` field, which must
  name an enabled app. An app that is no longer enabled loses its
  connections.
- **The client route.** `/nats`, a WebSocket on the `lodge` node, and
  the checks in this order:
  1. **Broker:** off answers 404.
  2. **Host:** `LODGE` or `LODGE:443`, else 403.
  3. **Origin:** a request with an `Origin` header is 403: browsers may
     not call it.
  4. **Method:** `GET`, else 405 with `Allow: GET`.
  5. **Upgrade:** a `GET` without a WebSocket upgrade is 400.
  6. **Peer:** [identity checks](#identity) a and b, as for an agent
     request.
  7. Otherwise lodge upgrades, caps a message at 64 KiB, and hands the
     connection to the broker. The cap is the WebSocket's read limit,
     set after the wrapper that would turn it off, so a message over it
     ends the connection with `1009` (message too big).

  The `CONNECT` line's `auth_token` is an OAuth access token with scope
  `events` ([oauth.md](oauth.md#lodges-own-callers)), and names the
  client. Any other token, or none, is refused with
  `-ERR 'Authorization Violation'` after the server's `INFO` line. The
  client's broker name is `o` and the 16 bytes of its client ID in
  lower-case base32 with no padding (27 characters).
  - **No client is admitted before the restore has finished.** After a
    start lodge refilters every subscription that the store brought back
    from the grant store, and the token check of this route waits for it,
    within 5 s: a grant change commits before its hook runs, so a stop
    between the 2 leaves a stored filter wider than the grant. A client
    the restore cannot read is revoked.
  - **Before the line reaches the server,** lodge checks the client's
    consumers for a [resync](#event-subscriptions), within 2 s; a failure
    is logged, and the connection goes on.
  - **A connection closes when its token expires.** Each minute lodge
    checks every open client connection's token again, and closes it when
    the token no longer names that client.
- **1 protocol line, never a message.** On either door lodge reads the
  connection's first line, which must be `CONNECT` and 1 JSON object (at
  most 8 KiB). A line whose options are no object, `null` included, is
  refused on both doors, with no identity decided: lodge writes the
  one-time secret into those options. It removes `auth_token`, `user`,
  `pass`, `jwt`, `nkey` and `sig`, and writes the line again with a
  one-time secret of 32 random bytes as `auth_token`. The server admits
  only the identity lodge decided from that secret, once, and the secret
  is gone.
- **An account per app and per client,** so that a reply subject cannot
  reach another app's stream:
  - app `<id>`: its subject `<id>.>`, a stream `app_<id>` with the
    subjects `<id>.>` and `lodge.<id>.>` that holds 7 days or 1 GiB,
    whichever comes first, and discards the oldest, and a JetStream
    `PubAck` to each publish. Only lodge publishes `lodge.<id>.>`;
  - client `<name>`: only its own consumers' pull requests and acks,
    `$JS.API.CONSUMER.MSG.NEXT.<stream>.<consumer>` and
    `$JS.ACK.<stream>.<consumer>.>`, with an inbox under
    `_INBOX.<name>.>`. Any other publish or subscribe is a permission
    violation, and a client with no consumer may publish nothing.
- **1 durable pull consumer per client and subscription,** named
  `<client>-<sub>`, each of those names matching `^[a-z0-9_]{1,32}$`,
  with filters inside one app's `<id>.>`: an empty token, a space, or `>`
  before the last token is refused. Its filters are those, and its own
  resync subject `lodge.<id>.resync.<consumer>`. It acknowledges
  explicitly, takes new messages, and a pull expires after at most 60 s
  and takes at most 256. Its metadata names its client, its subscription
  and the topic it asked for.
  **A pulled message keeps its stream subject.** The server sends it to
  the subscription of the pull's reply inbox, but with the event's own
  subject (such as `marks.bookmark.created.folder.pilot`) and the ack
  subject `$JS.ACK.<stream>.<consumer>.…` as its reply. Only a status
  reply (404, 408, 409) comes on the inbox's own subject. So a client
  reads a pull on a subscription of its own and takes what that
  subscription gets, never by the subject: a request mux that finds its
  request by the subject's last token, as nats.js `request` does without
  `noMux`, drops every event and sees only the status replies. The test
  `TestNatsPullKeepsTheStreamSubject` (`internal/gateway`) drives this over
  `/nats` with the protocol's own lines.
  **A subscription stays on 1 app:** subscribing the same client and
  subscription name to another app is refused, with an error that asks
  for an unsubscribe first, and that unsubscribe deletes the consumer it
  has. Subscribing it again to the same app updates its filters. A
  client's subscribe and unsubscribe run 1 at a time, so the check and the
  consumer it makes are 1 step.
- **A subscription change reconnects the client:** its permission names
  its consumers. A change of a subscription's filters alone, by a
  [grant change](#event-subscriptions), does not.
- **Revoking a client closes it first.** lodge takes the client out of
  its index, closes its connections at once, and deletes its consumers
  after that, concurrently and within 1 s: a consumer's deletion can wait
  for the server's own 5 s API timeout, and a revoked client must not
  stay connected meanwhile. A `/nats` WebSocket ends with no close
  handshake, so a peer that answers nothing delays neither the revoke nor
  the other connections. A consumer that is not deleted in time is kept in
  a set that no connection's authentication reads, and the client's next
  revoke tries again. The next start restores it from its metadata, and a
  revoke there finds it, so a revoked client keeps no consumer for good.
- **A subscription waits for a kept cleanup.** A subscribe of that client
  deletes the kept consumers first, and fails while 1 stays, so that no
  ref outlives it to delete a consumer it makes or to orphan one on
  another app.
- **A revoke never waits for a subscription.** Revoking takes no
  per-client lock: it counts an epoch for the client, removes it, closes
  it and deletes its consumers within its 1 s budget, whatever a
  subscription under way is doing. A subscribe that a revoke overtakes
  sees the new epoch, deletes the consumer it made, and fails with an
  error that says the client was revoked during the subscription; if that
  deletion fails, it is kept for the client's next revoke. So no consumer
  is left that nobody deletes.
- **A subscribe waits out a revoke in progress.** While a revoke of that
  client is still deleting, a subscribe of it fails at once with an error
  that says the revoke is in progress, and creates nothing: the revoke's
  own deletion would take the consumer the subscribe would make. A revoke
  ends within 1 s, so asking again then works.
- **Connections.** An app or a client holds at most 64 connections at
  once; past that a connection is refused. The socket's
  [limits](#connections) do not apply to it: the broker counts an
  identity, not a remote address.
- **lodge keeps no message.** It reads no message, indexes no message,
  and logs no message. Its index is which client has which consumers, and
  it is rebuilt from the consumers' metadata at the next start.

### Event subscriptions

`/gateway/events` on the `lodge` node is where a client with scope
`events` subscribes to the broker
([ADR-0022](../adr/0022-lodge-embeds-a-nats-broker-for-app-events.md)).
It is 404 when the broker or lodge's OAuth server is off. The checks, in
order:

1. **Host:** `LODGE` or `LODGE:443`, else 403.
2. **Origin:** a request with an `Origin` header is 403.
3. **Method:** `GET` on `/gateway/events`; `PUT` or `DELETE` on
   `/gateway/events/<sub>`; else 405 with `Allow`.
4. **Peer:** identity checks a and b, else 403.
5. **Token:** `Authorization: Bearer <token>` is a live OAuth access token
   of a live, bound client of a live principal. Else 401 with no detail and
   `WWW-Authenticate: Bearer resource_metadata="https://LODGE/.well-known/oauth-protected-resource"`.
6. **Scope:** a live token without scope `events` is 403, with
   `WWW-Authenticate: Bearer error="insufficient_scope", scope="events", …`.
7. **Upgrade:** a WebSocket upgrade is 400.

| Request | Body | Answer |
| --- | --- | --- |
| `GET /gateway/events` | none | 200, `{"subscriptions": [S, …]}`: the client's own |
| `PUT /gateway/events/<sub>` | `{"topic": "<prefix>"}`, at most 4 KiB, no other member | 200, `S`: made, or its filters updated |
| `DELETE /gateway/events/<sub>` | none | 204: the subscription and its consumer are gone |

`S` is `{"name", "app", "topic", "stream", "consumer", "filters"}`: the
stream and the consumer to pull from on `/nats`, and the filters the
consumer holds, its resync subject included. `GET` reads them from the
consumer itself.

- **`<sub>`** matches `^[a-z0-9_]{1,32}$`, else 400.
- **The topic is a prefix:** 1 to 32 tokens joined by `.`, each `*` or
  a literal of `[A-Za-z0-9_%-]`, with no `>`; the first is the ID of an
  installed, enabled app with a hearth manifest. It takes every subject
  whose first tokens match it. A topic that breaks this is 400, and an
  app that is not one is 404.
- **The effective read prefixes.** For each item of the principal's grant
  for the app, and each event of the app's manifest whose
  `read_permission` is the item's `cap`
  ([oauth.md](oauth.md#the-apps-manifest)):
  - an event with no selector gives its topic alone;
  - an event with a selector gives its topic, with 1 token for each
    segment of the item's path for that selector, and every subject below
    it; every byte of a segment outside `[A-Za-z0-9-]` is `%` and 2
    upper-case hex digits; the root path, or none, adds no token;
  - an item that names another selector at a path other than the root
    gives nothing for that event.

  Each is narrowed to the requested topic, token by token (2 different
  literals give nothing; a literal wins over `*`), and one that another
  covers is dropped. These are hearth's `events.ReadFilters`, and a test
  in `internal/hearthtest` compares the two.
- **A topic the grant gives nothing under is 403.** The consumer is made
  only with at least 1 filter of the grant. A consumer takes at most 64
  filters, its resync subject included: a topic under which the grant
  gives more is 400, and asks for a narrower topic. A grant change that
  would give more leaves the consumer only its resync subject, and logs
  why: lodge never keeps filters that may be wider than the grant.
- **A subscription stays on 1 app:** `PUT` of a `<sub>` that is another
  app's is 409, and so is one during a revoke of the client. An unknown
  `<sub>` on `DELETE` is 404. A broker that fails is 503.
- **lodge reads the grant inside the client's lock,** so a subscription
  and a grant change of the same client run 1 after the other. A new
  subscription is 1 step under that lock: its consumer starts with its
  resync subject alone and is registered; then lodge checks that the
  OAuth client is still there with scope `events`, reads the grant again
  and gives the consumer its filters. A grant change or a revoke that
  comes after that read finds the subscription, so a subscription cannot
  keep a grant the change narrowed. When the check or the second read
  fails, the subscription is deleted before the lock is released. A
  client whose OAuth client is gone gets 404; a lookup that fails for
  another reason gets 503.
- **A grant change refilters.** When a principal's grant for an app
  changes, lodge computes each of its clients' subscriptions on that app
  again and updates the consumer in place. The events not yet delivered
  are filtered again; a grant that gives nothing leaves the consumer only
  its resync subject. An event that was delivered and not acknowledged may
  come again. **A refilter that fails fails closed:** when lodge cannot
  read the grant or the client, or update the consumer, it revokes that
  client in the broker, which closes its connections and deletes its consumers; the
  client subscribes again.
- **A revoke ends delivery.** Revoking a client or a principal revokes
  each of its clients in the broker: connections first, then consumers,
  [as above](#the-broker).
- **Each minute** lodge checks for resyncs, checks the open connections'
  tokens, computes every subscription's filters again, and revokes a
  broker client whose OAuth client is gone or has no scope `events`. The
  resync check of a consumer that a pull moved past a gap finds it from
  the position lodge last recorded for that consumer.
- **Resync.** When a consumer's stream position, or the position lodge
  last recorded for it, is before its stream's first message, retention
  dropped events it never received. lodge then publishes 1 message on its
  resync subject, with `Nats-Msg-Id` its `id`:

  ```json
  {"specversion": "1.0", "id": "<UUIDv7>", "source": "/lodge",
   "type": "org.frostyard.lodge.resync", "subject": "<app>",
   "time": "2026-11-16T12:00:00Z"}
  ```

  The consumer reads on from the stream's first message; the event tells
  it to read the state again through the app's API. lodge publishes no
  second one while one is pending: the check and the publish of a
  consumer run 1 at a time. lodge keeps each consumer's last known stream
  position, and a pull moves the consumer's own position past a gap that
  the server never showed it. So the check is conservative: when the
  stream's first message is past the last known position, lodge publishes
  the resync event at the next check, at the client's connection or
  within the minute, even after a pull passed the gap. The positions are
  kept in a key-value bucket of lodge's own account, `lodge_positions`,
  which no client and no app reaches; a consumer with no record after a
  start is taken as being at position 0. A check that finds no gap moves
  the record up to the consumer's position; a check that finds a gap moves
  it only after the resync event is stored, or when 1 is pending. A consumer near the end of retention can get a
  resync event it did not strictly need.

### OAuth routes

lodge's OAuth server and its consent and grants pages
([oauth.md](oauth.md),
[ADR-0023](../adr/0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md)).
`oauth` is a reserved app ID.

**OAuth** routes (`/.well-known/oauth-…`, `/oauth/register`,
`/oauth/token`, `/oauth/revoke`) are for MCP clients. The checks, in
order:

1. **Host:** `Host` is `LODGE` or `LODGE:443`, else 403.
2. **Origin:** a request with an `Origin` header is 403.
3. **Method:** `GET` or `HEAD` on the metadata, `POST` on the others,
   else 405.
4. **Peer:** identity checks a and b, else 403. Tags and allow-lists are
   not consulted.

lodge then answers itself, with JSON, `Cache-Control: no-store` and the
[confined set](#device-routes) of device responses, without
`Cross-Origin-Resource-Policy`. Errors past these checks are OAuth's JSON
errors ([oauth.md](oauth.md)), not one line.

**Consent** routes are the pages an admin uses. The checks, in order:

1. **Host:** `Host` is `LODGE` or `LODGE:443`.
2. **Origin:** on a `GET` or `HEAD`, an `Origin` header, when present, is
   exactly `https://LODGE`; on a `POST`, it is present and exactly that.
3. **Fetch metadata:** on a `GET` or `HEAD`, `Sec-Fetch-Site`, when
   present, is `same-origin` or `none`, except that a top-level navigation
   (`Sec-Fetch-Mode: navigate`, `Sec-Fetch-Dest: document`) passes from any
   site, as on a shell page. On a `POST`, `Sec-Fetch-Site` is present and
   `same-origin`.
4. **Method:** as the route table says, else 405.
5. **Identity:** every [identity check](#identity), a to e.
6. **Admin:** the login is on the admin list, else `login LOGIN is not an
   admin: lodgectl allow admin LOGIN`.

A failure is 403 unless it says otherwise, with one line. Then a `POST` to
`/oauth/consent/<id>` needs that request's CSRF token and its hash
([oauth.md](oauth.md#the-consent-page)), and a `POST` to
`/oauth/grants/revoke` needs the grants page's CSRF token, which covers the
admin's login and a time ([oauth.md](oauth.md#the-grants-page)). The
status of each refusal is in oauth.md.

Every consent response, errors included, carries:

| Header | Value |
| --- | --- |
| `Content-Security-Policy` | `default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; form-action 'self'[ REDIRECT]; frame-ancestors 'none'; base-uri 'none'` |
| `X-Frame-Options` | `DENY` |
| `X-Content-Type-Options` | `nosniff` |
| `Referrer-Policy` | `same-origin` |
| `Cross-Origin-Opener-Policy` | `same-origin` |
| `Cross-Origin-Resource-Policy` | `same-origin` |
| `Permissions-Policy` | `camera=(), microphone=(), geolocation=()` |
| `Cache-Control` | `no-store` |

`Referrer-Policy` is `same-origin`, not `no-referrer`, because a browser
sends `Origin: null` with a page's `POST` under `no-referrer`, and the
`POST` needs lodge's origin. No referrer leaves for another origin.

`REDIRECT` is the origin of a sign-in's redirect URI, on that request's
consent page only, because a browser checks `form-action` against the
redirect that follows the form.

### Device routes

`/device/<id>/<path>`, any method, WebSocket upgrades included:

1. **Host:** `Host` is `LODGE` or `LODGE:443`, else 403.
2. **App:** `<id>` names an enabled app with `device: true`, else 404, or
   503 when disabled.

lodge checks no identity here. The app authenticates the request. When
WhoIs names the peer, from one of the node's own addresses (check b),
lodge adds `Lodge-Node-ID` and `Lodge-Node-Name`, plus `Lodge-Login` for
an untagged node, for display only. It proxies to the app's
`/device/<path>` with the query.

- The device's own headers pass through, `Origin` and `Authorization`
  included, except those [proxying](#proxying) removes.
- Bodies stream both ways. WebSocket frames are relayed as for sessions,
  up to 16 MiB, but with no liveness rule of lodge's.
- Each read of a request body has 30 s ([connections](#connections)).
- The response passes through. lodge drops `Set-Cookie` and adds a
  confined set, since a browser could load a device response on the lodge
  origin, which every app shares: `Content-Security-Policy: default-src
  'none'; sandbox; frame-ancestors 'none'` (beside the app's own),
  `X-Frame-Options: DENY`, `X-Content-Type-Options: nosniff`,
  `Cross-Origin-Opener-Policy: same-origin` and
  `Cross-Origin-Resource-Policy: same-origin`. Devices ignore them.

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
- **It checks that the socket is the app's.** The socket's file must be
  owned by the manifest's `user`, and its peer (`SO_PEERCRED`) must be
  that user or root: a socket systemd makes and passes to the app reports
  root, which called `listen`, and another app's user can be neither.
  Otherwise lodge sends nothing, and the app is disabled with the reason
  until `lodgectl reload`. An app whose `user` does not exist is disabled.
  Under `--root`, lodge's own uid passes too.
- **At start, lodge refuses to run** unless `/run/lodge/apps` is a
  directory owned by root that no one else can write, since whoever can
  write it could replace an app's socket. Under `--root`, which is the
  developer's own, it only warns.

### Tokens, logins and nodes

| Credential | Form | Kept by lodge |
| --- | --- | --- |
| MCP token | `lgm_` + 43 base64url characters (32 random bytes from `crypto/rand`) | SHA-256, hex, with its name, app, scope, creation, last use and revocation; the agent user it is bound to, or none |
| allowed login | a tailnet login, 1 to 256 bytes of UTF-8 without control characters | as given, with when it was added |
| allowed node | a StableNodeID | the ID, the node's name when it was allowed, and when |
| admin | a tailnet login, as an allowed login | as given, with when it was added |

- A token's name is 1 to 64 characters from `[A-Za-z0-9@._-]`, unique
  among live tokens. A revoked token's row stays for the log, and its name
  may be reused.
- A token's scope is 1 to 256 visible ASCII characters, which lodge does
  not interpret.
- **A token works on one route.** One issued with `--agent
  USER` works only on [the agent socket](#the-agent-socket), from `USER`.
  One issued without it works only on the tailnet's `/mcp/<id>`.
- A token is shown once, when it is issued, and never again: not by
  `lodgectl tokens`, not in a log, not in the state directory.
- `lodgectl allow node NODE` takes a StableNodeID, or a name that matches
  exactly one peer in the `lodge` node's peer list. It stores the ID.
- All of these live in lodge's state directory (`/var/lib/lodge/`, 0700).

### The control socket and `lodgectl`

`/run/lodge/control.sock` (owner `lodge`, 0600) serves JSON-RPC 2.0, one
message per line. It accepts a connection only from uid 0 or lodge's own
uid, so `lodgectl` runs as root (`sudo lodgectl …`). It is the only place
that issues `lgm_` tokens, allows a login or a node, or makes an admin;
OAuth tokens come from an admin's approval on the consent page
([OAuth routes](#oauth-routes)). lodge serves it from its start, before its nodes have
joined the tailnet, so a first start without an auth key can be checked
and allowed while the nodes wait for their login.

| Command | Method | Does |
| --- | --- | --- |
| `lodgectl status` | `lodge.status` | each node's state, and its login URL while it needs a login; every app's state; session counts; and providers' availability |
| `lodgectl apps` | `lodge.apps` | each manifest, its state and its reason |
| `lodgectl reload` | `lodge.reload` | rereads the manifests, as `SIGHUP` does |
| `lodgectl logins` | `lodge.logins` | the allowed logins |
| `lodgectl allow login LOGIN` | `lodge.allowLogin` | adds one; `conflict` if listed |
| `lodgectl disallow login LOGIN` | `lodge.disallowLogin` | removes it and closes its sessions; `notFound` if absent |
| `lodgectl nodes` | `lodge.nodes` | the allowed nodes |
| `lodgectl allow node NODE` | `lodge.allowNode` | adds one; `conflict` if listed; `notFound` or `invalidParams` for a name that matches no peer or several |
| `lodgectl disallow node NODE` | `lodge.disallowNode` | removes it and closes its sessions |
| `lodgectl admins` | `lodge.admins` | the admin logins |
| `lodgectl allow admin LOGIN` | `lodge.allowAdmin` | adds one; `conflict` if listed; `invalidParams` for a login that breaks an allowed login's form |
| `lodgectl disallow admin LOGIN` | `lodge.disallowAdmin` | removes it; `notFound` if absent |
| `lodgectl tokens` | `lodge.tokens` | the tokens: name, app, scope, creation, last use, revocation; the agent user |
| `lodgectl token issue NAME --app ID --scope SCOPE [--agent USER]` | `lodge.issueToken` | prints the token alone on stdout, and on stderr how to add it to Claude Code; `conflict` for a live name, `notFound` for an app without MCP. `--agent` binds it to the agent socket and that user, and `invalidParams` answers a user the configuration does not list; the hint on stderr then names the socket |
| `lodgectl token revoke NAME` | `lodge.revokeToken` | revokes it and closes its streams; `notFound` if absent |
| `lodgectl sessions` | `lodge.sessions` | live sessions: ID, app, login, node name, start, `seenAt` |
| `lodgectl session end ID` | `lodge.endSession` | closes one; `notFound` if absent |
| `lodgectl usage [--since DATE]` | `lodge.usage` | inference usage per app and alias ([inference.md](inference.md#the-usage-log)) |
| `lodgectl provider key NAME` | `lodge.providerKey` | sets or removes an inference provider's key, read from stdin; `notFound` for no such provider ([inference.md](inference.md#credentials)) |

The hint on stderr is
`claude mcp add --transport http <id> https://LODGE/mcp/<id> --header "Authorization: Bearer <token>"`.
It never includes `--scope project`, which would write the token into a
repository.

## Rules

- lodge serves nothing on a non-loopback address except through its two
  tailnet nodes, and nothing through Funnel.
- No browser route writes, and no request to one reads identity from
  anything but WhoIs. The one exception is the consent routes, under every
  [consent check](#oauth-routes)
  ([ADR-0023](../adr/0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md)):
  `/oauth/authorize` records a pending request, which grants nothing; a
  decision, a `POST` to `/oauth/consent/<id>`, carries that request's CSRF
  token and hash; a revocation, a `POST` to `/oauth/grants/revoke`, carries
  the grants page's CSRF token. They still read identity from WhoIs alone,
  and set no cookie.
- Every browser and content check has a test that fails it. Each finding
  of lodge's security review, and of M4's review that lodge took over, has a
  test.
- Forged `Lodge-*`, `Tailscale-*` and forwarding headers change nothing.
  A test sends each.
- Issuing an `lgm_` token, allowing a login or a node, and making an admin
  happen only on the control socket. An OAuth token is issued only after
  an admin's approval on the consent page
  ([ADR-0023](../adr/0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md)).
- Only the agents lodge's configuration names connect to the
  agent socket, and a token works only on the route it was issued for.
  Each has a test that fails it.
- Only an enabled app's user connects to the broker's socket, and a pull
  request's reply stays in the account that sent it. Each has a test that
  fails it.
- A client receives only the events its grant reads, a narrowed grant
  stops the events not yet delivered outside it, a revoke ends delivery,
  and a consumer behind its stream's first message gets 1 resync event.
  Each has a test that fails it.
- The MCP gateway lists an app's tool only to a principal whose grant holds
  its permission, reads the grant again at each call, and binds each
  session to its client. An app call reaches only an operation that its
  manifest exposes to its caller. Each has a test that fails it.
- Only `internal/mcpgw` imports the Go MCP SDK.
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

- Rationale: [ADR-0023](../adr/0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md) (OAuth and the consent routes), [ADR-0024](../adr/0024-agents-reach-apps-on-hearth-through-one-mcp-gateway.md) (the MCP gateway and app calls), [ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md), [ADR-0004](../adr/0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md), [ADR-0005](../adr/0005-browsers-sign-in-with-their-tailnet-identity.md), [ADR-0006](../adr/0006-agents-reach-each-app-through-its-own-mcp-endpoint.md), [ADR-0007](../adr/0007-lodge-serves-every-apps-web-build-from-disk.md), [ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md), [ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md), [ADR-0010](../adr/0010-untrusted-bytes-come-from-a-second-node.md), [ADR-0014](../adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md), [ADR-0020](../adr/0020-apps-report-an-unread-count-for-the-rail-in-their-health.md) (the rail's `badge`, Proposed), [ADR-0022](../adr/0022-lodge-embeds-a-nats-broker-for-app-events.md) (the broker, Proposed)
- Context: [design/overview.md](../design/overview.md)
- Related: [oauth.md](oauth.md), [app-contract.md](app-contract.md), [lodge-client.md](lodge-client.md), [shell.md](shell.md); frostnotes' [lodge-app.md](https://github.com/frostyard/frostnotes/blob/main/docs/specs/lodge-app.md) (notes' side)
- Built in: [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app); the agent socket in [Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket); the MCP gateway and app calls in [Phase 8](../plans/0001-roadmap.md#phase-8--the-mcp-gateway-and-apps-on-hearth)
