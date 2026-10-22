# frostyard/lodge

lodge is a tailnet-only web shell for frostyard's first-party apps:
frostnotes, frostmail, frostread and the personal-assistant agent Porter.
It gives them one address with a quick switcher, and each app runs in a
frame that stays loaded. lodge is the only process on the network. Each
app is a daemon on a Unix socket behind it. lodge also holds what the apps
share: the Go kit (`kit/`), the frontend library
`@frostyard/lodge-client` (`client/`), the shared look
`@frostyard/lodge-ui` (`ui/`), an MCP endpoint per app, and an inference
layer. Start at
[docs/README.md](docs/README.md). The architecture is in
[docs/design/overview.md](docs/design/overview.md), and the contract with
apps is [docs/specs/app-contract.md](docs/specs/app-contract.md). The
platform decision is frostyard/core's
[ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md).

This file (`AGENTS.md`) is the CANONICAL agent instructions — `CLAUDE.md`,
`GEMINI.md`, and `.github/copilot-instructions.md` are symlinks to it, and
`.claude/skills` symlinks to `.agents/skills/`
([ADR-0002](docs/adr/0002-agent-portable-instruction-surface.md)). Edit only
the canonical paths; keep content tool-agnostic.

## Status

Pre-release. The ADRs (2026-10-18 to 2026-10-20) were accepted on
2026-10-21. Phases 1 to 3 and 6 are built:
- the kit and the client, tagged up to `kit/v0.3.0` (with `netguard`) and
  `client/v0.3.2` (with the `rpc` subpath since `client/v0.3.0`);
- lodge v1, which is the gateway, the shell, the development build and
  packaging;
- the inference layer;
- lodge's side of the assistant: the agent socket and the companion,
  released in `kit/v0.2.0` and `client/v0.2.0`;
- the shared UI package, `@frostyard/lodge-ui`, tagged up to `ui/v0.6.0`
  (2026-10-22): the tokens, the React primitives, and frostyard's cold-blue
  accent since 0.5.0
  ([ADR-0018](docs/adr/0018-a-ui-package-beside-the-client.md) and
  [ADR-0019](docs/adr/0019-the-accent-is-frostyards-cold-blue.md), both
  Accepted).
- **frostread, the third app, is built and deployed on lodge's host on
  2026-10-21** ([its PRs](https://github.com/frostyard/frostread/pulls)); the
  repository is private until the owner opens it. Its reader is lodge's
  app `read`; its fetcher uses the kit's `netguard`
  ([ADR-0017](docs/adr/0017-the-kit-guards-outbound-fetches-against-inside-addresses.md)),
  and its web app is the first on the UI package
  ([ADR-0018](docs/adr/0018-a-ui-package-beside-the-client.md)).
- **Porter, the assistant, runs on lodge's host** on the agent socket and
  the companion (deployed from 2026-10-20;
  [its Deployments](https://github.com/frostyard/porter/blob/main/docs/plans/0001-roadmap.md#deployments)).
The checks on the user's devices and Phase 4 (mail, in frostmail) are in
progress. The order of work is the [roadmap](docs/plans/0001-roadmap.md). The kit is extracted from
[frostyard/frostnotes](https://github.com/frostyard/frostnotes) and
[frostyard/frostmail](https://github.com/frostyard/frostmail), which are
sibling checkouts at `../frostnotes` and `../frostmail` when present. Before
writing a lodge counterpart, read the file in those repositories that a
step names.

## Map

Everything in the map is built.

- `cmd/lodge`: the daemon. `cmd/lodgectl`: the CLI, over lodge's control
  socket. Built with the `e2e` tag, `cmd/lodge` is the development build
  (`make e2e-build`), whose fixed identity is `internal/e2eid` (every file
  tagged `e2e`; a test proves with `go tool nm` that releases lack it).
- `internal/tailnet`: the only package that imports `tailscale.com`. It
  covers the two tsnet nodes (`lodge` and `lodge-content`), their TLS
  listeners and WhoIs. `internal/peer` is WhoIs's answer, without
  Tailscale, for everything else.
- `internal/gateway`: sorts routes by kind, runs each kind's checks, sets
  the headers, and proxies to app sockets.
  - `internal/sessions`: browser sessions.
  - `internal/tokens`: MCP tokens.
  - `internal/allow`: the allow-lists of logins and nodes.
  - `internal/store`: lodge's SQLite database, which they live in.
  - `internal/apps`: manifests, bundles and health.
  - `internal/inference`: the inference socket, aliases, the queue,
    providers and the usage log ([specs/inference.md](docs/specs/inference.md));
    `responses.go` translates chat completions for a `codex` provider.
  - `internal/codexauth`: Sign in with ChatGPT for a `codex` provider:
    `lodgectl provider login`'s sign-in, the login file and its refresh
    ([ADR-0015](docs/adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md)).
  - `internal/copilotauth`: GitHub's device flow and the Copilot token
    exchange for a `copilot` provider
    ([ADR-0016](docs/adr/0016-copilot-uses-the-owners-copilot-plan-through-githubs-device-sign-in.md)).
  - `internal/agents`: the agents on lodge's host and their socket,
    `/run/lodge/agent.sock`, admitted by peer uid
    ([ADR-0014](docs/adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md)).
  - `internal/control`: the control socket that `lodgectl` uses.
  - `internal/apptest`: fake apps on sockets, for tests.
- `kit/`: the nested module `github.com/frostyard/lodge/kit` that app
  daemons import. It never imports `tailscale.com` or anything of lodge's
  outside `kit/` (checked by `internal/repocheck`), and every gate runs in
  it too.
  - `kit/lodgeapp`: the app-socket server (socket activation, the peer
    check, `LODGE_ROOT`), the `Lodge-*` caller, `/health`, `/api` as a
    `net.Conn` of JSON-RPC lines, and an agent's client of the agent
    socket (`AgentClient`).
  - `kit/manifest`: `app.json`, which lodge loads with too.
  - `kit/mcpsession`: binds MCP sessions to their token.
  - `kit/kittest`: plays lodge's side over a socket, and checks the
    contract's socket rules.
  - `kit/jsonrpc`: the apps' JSON-RPC runtime (server, router, client, the
    private socket), generic over each app's generated types, and
    `kit/rpctest`, its test harness
    ([ADR-0012](docs/adr/0012-the-kit-shares-the-rpc-runtime-not-the-generator.md)).
  - `kit/inference`: an app daemon's client of the inference socket
    (models, chat completions and responses, streamed or not).
  - `kit/netguard`: the outbound address guard: `Public`, `Control` (a
    dialer's check after DNS), `Dialer`, `Transport` and `Name`
    ([ADR-0017](docs/adr/0017-the-kit-guards-outbound-fetches-against-inside-addresses.md)).
- `client/`: `@frostyard/lodge-client`, which every app bundle includes.
  `ui/`: `@frostyard/lodge-ui`, the look the apps share: the design tokens,
  the Tailwind theme over them, the base layer, and the React primitives,
  pinned by `ui/vX.Y.Z`
  ([ADR-0018](docs/adr/0018-a-ui-package-beside-the-client.md)).
  `shell/`: the shell page's UI, on the client's shell half. All three are
  TypeScript, built in an nsl machine.
- `packaging/`: the systemd unit, sysusers.d and tmpfiles.d entries, the
  install script (`make dist`, `make install`) and an app's unit
  templates. `internal/packagecheck` tests them.
- `dev/`: the nsl machine's provisioning, `dev/e2e.sh` (the development
  build with three `dev/fakeapp` apps, one of them the companion, for
  smoke tests in a browser).
- `internal/repocheck`: tests of the repository's rules. Relative links
  resolve, every doc has an index line, no private host is named, and
  `kit/` keeps its import boundary.
  `internal/scaffoldcheck`: tests that the new-lodge-app scaffold renders,
  builds on this checkout's kit, passes the socket rules and stages
  safely. `internal/workflowcheck`: tests that actions are SHA-pinned with least
  privilege.

## Gates

- `make check` formats, then runs `make verify`: tidy (`go mod tidy
  -diff`), vet, gofmt, the pinned golangci-lint (`mise install`) and the
  tests. Run it before claiming any change complete, docs included, because
  `internal/repocheck` checks the docs' links and index. The `e2e`-tagged
  files are vetted, linted and tested with their tag too. `make ci` is
  verify plus `make race`, `make cross` (linux/amd64 and linux/arm64), and
  `make client-check` and `make shell-check` (nsl). CI runs the same
  targets.
- Tests run unfiltered (`go test ./...`, frostyard/core ADR-0038). A test
  that needs a real tailnet, the network or root skips unless
  `LODGE_LIVE_<WHAT>=1` is set, and never runs inside the gate.
- Never run pnpm, node or cargo on the host. `client/`, `ui/` and `shell/`
  build in the nsl machine `lodge` (`dev/nsl/provision.sh`), as frostnotes' app
  does.
- Fix a lint finding rather than loosening `.golangci.yml`.

## Invariants (decided; binding from the first line of code)

These are the app contract's rules
([specs/app-contract.md](docs/specs/app-contract.md#rules)):

- **Only lodge listens on the network.** App daemons listen only on Unix
  sockets.
- **An app socket accepts only lodge's uid** (`SO_PEERCRED`). It refuses a
  request without `Lodge-Caller`, except `GET /health`, and each path
  admits one caller kind. That is why apps can trust the `Lodge-*` headers.
  lodge deletes every `Lodge-*` header a client sends.
- **Each app runs as its own system user** and cannot read another app's
  state. Agents, the assistant included, reach an app only through
  `/mcp/<id>` with a token.
- **lodge serves every app's web files** from root-owned directories. No
  daemon serves code to the lodge origin.
- **Untrusted bytes come only from `lodge-content`,** under the sandbox
  policy.
- **A canonical URL, `/<id>/<route>`, keeps working** across app versions.
- **Provider credentials live only in lodge.** Apps never hold provider
  keys.
- **A contract-major mismatch disables that one app.** The others keep
  running.
- **lodge never parses an app's JSON-RPC.** It moves frames.
- **Identity comes from WhoIs alone.** No browser route writes. Issuing a
  token and allowing a login or a node happen only on the control socket.

## Skills (follow these for common tasks)

Step-by-step procedures live in [.agents/skills/](.agents/skills/); follow
them rather than improvising, whichever agent you are:

- [new-lodge-app](.agents/skills/new-lodge-app/SKILL.md): scaffold a new
  app behind lodge, in a new repository. It has a daemon on the kit that
  passes the contract's socket rules, a JSON-RPC API, a web bundle on
  lodge-client, and the manifest, units and staging.

## Go conventions (binding from the first line; frostnotes')

- Pure Go (`CGO_ENABLED=0`). `ctx` comes first. Errors are lowercase and
  wrapped with `%w`. Use `slog` for logs and `encoding/json/v2`. No mutable
  package state. Use modern idioms (`t.Context()`, `t.Chdir`, `wg.Go`,
  `omitzero`, `b.Loop`). Tests use temp dirs, and never sleep to
  synchronize.
- One package wraps each heavy dependency: `tailscale.com` only in
  `internal/tailnet`, and the MCP SDK only where the kit needs it.
- Downloads in scripts are version-pinned and checksum-verified
  (frostyard/core ADR-0023). Workflow actions are pinned to full commit
  SHAs, with `permissions: {}` at the top (ADR-0021, checked by
  `internal/workflowcheck`).

## Repository boundary

- **No private hosts.** Never commit the owner's host names, addresses,
  tailnet name, device names or the model server's address. Docs write
  `LODGE` (`lodge.<tailnet>.ts.net`), `CONTENT`
  (`lodge-content.<tailnet>.ts.net`), `<tailnet>.ts.net` and `MODEL_HOST`.
  Examples use `alice@example.com`, `phone` and `laptop`.
  `internal/repocheck` fails on a concrete `*.ts.net` name, a home-network
  name, or an IPv4 address outside loopback and the documentation ranges.
- Never commit `build/`, `node_modules`, tsnet state, auth keys, MCP tokens,
  provider credentials, `.envrc` or `.mcp.json`.
- Links into this repository from others use
  `https://github.com/frostyard/lodge/blob/main/<path>`. Links out use
  `https://github.com/frostyard/<repo>/blob/main/<path>`.
- Use conventional commit subjects.

## Documentation rules (enforced)

Docs live in `docs/` in four categories (frostyard/core ADR-0025). **Every
new doc starts from its category's `TEMPLATE.md`** and follows its
structure:

- `docs/adr/` — why we decided. Immutable once Accepted; reversals are new
  ADRs that mark the old one Superseded. lodge is pre-release: a Proposed
  ADR may still be edited in place.
- `docs/design/` — how it fits together. Living; updated in place to match
  reality.
- `docs/specs/` — exact contracts. Change only alongside implementing code.
- `docs/plans/` — order of work. Phases with "Done when" outcomes.

### Cross-linking is mandatory

A doc without its required links is incomplete — do not finish a docs change
until they exist, in both directions:

- **ADR** → links every design doc/spec it shapes, and prior ADRs it builds on.
- **Design doc** → links the ADR(s) providing its rationale, the spec(s)
  pinning its contracts, and the roadmap phase that builds it.
- **Spec** → links its motivating ADR(s) and the design doc showing where it
  fits.
- **Plan** → every phase links the design docs/specs it implements; resolved
  open questions become ADRs.

When you touch a doc, verify its links still hold (targets exist, section
anchors valid) and add the back-links on the targets. Use relative paths.
`make check` fails on a relative link whose file or heading is missing.

### Housekeeping

- New doc ⇒ add a line to the index in [docs/README.md](docs/README.md).
- New significant decision ⇒ new ADR *first*, then update the affected design
  docs/specs in the same change.
- Convert relative dates ("next weekend") to absolute dates in all docs.
