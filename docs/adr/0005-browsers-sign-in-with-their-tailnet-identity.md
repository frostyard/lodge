# 0005 — Browsers sign in with their tailnet identity

- **Status:** Proposed
- **Date:** 2026-10-18

## Context

A browser on lodge runs every app's frontend
([ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md)). Each app's
frame talks to its daemon over a WebSocket, `/app/<id>/api`, as caller
`session`. A session can read and write the owner's mail and notes, so its
sign-in guards everything.

On 2026-10-18 the user decided that browsers sign in by their Tailscale
identity, with no passwords or passkeys. lodge learns which tailnet node
and user is calling, and turns that into a session. lodge is a node of its
own whose WhoIs names the peer of each connection
([ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md)).
frostnotes' M4 drafted this for notesd's hub (its draft ADR-0027, local
commit `0586e2b`). It moves to lodge with the fixes from M4's security
review.

One person owns lodge, the tailnet and every device. A browser adds these
threats, rather than strangers on the network:

- **Other sites in the same browser.** A page on any site can open a
  WebSocket to lodge, submit a form to it, or load its images. The request
  leaves from the user's device with the user's identity, as a cookie
  would. That is cross-site request forgery, cross-site WebSocket
  hijacking, and DNS rebinding of a hostile name onto lodge's address.
- **Script in an app's own page** (XSS), from content or a dependency. It
  can do anything the page can, while it runs.
- **A credential that leaks** from storage, a URL, a log or a screenshot,
  and is used from elsewhere.
- **Other nodes on the tailnet.** They share lodge's site
  (`*.<tailnet>.ts.net`, since `ts.net` is on the Public Suffix List), so
  the browser counts them as the same site. Shared and tagged nodes may
  reach lodge too, as the policy allows.
- **Nodes that forward for others.** A node that relays traffic carries its
  owner's identity to everyone behind it. That covers a subnet router,
  `tailscale serve` or Funnel on another node, a reverse proxy, and
  `ssh -L` (review finding 4).

The review also found:

- **Dead sessions could lock a phone out** (finding 1). A session was
  re-checked for its WhoIs only, never for liveness. `x/net/websocket`
  sends no pings, and tsnet's gVisor connections get no TCP keepalive. So
  a phone that went to sleep held its sessions until the per-node limit
  refused it.
- **The diagnostic `GET /api` without an upgrade carries no `Origin`, so
  requiring one failed it** (finding 2).
- **WebKit on iOS may not send `Sec-Fetch-Site` on a WebSocket handshake**
  (finding 3).
- **The test identity was underspecified** (finding 7). Host and Origin
  checks fail under Firefox on loopback.

## Decision

- **Identity is the connection's tailnet peer, asked of lodge's own node.**
  For each browser request, lodge calls WhoIs on the request's remote
  address in its `tsnet` node. The answer is the WireGuard peer whose key
  Tailscale's coordination service bound to that node, and the node's
  owner. Nothing a client sends is consulted. lodge ignores
  `Tailscale-User-*`, `X-Forwarded-*`, `Forwarded`, cookies, queries and
  tokens for identity.
- **A peer may sign in when every one of these holds:**
  - WhoIs names it;
  - the remote address is one of that node's own Tailscale addresses, which
    shuts out traffic routed for a subnet through the node;
  - its node is not tagged, since a tagged node belongs to no user;
  - its owner's login is on lodge's **allowed logins**, compared exactly;
  - its node is on lodge's **allowed nodes**, by stable node ID.

  Both lists start empty. The owner runs `lodgectl allow login LOGIN` and
  `lodgectl allow node NODE`, and allows only end-user devices that do not
  forward for others. lodge checks the lists on every request, so
  `lodgectl disallow` takes effect at once and closes the sessions it
  affects. A WhoIs answer may be reused for at most 60 s on the same TCP
  connection.
- **Forwarding needs three fences, because no single one is enough.**
  - The address check stops routed traffic.
  - A node that forwards under its own address (a subnet router with SNAT,
    `tailscale serve`, a reverse proxy, `ssh -L`) looks like itself. Only
    the node allow-list keeps it out.
  - The tailnet policy should grant lodge's port 443 to the allowed
    devices only.
- **A session is one WebSocket connection, and the browser holds no
  credential.** `GET /app/<id>/api` upgrades to a WebSocket after these
  checks, each failure a 403 before any upgrade:
  - `Host` is lodge's name;
  - `Origin` is exactly `https://` and lodge's name, and is required on the
    upgrade only;
  - `Sec-Fetch-Site`, when the browser sends it, is `same-origin`;
  - the peer may sign in.

  lodge relays the socket to the app's `GET /api` with the session's
  identity headers. There is no session cookie, no token in storage and
  nothing in a URL. Closing the page signs out. A reconnect is a new
  sign-in, and costs one WhoIs.
- **Sessions are kept alive by traffic, and dead ones go.** lodge closes a
  session after 90 s without a frame from the browser. An app's frontend
  sends a message at least every 30 s while its page is visible (notes
  calls `rpc.ping`). A hidden or sleeping page therefore loses its sessions
  and reconnects when it returns. Every 60 s lodge asks WhoIs again and
  re-checks the allow-lists, and closes a session whose peer no longer
  passes.
- **Sessions are bounded, and the bound evicts rather than locks out.** A
  node may hold 8 sessions per app, and lodge 64 in all. A new session past
  a node's bound closes that node's least recently used session of the
  app. Past lodge's bound, the new session is refused (429).
- **Nothing outside a WebSocket changes anything.** Every other browser
  route (the shell page, app files, an app's `x/` reads) answers `GET` and
  `HEAD` only, and lodge reads nothing from a query.
  - Forged requests: a cross-site form or fetch reaches lodge with the
    user's identity, but finds nothing that writes. A cross-site WebSocket
    carries its page's `Origin`, which a browser sets and no page can
    change.
  - Reads across sites: every browser route checks `Host`, against DNS
    rebinding. It refuses `Sec-Fetch-Site` other than `same-origin` or
    `none`, except a top-level navigation to the shell page.
    [specs/lodge-http.md](../specs/lodge-http.md#browser-checks) pins the
    exception and the response headers.
- **Issuing a credential happens only on lodge's control socket:**
  allowing a login or a node, and issuing an MCP token
  ([ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)).
  `lodgectl` runs on lodge's machine, as root. Script in a page could use
  the session while the page is open, but could leave nothing behind that
  outlives it.
- **Apps decide what a session may do.** lodge passes `Lodge-Caller:
  session` with `Lodge-Login`, `Lodge-Display-Name`, `Lodge-Node-ID`,
  `Lodge-Node-Name` and `Lodge-Session`. Each app admits methods for
  `session` in its own engine, and attributes a session's writes to the
  connection, never to the request's parameters
  ([specs/app-contract.md](../specs/app-contract.md#caller-headers)).
- **Tests fake the identity, and never the release binary.** WhoIs is an
  interface, and unit tests give lodge a fake one. The development build,
  built with the `e2e` tag, gives the peers of its loopback listener one
  fixed identity. That identity still goes through the allow-lists, and
  `LODGE`, `Origin` and `connect-src` are defined for loopback
  ([specs/lodge-http.md](../specs/lodge-http.md#the-development-build)).
  No flag or variable of a release build turns a check off. A test proves
  with `go tool nm` that a release build holds no e2e code.

## Consequences

- Nothing in the browser can be phished, stolen or replayed. There are no
  passwords, cookies or tokens to manage, expire or rotate. A lost phone is
  removed from the tailnet, or from `lodgectl`'s node list, and its
  sessions end within a minute.
- The device is the credential. Anyone using an allowed device while it is
  unlocked is signed in, in any browser on it. So is any program on it that
  speaks WebSocket and sets lodge's `Origin`. The device's lock screen, the
  tailnet's device approval and the node allow-list are the guards.
- Adding a device takes two steps: it joins the tailnet, and the owner
  allows its node. A new browser on an allowed device needs nothing.
- lodge works only at its tailnet name, the one its certificate names.
- Sessions do not outlive a lodge restart. Apps reconnect, as they do when
  their daemon restarts.
- A page in a background tab, or on a sleeping phone, drops its sessions
  after 90 s and reconnects on its return. The cost is one WhoIs and the
  app's resubscription.
- Tailscale's coordination service is trusted to say who owns a node, as
  it already is for the rest of the tailnet.
- Whether WebKit sends `Sec-Fetch-Site` on WebSocket handshakes is checked
  on the user's phone in the roadmap's Phase 2. The check holds either way.

## Alternatives considered

- **Tailscale's identity headers from `tailscale serve`:** only as
  trustworthy as the backend's listening address
  ([ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md)), and
  they name a user, not the device.
- **A session cookie issued after the tailnet check:** a second credential.
  It needs `HttpOnly`, `SameSite` and CSRF tokens, and `SameSite` does not
  separate the tailnet's nodes, which are one site. It also needs expiry
  and revocation, and gains nothing when every connection can be
  identified afresh.
- **Passwords or passkeys:** declined by the user. They are credentials to
  enroll, recover and rate-limit, for one person the tailnet already
  identifies.
- **Logins only, without nodes:** a forwarding node of an allowed login
  would admit everyone behind it.
- **Any tailnet user, or the node's owner:** a tagged lodge has no owner,
  and a share would admit another tailnet's users. An explicit list costs
  one command.
- **Pings from lodge instead of from the page:** a browser answers pings
  while its tab sleeps, so idle tabs would hold sessions until the limit.
  Traffic from the visible page keeps only live sessions.
- **Refusing a node's ninth session, as M4's draft did:** a phone whose
  sessions died would be locked out until they timed out.

## References

- Shapes: [specs/lodge-http.md](../specs/lodge-http.md#browser-checks), [specs/lodge-http.md](../specs/lodge-http.md#sessions), [specs/app-contract.md](../specs/app-contract.md#caller-headers), [design/overview.md](../design/overview.md#sign-in-and-sessions), [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app)
- Builds on: [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md), [ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md); frostnotes' M4 draft ADR-0027 (local commit `0586e2b`), with review findings 1, 2, 3, 4 and 7
- Related: [ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md), [ADR-0007](0007-lodge-serves-every-apps-web-build-from-disk.md), [ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md)
