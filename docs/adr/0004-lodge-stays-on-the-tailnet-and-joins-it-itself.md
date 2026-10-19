# 0004 — lodge stays on the tailnet, and joins it itself

- **Status:** Proposed
- **Date:** 2026-10-18

## Context

lodge is the only process on the network
([ADR-0003](0003-lodge-is-the-only-process-on-the-network.md)). It serves
browsers, agents and notes' devices. frostnotes' M4 drafted this decision
for notesd's hub (its draft ADR-0026, local commit `0586e2b`). It moves to
lodge, which now owns the hub's network side.

On 2026-10-18 the user decided:

- **Everything stays on the tailnet:** the shell, every app, MCP and
  devices. Nothing goes on the public internet, and there is no Funnel.
- The phone and the second machine join the tailnet to use lodge.
- **Browsers sign in by their tailnet identity**
  ([ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md)), which
  lodge must learn in a way no client can forge.
- **The tsnet node is named `lodge`.** A second node, `lodge-content`,
  serves untrusted bytes
  ([ADR-0010](0010-untrusted-bytes-come-from-a-second-node.md)).

Tailscale offers three ways for a Go service to be on a tailnet:

- behind `tailscale serve` on a machine that runs `tailscaled`;
- on a `tailscaled` node's tailnet address, asking `tailscaled`'s local API
  who a connection's peer is (WhoIs);
- as a node of its own, with the `tsnet` library: a userspace network stack
  inside the process, with its own state, name, certificate and WhoIs.

lodge is pure Go (`CGO_ENABLED=0`), and so is `tsnet`. M4's review checked
the `tsnet` APIs this relies on, and found three gaps:

- `tsnet` uploads logs to Tailscale unless told not to. Its logger skips
  log upload only in tests, and may ignore `TS_NO_LOGS_NO_SUPPORT` (to
  verify).
- `ListenTLS` sets no ALPN.
- A login URL is a worse first start than a tagged auth key.

## Decision

- **lodge is reachable only on the tailnet.** It has no public endpoint: no
  Funnel, and no proxy to the internet. lodge never calls `ListenFunnel`,
  so no route reaches beyond the tailnet, whatever the host's configuration.
  The tailnet's access policy decides who reaches lodge at all. lodge's docs
  say to tag both nodes, and to grant port 443 on that tag only to the
  devices that should use lodge
  ([design/overview.md](../design/overview.md#operational-notes)).
- **lodge joins the tailnet itself, with `tsnet`,** as two nodes. They are
  named `lodge` and `lodge-content` by default (`--node`,
  `--content-node`). `internal/tailnet` is the only package that imports
  `tailscale.com`. It wraps starting a node, listening, WhoIs, the node's
  name and shutdown.
- **TLS is each node's own, on port 443 only.** `ListenTLS` serves the
  certificate Tailscale obtains for the node's name (HTTPS certificates
  must be on in the tailnet). Neither node listens on another port.
  lodge serves **HTTP/1.1 only** on these listeners, because `ListenTLS`
  sets no ALPN. Browsers need HTTPS for a secure context.
- **Identity comes from the node.** For every request, lodge asks its own
  node who the peer is (`WhoIs` on the connection's remote address). A
  peer the node cannot name is refused. ADR-0005 builds sign-in on this.
- **Log upload is off before the node starts.** lodge calls
  `logtail.Disable()` before the first `tsnet.Server` starts, so nothing
  reaches Tailscale's log service. lodge's own logs go to its journal. A
  test runs a node against the in-process test control server and fails
  if anything dials Tailscale's log host.
- **The nodes' state lives in lodge's state directory,** in
  `/var/lib/lodge/tailnet/<node>/` (0700, owner `lodge`). It holds each
  node's private key.
- **The first start takes a tagged auth key** from `TS_AUTHKEY`, or for
  `lodge-content` from `TS_AUTHKEY_CONTENT` when that is set. The owner makes
  a tagged, pre-approved key, so the nodes belong to no user and their keys
  do not expire. Without a key, lodge logs Tailscale's login URL and waits.
  The docs prefer the key.
- **Plain HTTP is for loopback only.** `--listen ADDR` and
  `--content-listen ADDR` accept only a loopback address. Tests, the
  development build ([specs/lodge-http.md](../specs/lodge-http.md#listeners))
  and a device on lodge's own machine use them. A browser request on a plain
  listener has no tailnet identity, and is refused. The one exception is a
  binary built with the `e2e` tag.

## Consequences

- lodge's exposure is a property of the binary, not of a proxy's
  configuration. It serves nothing outside the tailnet, and nothing on the
  host's own addresses but loopback.
- lodge depends on `tailscale.com`, pinned. The userspace stack and
  WireGuard come with it, and the binary grows by tens of megabytes. A
  Tailscale security fix reaches lodge only in a lodge release, so
  dependency updates watch Tailscale's bulletins. The kit, which apps
  import, never depends on it.
- The tailnet gains two nodes, each with a name and a certificate.
  Certificates appear in public Certificate Transparency logs, so the
  nodes' names say nothing private.
- notes' devices move from the hub's old address to
  `https://LODGE/device/notes`. That is notes' relocation, in frostnotes'
  [ADR-0026](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0026-notesd-runs-as-a-lodge-app.md).
  Nothing changes on the user's machines without the user's go-ahead.
- `tsnet`'s userspace stack is slower than the kernel's. Large attachments
  take longer.
- lodge needs Tailscale's coordination service to start and to learn new
  peers. If the service is down, existing peers keep working.
- Packaging needs no `tailscaled` beside lodge, only lodge's state
  directory and, once, an auth key.

## Alternatives considered

- **`tailscale serve` on the host:** its identity headers cross whatever
  sits between serve and the backend, where any machine on that path can
  send its own. Tailscale's documentation says to trust them only from a
  backend on loopback.
- **Tailscale on the host, with `tailscale serve` to lodge on loopback or a
  0600 socket:** sound while the backend is reachable only from serve. But
  lodge could not check that the deployment stays so, Funnel would be one
  command away, and the headers name a user, not the device that ADR-0005
  allow-lists.
- **lodge on a `tailscaled` node's address, with WhoIs from the local
  API:** lodge would trust that packets from tailnet addresses arrive only
  through Tailscale's interface. That rests on the host's firewall, and
  fetching certificates would need operator rights on `tailscaled`.
- **A public lodge behind a reverse proxy:** rejected by the user. It would
  need passwords or passkeys, rate limits and internet-facing hardening.

## References

- Shapes: [specs/lodge-http.md](../specs/lodge-http.md#listeners), [design/overview.md](../design/overview.md), [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app)
- Builds on: [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md); frostnotes' M4 draft ADR-0026 (local commit `0586e2b`), and its review's finding 6 and nits
- Related: [ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md), [ADR-0010](0010-untrusted-bytes-come-from-a-second-node.md); frostnotes [ADR-0016](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0016-pair-devices-with-a-one-time-code-and-a-revocable-token.md) (devices' TLS)
