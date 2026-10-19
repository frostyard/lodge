# Design: lodge, how it fits together

Living document. Rationale:
[ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md) (the
gateway),
[ADR-0004](../adr/0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md)
(the nodes),
[ADR-0005](../adr/0005-browsers-sign-in-with-their-tailnet-identity.md)
(sign-in),
[ADR-0006](../adr/0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)
(agents),
[ADR-0007](../adr/0007-lodge-serves-every-apps-web-build-from-disk.md)
(bundles),
[ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md) (the home
screen),
[ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md) (the
shell),
[ADR-0010](../adr/0010-untrusted-bytes-come-from-a-second-node.md)
(content),
[ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md)
(inference). The platform decision is frostyard/core's
[ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md).
Contracts: [specs/app-contract.md](../specs/app-contract.md),
[specs/lodge-http.md](../specs/lodge-http.md),
[specs/lodge-client.md](../specs/lodge-client.md),
[specs/shell.md](../specs/shell.md).

**Status: planned.** Only a stub `cmd/lodge` exists. This document
describes the design the [roadmap](../plans/0001-roadmap.md) builds, and is
rewritten as each phase lands.

## Overview

lodge is one Go process on one host. It is two tailnet nodes, and the only
thing on that host that listens on the network. Browsers, agents and
devices reach it over the tailnet. The apps sit behind it, each a daemon on
a Unix socket running as its own system user. lodge checks who is calling,
tells the app in headers, and moves the bytes.

```
 phone, laptop (browser)        laptop, tagged nodes (agents)     desktops (notes devices)
       │ https://LODGE/…               │ Bearer lgm_…                  │ notes' device token
       └─────────────────────── WireGuard (the tailnet) ───────────────┘
                                       │
 lodge ── internal/tailnet: node "lodge" (:443) and node "lodge-content" (:443), WhoIs
   │
   ├─ internal/gateway: sort by kind → checks → headers → proxy
   │    ├─ /, /<id>/<route>, /lodge/…   the shell (from /usr/share/lodge/shell/)
   │    ├─ /app/<id>/…                  bundles (from /usr/share/lodge/apps/<id>/web/)
   │    ├─ /app/<id>/api, /x/…          sessions and reads ─┐
   │    ├─ /mcp/<id>                    agents' tokens      ├─▶ /run/lodge/apps/<id>.sock
   │    ├─ /device/<id>/…               the app's own auth  │    (HTTP/1.1; peer must be lodge)
   │    └─ CONTENT/<id>/…               sandboxed bytes ────┘
   ├─ internal/inference ◀── /run/lodge/inference.sock ◀── app daemons (peer uid → app)
   │       └─▶ the local model (MODEL_HOST), later Copilot and Codex
   └─ internal/control ◀── /run/lodge/control.sock ◀── lodgectl (root)

 notesd (user frostnotes) · maild (user frostmail) · the assistant (user frostassist)
```

**One host per lodge.** lodge and every app it hosts share one host,
because they meet on Unix sockets. lodge's first host is the always-on
virtual machine that runs the notes hub today. Notes moves in place. That
host sits behind NAT on the machine that runs the local model, and `tsnet`
works from behind it.

## Design

### Processes and users

| Process | User | Groups | Holds |
| --- | --- | --- | --- |
| lodge | `lodge` | `lodge` | `/var/lib/lodge/`: tsnet state, allow-lists, tokens, provider credentials |
| notesd | `frostnotes` | `lodge-apps` | its vaults' state; its socket is `frostnotes:lodge` 0660 |
| maild | `frostmail` | `lodge-apps` | mail, its accounts' credentials |
| the assistant | `frostassist` | `lodge-apps` | its own state, and MCP tokens for the apps it uses |

- lodge can open every app's socket (group `lodge`), and nothing else of
  theirs.
- An app can open the inference socket (group `lodge-apps`), and nothing of
  lodge's or another app's.
- systemd socket units create the app sockets
  ([app-contract.md](../specs/app-contract.md#install-layout)).
- An app's private socket, for its CLI and desktop bridge, is its own
  business.

### The nodes

`internal/tailnet` is the only package that imports `tailscale.com`. It
runs two `tsnet.Server`s in the lodge process:

- `lodge`, for the shell, the apps, agents and devices;
- `lodge-content`, for untrusted bytes.

Each has its state in `/var/lib/lodge/tailnet/<node>/`, TLS on `:443` with
its own certificate, and HTTP/1.1 only. Log upload is disabled before
either starts. The package gives the gateway a listener per node, each
node's DNS name (`LODGE`, `CONTENT`), and `WhoIs(ctx, remoteAddr)`. WhoIs
returns a `Peer`: the stable node ID, the node's short name, its
addresses, whether it is tagged, and its owner's login and display name.
Tests elsewhere use a fake WhoIs. The development build uses a fixed one
on loopback.

### The gateway

Every request goes through one router (`internal/gateway`):

1. It sorts the request by node and path into a kind: browser, agent,
   device or content
   ([lodge-http.md](../specs/lodge-http.md#routes)).
2. It runs that kind's checks, and only those.
3. It finds the app: enabled, disabled or unknown.
4. It serves a file itself, or proxies to the app's socket.

Proxying deletes the client's `Lodge-*`, `Tailscale-*` and forwarding
headers, sets the caller's, and relays the body or WebSocket frames
without reading them. One function per response kind sets the headers:
the shell, an app frame or content. An app's response cannot widen them.

`internal/apps` reads `/usr/share/lodge/apps/*/app.json` at start and on
reload, validates each manifest against the kit's type, and polls each
app's `GET /health` every 15 s. Each app's state (up, degraded, down,
disabled, and why) feeds `/lodge/apps` and `lodgectl apps`.

### Sign-in and sessions

- **Identity.** A browser or content request is admitted when WhoIs names
  the peer, from one of its own addresses, on an untagged node whose login
  and node are both allowed (`internal/allow`). The WhoIs answer is cached
  per connection for up to 60 s. The lists are checked on every request.
- **Sessions.** `internal/sessions` holds every live `/app/<id>/api`
  WebSocket, with its peer and `seenAt`.
  - A session ends after 90 s without a frame from the browser.
  - A ticker re-checks WhoIs and the lists every 60 s.
  - Disallowing a login or a node closes its sessions at once.
  - A node's ninth session of one app evicts its oldest.

  Frontends keep their sessions alive by sending traffic while visible.
- **Writes.** No browser route but the WebSocket accepts anything but
  `GET` and `HEAD`, so a forged cross-site request finds nothing that
  writes.

### Agents

`internal/tokens` keeps MCP tokens as SHA-256 hashes, each with its app
and scope. On `/mcp/<id>`, the gateway refuses an `Origin`, requires a
named peer, and checks the bearer token. It then proxies with the token's
name and scope instead of the secret. The app owns MCP sessions, and binds
each to the token name and scope that opened it. `lodge/kit` provides the
session helper. Revoking a token closes every stream lodge is proxying for
it.

### Untrusted content

`lodge-content` serves `https://CONTENT/<id>/<path>` from the app's
`/content/<path>`, after the browser identity checks. Every response gets
the sandbox policy and may only be framed by `LODGE`. Apps learn the
origin from `init`, and build URLs with `contentURL()`. Their frames' policy
allows `CONTENT` for images, media and frames. A content URL may open in a
new tab, still sandboxed.

### The shell

The shell page ([shell.md](../specs/shell.md)) is lodge's own bundle,
served for `/`, `/<id>` and `/<id>/<route>`.

- It draws a rail on wide screens and a bar on narrow ones, and a switcher
  on Ctrl+\`.
- It keeps one frame per opened app at `/app/<id>/<route>`, which stays
  loaded.
- It reads `/lodge/apps` for the apps and their states.
- It talks to frames only through lodge-client
  ([lodge-client.md](../specs/lodge-client.md)), keeps the address bar on
  canonical URLs, and owns history.

One web manifest, `/lodge/manifest.webmanifest`, makes lodge a single
home-screen app. No service worker is registered.

### Inference

`internal/inference` serves the OpenAI-compatible socket
([app-contract.md](../specs/app-contract.md#inference)).

- It maps the peer's uid to an app.
- It resolves the request's alias to a provider and model from
  `/etc/lodge/config.json`.
- It queues the request by priority, within the provider's slots, keeping
  one slot for interactive work.
- It forwards with the provider's credentials, and logs the metadata,
  never the content.

The first provider is the local model at `MODEL_HOST`. Copilot and Codex
adapters follow once their terms are checked. A provider that lodge's host
cannot reach is shown as unavailable.

### The control socket

`internal/control` serves JSON-RPC on `/run/lodge/control.sock` to root
and to lodge's own uid. `cmd/lodgectl` is its client. It is the only way to
allow a login or a node, issue a token, or reload the apps
([lodge-http.md](../specs/lodge-http.md#the-control-socket-and-lodgectl)).

### The kit and the client

- **`kit/`** is a nested Go module, `github.com/frostyard/lodge/kit`, for
  app daemons. It holds:
  - the manifest type;
  - the socket server, with socket activation, the peer check and
    `LODGE_ROOT`;
  - the `Lodge-*` caller in the request's context;
  - JSON-RPC over WebSocket, extracted from frostnotes and frostmail;
  - `/health`;
  - the MCP-session helper;
  - an inference client.

  It never imports `tailscale.com` or lodge's `internal/`, so apps do not
  pull in Tailscale.
- **`client/`** is `@frostyard/lodge-client`: the app half and the shell
  half of the frame protocol.

Apps pin both to a published lodge commit or release.

### Testing

- **`internal/tailnet`:** real `tsnet` nodes against Tailscale's in-process
  test control server (`tstest/integration/testcontrol`). WhoIs names a
  user's node and a tagged node, TLS answers on each node's name, and
  nothing dials the log host.
- **`internal/gateway`:** every check in lodge-http.md has a test that
  fails it and one that passes it, with a fake WhoIs and `httptest`, and
  apps faked by `kit`'s test server on a socket in a temp root.
  - Forged identity headers change nothing.
  - Path tricks find nothing.
  - Disallowing, ending a session and revoking a token close live
    connections.
  - Every limit is tested at its value and past it.
- **The development build** (`e2e` tag) runs lodge on loopback with one
  fixed identity, for apps' browser runs. A test proves that a release
  build holds none of it.
- **`client/`:** unit tests of both halves in the nsl machine, including
  the origin and source checks.

## Operational notes

- **First start:**
  1. Make a tagged, pre-approved auth key in the tailnet's admin console.
  2. Start lodge once with it in `TS_AUTHKEY`. Later starts read
     `/var/lib/lodge/tailnet/` and need no key.
  3. Run `sudo lodgectl allow login <your login>` and `sudo lodgectl allow
     node <your phone>`.
  4. Open `https://lodge.<tailnet>.ts.net/`.
- **The tailnet policy:** tag both nodes, and grant port 443 on that tag
  only to the devices that use lodge and to agents' tagged nodes. Never
  allow a node that forwards for others: a subnet router, a node running
  `tailscale serve`, or a proxy.
- **Adding an app:** install its manifest and bundle under
  `/usr/share/lodge/apps/<id>/` and its socket unit, then run `lodgectl
  reload`. `lodgectl apps` says whether it is enabled, and why not.
- **Losing a phone:** remove it from the tailnet, or `lodgectl disallow node`
  it. Its sessions close within a minute, or at once.
- **A token in the wrong hands:** `lodgectl token revoke NAME`. Its streams
  close at once. Its writes carry its name in each app's history.
- **Back up** `/var/lib/lodge/` with the apps' state. Restoring it keeps
  the nodes' names. Losing it means new auth keys, and the same names only
  if the old nodes are first removed from the tailnet.
- **Nothing private in node names:** they appear in public certificate
  logs.
- **Providers:** lodge's host must reach each configured provider. A local
  model on another machine needs a network path to it: a firewall rule, a
  listener or the tailnet. `lodgectl status` shows a provider lodge cannot
  reach as unavailable.
- If Tailscale's coordination service is down at start, lodge logs it and
  keeps trying. Peers that already know lodge keep working.

## References

- Rationale: [ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md) to [ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
- Contracts: [specs/app-contract.md](../specs/app-contract.md), [specs/lodge-http.md](../specs/lodge-http.md), [specs/lodge-client.md](../specs/lodge-client.md), [specs/shell.md](../specs/shell.md)
- Built in: [roadmap](../plans/0001-roadmap.md), Phases [1](../plans/0001-roadmap.md#phase-1--the-kit) (the kit and the client), [2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app) (lodge v1), [3](../plans/0001-roadmap.md#phase-3--the-inference-layer) (inference), [4](../plans/0001-roadmap.md#phase-4--mail-in-lodge) (mail), [5](../plans/0001-roadmap.md#phase-5--the-assistants-design) (the assistant)
- Related: frostnotes' [lodge-app.md](https://github.com/frostyard/frostnotes/blob/main/docs/specs/lodge-app.md) and [ADR-0026](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0026-notesd-runs-as-a-lodge-app.md)
