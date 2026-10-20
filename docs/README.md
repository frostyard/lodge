# Documentation

Docs are split by the question they answer (frostyard/core ADR-0025):

| Directory | Question | Contents |
| --- | --- | --- |
| [adr/](adr/) | **Why** did we choose this? | Architecture Decision Records — immutable once accepted; superseded, never edited |
| [design/](design/) | **How** does it fit together? | Living documents describing the current architecture |
| [specs/](specs/) | **What exactly** is the contract? | Precise, testable interface definitions |
| [plans/](plans/) | **When/in what order** do we build? | Roadmaps and phase plans; updated as work lands |

Org-wide decisions that bind this repository are in
[org-adrs.md](org-adrs.md). The platform decision behind lodge is
frostyard/core's
[ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md).

## Index

### Decisions (ADRs)

- [0001 — Record architecture decisions](adr/0001-record-architecture-decisions.md)
- [0002 — Agent-portable instruction surface](adr/0002-agent-portable-instruction-surface.md)
- [0003 — lodge is the only process on the network](adr/0003-lodge-is-the-only-process-on-the-network.md)
- [0004 — lodge stays on the tailnet, and joins it itself](adr/0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md)
- [0005 — Browsers sign in with their tailnet identity](adr/0005-browsers-sign-in-with-their-tailnet-identity.md)
- [0006 — Agents reach each app through its own MCP endpoint](adr/0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)
- [0007 — lodge serves every app's web build from disk](adr/0007-lodge-serves-every-apps-web-build-from-disk.md)
- [0008 — lodge installs as one home-screen app](adr/0008-lodge-installs-as-one-home-screen-app.md)
- [0009 — Apps switch in frames that stay loaded](adr/0009-apps-switch-in-frames-that-stay-loaded.md)
- [0010 — Untrusted bytes come from a second node](adr/0010-untrusted-bytes-come-from-a-second-node.md)
- [0011 — A shared inference layer routes apps to models](adr/0011-a-shared-inference-layer-routes-apps-to-models.md)
- [0012 — The kit shares the RPC runtime, not the generator](adr/0012-the-kit-shares-the-rpc-runtime-not-the-generator.md)
- [0013 — The companion opens beside the current app](adr/0013-the-companion-opens-beside-the-current-app.md)
- [0014 — Agents on lodge's host use a local agent socket](adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md)
- [0015 — Codex uses the owner's ChatGPT plan through Sign in with ChatGPT](adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md)

### Design

- [Overview](design/overview.md) — the nodes, the gateway, the shell and its companion, the apps behind it, agents, inference, users and paths, operations

### Specs

- [App contract](specs/app-contract.md) — what an app installs and serves, and what lodge provides: layout, manifest, URLs, caller headers, inference, MCP tokens, the agent socket, rules
- [lodge's HTTP surface](specs/lodge-http.md) — listeners, routes by kind, browser, content, agent and device checks, the agent socket, headers, sessions, tokens and allow-lists, `lodgectl`
- [lodge-client](specs/lodge-client.md) — the postMessage protocol between the shell and an app's frame, the companion's messages, and the library's API
- [The shell](specs/shell.md) — the shell page: rail, switcher and its shortcut, frames, addresses and history, the companion
- [The inference layer](specs/inference.md) — lodge's side of the inference socket: the caller, providers, aliases and allow-lists, the queue, credentials, availability and the usage log

### Plans

- [0001 — Roadmap](plans/0001-roadmap.md) — the kit, lodge v1 with notes, inference, mail, the assistant's design, the companion and the agent socket

## Conventions

- **New docs start from their category's `TEMPLATE.md`** (in each directory).
- New decision → new ADR with the next number; if it reverses an old one, mark
  the old one `Superseded by NNNN` rather than editing it.
- Design docs are updated in place to always reflect reality.
- Specs change only alongside the code that implements them.
- Cross-links between categories are mandatory in both directions — see the
  documentation rules in [AGENTS.md](../AGENTS.md) (CLAUDE.md/GEMINI.md are
  symlinks to it, ADR-0002).
- Adding a doc means adding it to the index above. `make check` fails on a
  doc without an index line, and on a relative link that does not resolve.
