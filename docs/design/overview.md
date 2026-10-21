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
(inference),
[ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md) (the
companion),
[ADR-0014](../adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md)
(the agent socket). The platform decision is frostyard/core's
[ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md).
Contracts: [specs/app-contract.md](../specs/app-contract.md),
[specs/lodge-http.md](../specs/lodge-http.md),
[specs/lodge-client.md](../specs/lodge-client.md),
[specs/shell.md](../specs/shell.md),
[specs/inference.md](../specs/inference.md),
[specs/ui-package.md](../specs/ui-package.md).

**Status: Phases 1 to 3 and 6 built (2026-10-19).**
- The kit and the client are released as `kit/v0.2.0` and `client/v0.2.0`.
- lodge v1 (the nodes, the gateway, the control socket, the shell, the
  development build and packaging) and the inference layer are merged, and
  run on lodge's first host.
- Claude Code's checks on `/mcp` passed (2026-10-19). The checks on the
  user's phone and Chrome's PDF viewer are not done. The Codex provider
  (the owner's ChatGPT plan) is built
  ([ADR-0015](../adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md)),
  and so is the Copilot provider (the owner's Copilot plan,
  [ADR-0016](../adr/0016-copilot-uses-the-owners-copilot-plan-through-githubs-device-sign-in.md), Proposed).
- The assistant, Porter, is designed (Phase 5). lodge's side of it, the
  roadmap's
  [Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket),
  is built and released in v0.2.0: the agent socket and the companion. The
  agent socket was checked on lodge's host (2026-10-19); the companion's
  checks on the user's devices wait for the deployment.

This document is rewritten as each phase lands.

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
   │       └─▶ the local model (MODEL_HOST), the owner's ChatGPT plan (codex)
   ├─ /mcp/<id> ◀── /run/lodge/agent.sock ◀── agents on this host (peer uid)
   └─ internal/control ◀── /run/lodge/control.sock ◀── lodgectl (root)

 notesd (user frostnotes) · maild (user frostmail) · porterd (user frostporter)
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
| lodge | `lodge` | `lodge`, `lodge-apps`, `lodge-agents` | `/var/lib/lodge/`: tsnet state, allow-lists, tokens, provider credentials |
| notesd | `frostnotes` | `lodge-apps` | its vaults' state; its socket is `frostnotes:lodge` 0660 |
| maild | `frostmail` | `lodge-apps` | mail, its accounts' credentials |
| porterd, the assistant | `frostporter` | `lodge-apps`, `lodge-agents` | its own state (its thread), and an MCP token for each app it uses |

- lodge can open every app's socket (group `lodge`), and nothing else of
  theirs.
- An app can open the inference socket (group `lodge-apps`), and nothing of
  lodge's or another app's.
- An agent on lodge's host can open the agent socket (group
  `lodge-agents`), and lodge serves it only when lodge's configuration
  names its user.
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
returns a `Peer` (`internal/peer`, which holds no Tailscale): the stable
node ID, the node's short name, its addresses, whether it is tagged, and
its owner's login and display name. Tests elsewhere use a fake WhoIs. The
development build uses a fixed one on loopback.

**Start order.** lodge opens its database, reads the manifests, and starts
both nodes without waiting for them. It then serves the control,
inference and agent sockets and polls the apps' health, and only then
waits for the nodes to join the tailnet before the gateway listens on
them. The gateway exists from the start, for the agent socket, and learns
the nodes' names once they have joined. So on a first start without an auth
key, `lodgectl status` already answers, and shows each node's login URL.
The journal says only that a login is needed: the URL claims the node for
whoever opens it first.

**Connections.** Each listener caps connections per remote address and in
all, refused requests close their connections, and device bodies have a
deadline per read, since device routes run before any identity is known
([lodge-http.md](../specs/lodge-http.md#connections)). Before each request,
lodge checks that an app's socket belongs to the manifest's user.

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

**Agents on lodge's host** use the agent socket,
`/run/lodge/agent.sock`, rather than the tailnet
([ADR-0014](../adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md);
[lodge-http.md](../specs/lodge-http.md#the-agent-socket)). Porter, the
assistant, is the first: an app that owns none of the user's data, and
reaches notes and mail only through their MCP, with one token per app.

- lodge admits a connection only when its peer's uid is one of the agents
  that `/etc/lodge/config.json` names. That uid stands in for `Host`,
  WhoIs and the address check.
- The socket serves `/mcp/<id>` alone, with the tailnet's token check,
  scope, sessions, revocation and log, and the agent's user logged in
  place of a node.
- A token issued with `--agent USER` works only there, from that user, and
  any other token only on the tailnet. A token copied off the host is
  useless, and one agent cannot use another's.
- So an agent on lodge's host needs no tsnet and no node, and its calls
  never leave the host. Agents elsewhere stay on the tailnet.
- `internal/agents` keeps the list, read from the configuration with the
  manifests, and the socket's connections; reloading without an agent
  closes them. The gateway serves the socket with the same code as the
  tailnet's `/mcp/<id>`. The kit's `lodgeapp.Env.AgentClient` is an
  agent's client of it.

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

**The companion** is the one app whose manifest sets
`companion: true`: Porter, whose chat is its main surface
([ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md);
[shell.md](../specs/shell.md#the-companion)).

- It opens in a frame of its own beside the current app's: a panel 400
  pixels wide from 600 pixels, or a full-height sheet under that, over an
  app that stays loaded.
- A button on the rail and the bar, and Ctrl+Shift+\`, toggle it. The
  switcher and its `key` open the same app in full, and the panel hides
  while that is current.
- The shell forwards it the current app's `context`, a pointer it reads
  through MCP with its own tokens. An app's "Ask about this" sends `ask`,
  which opens the companion with a prompt
  ([lodge-client.md](../specs/lodge-client.md#the-companion)).
- `internal/apps` decides which app is the companion, and `/lodge/apps`
  marks it. The client's shell half keeps a port for the companion's frame
  beside the app's own, and drops what the companion may not send. The
  development build's `dev/e2e.sh` runs a fake app, Helper, as the
  companion.

One web manifest, `/lodge/manifest.webmanifest`, makes lodge a single
home-screen app. No service worker is registered.

### Inference

`internal/inference` serves the OpenAI-compatible socket
([app-contract.md](../specs/app-contract.md#inference);
[inference.md](../specs/inference.md) pins lodge's side).

- It maps the peer's uid to an app.
- It resolves the request's alias to a provider and model from
  `/etc/lodge/config.json`.
- It queues the request by priority, within the provider's slots, keeping
  one slot for interactive work.
- It forwards with the provider's credentials, and logs the metadata,
  never the content.

The first provider is the local model at `MODEL_HOST`, an `openai`
provider. A `codex` provider is the owner's ChatGPT plan, through Sign in
with ChatGPT ([ADR-0015](../adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md)):
- **Signing in:** the owner signs in on their computer with `lodgectl
  provider login` and imports the login file on lodge's host. lodge then
  refreshes it.
- **Translation:** lodge turns apps' chat completions into the Responses
  requests that plan usage takes, and turns the answers back
  ([specs/inference.md](../specs/inference.md#the-codex-provider)).

A `copilot` provider is the owner's GitHub Copilot plan, through the
Copilot API that the editors use
([ADR-0016](../adr/0016-copilot-uses-the-owners-copilot-plan-through-githubs-device-sign-in.md), Proposed):
- **Signing in:** `lodgectl provider login NAME --copilot` runs GitHub's
  device flow on any computer, and the owner sets the GitHub token as the
  provider's key on lodge's host.
- **Requests:** lodge trades that token for a short-lived Copilot token,
  kept in memory, and sends apps' requests to the OpenAI-compatible API
  unchanged ([specs/inference.md](../specs/inference.md#the-copilot-provider)).

A provider that lodge's host cannot reach is shown as unavailable, and its
aliases fail at once.

- The socket is made at start, beside the control socket, and served
  before the tailnet nodes join. lodge is in group `lodge-apps` to give it
  that group.
- The configuration is the `inference` member of `/etc/lodge/config.json`,
  reloaded with the manifests. A broken one is refused whole, and
  `lodgectl status` says why.
- Each provider has its own queue, kept across a reload that leaves the
  provider unchanged, so requests in flight still count.
- The usage log goes to the journal and to lodge's database;
  `lodgectl usage` sums it.

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
  - JSON-RPC 2.0 (`kit/jsonrpc`), extracted from frostnotes and frostmail:
    the connection loop, the router, the client and the private socket,
    generic over each app's generated types, with `kit/rpctest` for tests
    ([ADR-0012](../adr/0012-the-kit-shares-the-rpc-runtime-not-the-generator.md));
  - `/health`;
  - the MCP-session helper;
  - an outbound address guard, `kit/netguard`, for a fetch of a URL the app
    was given: it refuses loopback, private, link-local, tailnet and other
    non-public addresses after DNS, on each connection, so a redirect is
    refused too
    ([ADR-0017](../adr/0017-the-kit-guards-outbound-fetches-against-inside-addresses.md));
  - an inference client.

  It never imports `tailscale.com` or lodge's `internal/`, so apps do not
  pull in Tailscale.
- **`client/`** is `@frostyard/lodge-client`: the app half and the shell
  half of the frame protocol.
- **`ui/`** is `@frostyard/lodge-ui`, the look the apps share: the design
  tokens as CSS custom properties (light and dark, and the density
  tokens), the domain accents apart from them, the Tailwind v4 theme over
  the tokens with the shared type roles, a base layer, Inter, and React
  primitives in a `./react` subpath. `./theme` is framework-free, for the
  shell and Porter too. It carries no preflight and never imports Tailwind
  as a whole, and every app bundles its own copy at build time
  ([ADR-0018](../adr/0018-a-ui-package-beside-the-client.md),
  [ui-package.md](../specs/ui-package.md)). Version 0.1.0 carries
  frostmail's values unchanged; the apps move to it one at a time, and the
  look change comes later as token values only. **The shell takes the same
  look**: its cream and green, on `system-ui`, became the package's tokens
  and Inter ([shell.md](../specs/shell.md#colours-and-type)), with its
  manifest colours and home-screen icons moved with them.

Apps pin all three to a published lodge commit or release.

### Testing

- **`internal/tailnet`:** real `tsnet` nodes against Tailscale's in-process
  test control server (`tstest/integration/testcontrol`). WhoIs names a
  user's node and a tagged node, a node without a key reports its login
  URL, and the TLS listener offers HTTP/1.1 only. tsnet starts no log
  uploader inside a test binary, so the log-host test builds a helper
  binary and watches its connections through a recording proxy: a plain
  `tsnet.Server` is seen dialing the log host, and lodge's node is not.
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
  build holds none of it, and another serves a kit app through it end to
  end. `dev/e2e.sh` runs it with two fake apps, for trying the shell in a
  browser.
- **The shell:** unit tests of the rail, the switcher, frames, history and
  app states in the nsl machine (Vitest with happy-dom).
- **Packaging:** `internal/packagecheck` stages an install and runs
  `systemd-analyze verify` on the units.
- **`client/`:** unit tests of both halves in the nsl machine, including
  the origin and source checks.

## Operational notes

- **Install:** `make shell-build` (in the nsl machine) and `make dist`,
  then `packaging/install.sh` on the host
  ([packaging/README.md](../../packaging/README.md)).
- **First start:**
  1. Make a tagged, pre-approved auth key in the tailnet's admin console.
  2. Start lodge once with it in `TS_AUTHKEY` (`/etc/lodge/lodge.env`).
     Later starts read `/var/lib/lodge/tailnet/` and need no key. Without
     one, `sudo lodgectl status` shows each node's login URL.
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
- **Adding an agent on lodge's host:** put its system user in
  group `lodge-agents`, add the user to `agents` in
  `/etc/lodge/config.json`, run `sudo lodgectl reload`, and issue its
  tokens with `sudo lodgectl token issue NAME --app ID --scope SCOPE
  --agent USER`.
- **Back up** `/var/lib/lodge/` with the apps' state. Restoring it keeps
  the nodes' names. Losing it means new auth keys, and the same names only
  if the old nodes are first removed from the tailnet.
- **Nothing private in node names:** they appear in public certificate
  logs.
- **Providers:** copy `/etc/lodge/config.example.json` to
  `/etc/lodge/config.json` and name the providers and aliases, set any key
  with `sudo lodgectl provider key NAME` (from stdin), then `sudo lodgectl
  reload`. lodge's host must reach each configured provider. A local model
  on another machine needs a network path to it: a firewall rule, a
  listener or the tailnet. `lodgectl status` shows a provider lodge cannot
  reach as unavailable.
- If Tailscale's coordination service is down at start, lodge logs it and
  keeps trying. Peers that already know lodge keep working.

## References

- Rationale: [ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md) to [ADR-0014](../adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
- Contracts: [specs/app-contract.md](../specs/app-contract.md), [specs/lodge-http.md](../specs/lodge-http.md), [specs/lodge-client.md](../specs/lodge-client.md), [specs/shell.md](../specs/shell.md), [specs/inference.md](../specs/inference.md)
- Built in: [roadmap](../plans/0001-roadmap.md), Phases [1](../plans/0001-roadmap.md#phase-1--the-kit) (the kit and the client), [2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app) (lodge v1), [3](../plans/0001-roadmap.md#phase-3--the-inference-layer) (inference), [4](../plans/0001-roadmap.md#phase-4--mail-in-lodge) (mail), [5](../plans/0001-roadmap.md#phase-5--the-assistants-design) (the assistant's design), [6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket) (the companion and the agent socket)
- Related: frostnotes' [lodge-app.md](https://github.com/frostyard/frostnotes/blob/main/docs/specs/lodge-app.md) and [ADR-0026](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0026-notesd-runs-as-a-lodge-app.md); Porter's [ADR-0003](https://github.com/frostyard/porter/blob/main/docs/adr/0003-porter-is-a-lodge-app-that-owns-no-data.md) and [design/overview.md](https://github.com/frostyard/porter/blob/main/docs/design/overview.md)
