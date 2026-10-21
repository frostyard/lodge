# 0017 — The kit guards outbound fetches against inside addresses

- **Status:** Proposed
- **Date:** 2026-10-21

## Context

Frostmail fetches one URL of its own choosing: a one-click unsubscribe
request ([ADR-0027](https://github.com/frostyard/frostmail/blob/main/docs/adr/0027-unsubscribe-with-one-click.md)).
That request must never reach the owner's own network, so frostmail checks
every address its dialer is about to connect to, after DNS, against a list
of inside ranges. The check lives in frostmail's own package, with a test
for each range.

Frostread, a new feed reader app, fetches what a feed names: the feed, the
pages a feed's posts link to, and the images in them. Those URLs come from
whoever wrote the feed, and the daemon that fetches them runs on lodge's
host, as its own system user. That host sits on the owner's home network
and on the tailnet. A URL is therefore a request to make the host fetch
something: a loopback service, the router, a printer, a NAS, the model
server, another app's socket, or a cloud metadata endpoint. The same
fetch would let whoever wrote a feed map the network behind it.

On 2026-10-21 the owner decided that the reader never fetches a homelab,
private, loopback, link-local or tailnet address in version 1, and that the
guard both apps need is one shared piece of code, not one copy each.

## Decision

**The kit gains `kit/netguard`, and every app that fetches a URL an outsider
names uses it.** It is standard library only, and its API is small:

- `Public(ip netip.Addr) error` returns nil for a public unicast address,
  and an error wrapping the exported `ErrInside` for every other. It refuses
  loopback, the private ranges (RFC 1918, and the IPv6 unique local
  addresses `fc00::/7`, which hold the tailnet's), link-local unicast and
  multicast, interface-local and other multicast, the unspecified address,
  an address with a zone, and these ranges: "this network" (RFC 1122),
  CGNAT (RFC 6598, which holds the tailnet's IPv4), the IETF protocol
  assignments, the benchmarking range, the three documentation ranges
  (RFC 5737), the reserved block and the limited broadcast address.
  - It refuses the IPv6 NAT64 well-known and local-use prefixes, Teredo,
    the documentation prefix, 6to4 and the deprecated site-local prefix
    whole, because those embed an IPv4 address that a check by prefix does
    not judge.
  - An IPv4-mapped IPv6 address is judged as the IPv4 address it holds.
- `Control` is a `net.Dialer.Control`. It parses the `host:port` the dialer
  is about to connect to, refuses anything that is not a literal IP
  address, and calls `Public`. It therefore runs **after DNS**, on each
  address the dialer tries, so a name that resolves inside, and a name that
  rebinds between two lookups, are both refused.
- `Dialer(timeout)` and `Transport(timeout)` are a dialer with `Control`,
  and an HTTP transport that dials through it, with **no proxy**: an
  environment proxy would make the connection itself and bypass the guard.
- Because the check is on each dial, **a redirect to an inside address is
  refused**: the guard does not judge the first URL, it judges every
  connection.
- `Name(host)` is a cheap refusal before any lookup: `localhost`, the names
  under it, and the names reserved for a home network, a private network
  and the tailnet, and a name with no dot. It is defence in depth only.
  `Control` is the fence, and its doc comment says so.
- **The caller refuses every scheme but http and https.** A `file:` or
  `ftp:` URL never reaches a dialer the guard wraps, so the guard cannot
  help there.

The kit's existing rules hold: no `tailscale.com`, no lodge `internal/`,
standard library only for this package.

## Consequences

- One guard, reviewed and tested once, for frostmail, frostread and every
  app after them. A fix to a range lands for all of them.
- Frostmail can move to `kit/netguard` in a later change and delete its own
  copy. Until then there are two copies, and they must be kept in step; the
  ranges here are a superset of frostmail's.
- **A homelab feed cannot be read in version 1**: a feed on the owner's own
  network is refused, by design. So is a public feed whose image sits on a
  NAS, and so is a public page that redirects to an inside address. This is
  a feature, not a defect, and the reader's error message must say so.
- The IPv6 ranges that embed IPv4 are refused whole, so a public IPv4
  address reached through NAT64 is refused too. An app that must follow
  such a URL (a host with only NAT64) needs a later decision.
- The guard is a dial-time check, so it does not bound what a fetch reads
  or how long it takes. Callers still set a timeout, read no more than a
  fixed limit, and resolve nothing themselves.
- An app that fetches from inside on purpose — its own daemon, a homelab
  service the owner named — must not use this transport.

## Alternatives considered

- **A copy per app:** the cheapest for the second app, and the reason this
  ADR exists. Two copies drift, each needs its own SSRF review, and the
  third app copies whichever it finds first.
- **A check by name, before DNS:** a name says nothing about the address;
  a public name resolves wherever its zone says, and a rebinding name
  changes its answer between the check and the connection. Refused.
- **A check of the URL's host in each handler:** the same rebinding hole,
  and it misses redirects. Refused.
- **An allow-list proxy on another host:** the strongest fence, and a
  second process, its configuration, its own network and its credentials
  for version 1. Not now; this ADR does not prevent it later.
- **Allowing the homelab by a list of names:** the owner chose not to in
  version 1: the reader's input is untrusted, and a list of names makes the URL
  parser the policy.

## References

- Shapes: [design/overview.md](../design/overview.md#the-kit-and-the-client)
- Builds on: [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md) — the gateway is the network edge, and an app daemon must not become a second way into the network.
