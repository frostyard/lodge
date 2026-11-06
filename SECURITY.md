# Security policy

lodge is the only process of frostyard's first-party apps that listens on
the network. It signs browsers in by their tailnet identity, issues the
agents' MCP tokens, holds the inference providers' credentials, and serves
untrusted bytes from a second node under a sandbox policy. Treat any report
that touches the tailnet identity and the allow-lists, browser sessions,
MCP tokens, the `Lodge-*` headers, the app, agent, inference or control
sockets, provider credentials, the `lodge-content` node, or the kit's
socket peer check or outbound address guard (`kit/netguard`) with care.

## Supported versions

lodge is pre-release. Fixes go to `main` and ship in the next release of
each series that the fix touches. Only the latest of each series gets
fixes; there are no backports to older releases:

- lodge itself: the latest `vX.Y.Z` release and its
  `frostyard-lodge` `.deb`;
- the Go kit: the latest `kit/vX.Y.Z` tag;
- `@frostyard/lodge-client` and `@frostyard/lodge-ui`: the latest
  `client/vX.Y.Z` and `ui/vX.Y.Z` tags.

## Reporting a vulnerability

Report it privately, through GitHub's private vulnerability reporting for
this repository: <https://github.com/frostyard/lodge/security>
("Report a vulnerability").

Do not open a public issue for a vulnerability. Never post auth keys, MCP
tokens, provider credentials, host names, tailnet names or logs that hold
them.

## What to include

- the version (`lodge --version` or `lodgectl version`), the kit, client or
  UI tag, or the commit;
- where it runs: an installed `.deb`, a `make install`, or the development
  build (`make e2e-build`, loopback only);
- the impact, and what you expected instead;
- the steps to reproduce it, with made-up names (`alice@example.com`,
  `phone`, `laptop`).
