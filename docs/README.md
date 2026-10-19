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
- 0003 — lodge is the only process on the network
- 0004 — lodge stays on the tailnet, and joins it itself
- 0005 — Browsers sign in with their tailnet identity
- 0006 — Agents reach each app through its own MCP endpoint
- 0007 — lodge serves every app's web build from disk
- 0008 — lodge installs as one home-screen app
- 0009 — Apps switch in frames that stay loaded
- 0010 — Untrusted bytes come from a second node
- 0011 — A shared inference layer routes apps to models

### Design

- Overview — the nodes, the gateway, the shell, the apps behind it, inference, users and paths, operations

### Specs

- App contract — what an app installs and serves, and what lodge provides: layout, manifest, URLs, caller headers, inference, MCP tokens, rules
- lodge's HTTP surface — listeners, routes by kind, browser, content, agent and device checks, headers, sessions, tokens and allow-lists, `lodgectl`
- lodge-client — the postMessage protocol between the shell and an app's frame, and the library's API
- The shell — the shell page: rail, switcher and its shortcut, frames, addresses and history

### Plans

- 0001 — Roadmap — the kit, lodge v1 with notes, inference, mail, the assistant

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
