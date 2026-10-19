# 0003 — lodge is the only process on the network

- **Status:** Proposed
- **Date:** 2026-10-18

## Context

frostyard's first-party apps are daemons with thin clients:

- frostnotes' `notesd`
  ([frostnotes ADR-0004](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0004-a-notes-daemon-with-thin-clients.md));
- frostmail's `maild`
  ([frostmail ADR-0002](https://github.com/frostyard/frostmail/blob/main/docs/adr/0002-daemon-and-thin-clients.md)).

Each serves JSON-RPC on a Unix socket to its own user. frostyard/core
[ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
decided that they run behind lodge, a web shell on the owner's tailnet,
with a personal-assistant agent to follow.

frostnotes' first plan for M4 put the network inside notesd:

- a tailnet node of its own;
- sign-in by tailnet identity;
- browser sessions;
- a server for its web build;
- an MCP endpoint with scoped tokens.

frostmail needs the same, because one maild should serve every client. The
assistant would need it a third time. That is the most security-sensitive
code in each app, and copies of it would drift as the apps' shared
machinery already has.

One host runs every app. Its apps hold mail, notes and their credentials.
An app that a bug or a dependency compromises should not reach another
app's data.

## Decision

- **lodge is the only process that listens on the network.** It is a
  tailnet node ([ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md)),
  and it owns these, once, for every app:
  - sign-in ([ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md));
  - agents' tokens ([ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md));
  - serving web code ([ADR-0007](0007-lodge-serves-every-apps-web-build-from-disk.md));
  - the shell ([ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md));
  - untrusted content ([ADR-0010](0010-untrusted-bytes-come-from-a-second-node.md));
  - inference ([ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md)).
- **An app is a daemon on a Unix socket behind lodge,** plus a web bundle and
  a manifest. The daemon serves HTTP/1.1 on `/run/lodge/apps/<id>.sock`,
  which belongs to the app's user and group `lodge`, mode 0660. It accepts
  a connection only from lodge's uid, which it checks with `SO_PEERCRED`. An
  app listens on no TCP port.
- **lodge tells the app who is calling, in `Lodge-*` headers.** Before it
  proxies a request, lodge deletes every `Lodge-*` header the client sent.
  It then sets `Lodge-Caller` (`session`, `agent`, `device` or `content`)
  and that kind's identity:
  - the login, the node and the session for a browser;
  - the token's name and scope for an agent.

  An app refuses a request without `Lodge-Caller`. Only `GET /health`,
  which lodge polls, is exempt. Each app path admits one caller kind. The
  peer check is what lets an app trust these headers.
- **Each app runs as its own system user,** declared in `sysusers.d`
  (`frostnotes`, `frostmail`, `frostassist`), with its state readable only
  by that user. No app reads another's state. The assistant and every other
  agent reach an app only through that app's `/mcp/<id>`, with a token.
- **lodge decides who reaches an app, and the app decides what they may
  do.** lodge checks identity and credentials, and passes a token's scope
  through unread. Each app enforces its callers' rights in its own engine.
  lodge never parses an app's JSON-RPC. It moves frames.
- **lodge serves every app's web bundle** from root-owned directories
  ([ADR-0007](0007-lodge-serves-every-apps-web-build-from-disk.md)). No
  daemon serves code to the lodge origin.
- **An app keeps its private socket** for its CLI and desktop bridge, as
  caller `local`. That socket is separate from lodge's and unchanged.
- **The contract has a major version** (`contract: 1` in the manifest).
  lodge disables an app whose major it does not speak, and the other apps
  keep running.

[specs/app-contract.md](../specs/app-contract.md) pins the layout,
manifest, URLs, headers and rules.
[specs/lodge-http.md](../specs/lodge-http.md) pins what lodge checks before
it proxies.

## Consequences

- Sign-in, origin checks, headers, tokens and serving code are written and
  reviewed once, in lodge. An app's network surface is one socket that only
  lodge can open.
- A compromised app daemon holds its own data and nothing else. It cannot
  read another app's state, open another app's socket (the peer check
  refuses it), or reach a provider key
  ([ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md)).
- **lodge is the trust root for callers.** A process running as lodge's
  user can claim any caller to any app, as lodge itself can. lodge's code,
  its dependencies and its user are the boundary that needs the security
  review.
- Every app depends on lodge being up. When lodge is down, nothing is
  reachable from a browser or an agent. Apps' private sockets and their
  desktop clients keep working.
- Apps build on the shared kit, `lodge/kit`, for the socket, the peer check
  and the headers, so the rules are implemented once on the app side too.
- A request crosses one more hop, a Unix socket on the same host. The
  WebSocket is relayed frame for frame.
- A contract change is coordinated across apps by its major version.

## Alternatives considered

- **Each app its own tailnet node,** as frostnotes' M4 first planned: every
  app repeats sign-in, sessions, headers and tokens. The owner gets one
  address per app, and nothing switches between them.
- **One binary that links every app:** one app's crash or release stops the
  others, every app's dependencies share one module, and the apps would
  share a uid and each other's data.
- **lodge as a library linked into each daemon:** one network process per
  app again, with a node, a certificate and an address each.
- **Apps on loopback TCP behind lodge:** any local process could connect,
  and the app could not tell lodge from another user. A Unix socket
  carries the peer's uid.
- **lodge enforcing each caller's rights:** lodge would have to parse every
  app's JSON-RPC and know its methods. Rights belong with the code that
  knows the data.
- **Caddy, or `tailscale serve`, in front of the daemons:** identity
  headers from a proxy are trusted only as far as the backend's listening
  address. A second process would hold the checks that ADR-0005 needs in
  one place.

## References

- Shapes: [specs/app-contract.md](../specs/app-contract.md), [specs/lodge-http.md](../specs/lodge-http.md), [design/overview.md](../design/overview.md), [roadmap](../plans/0001-roadmap.md)
- Builds on: frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md); frostnotes [ADR-0004](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0004-a-notes-daemon-with-thin-clients.md); frostmail [ADR-0002](https://github.com/frostyard/frostmail/blob/main/docs/adr/0002-daemon-and-thin-clients.md)
- Related: [ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md) to [ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md); frostnotes [ADR-0026](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0026-notesd-runs-as-a-lodge-app.md) (notes' side)
