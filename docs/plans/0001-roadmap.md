# Plan: Roadmap

What lodge delivers, in order, from the shared kit to the assistant's
design and lodge's side of it. lodge is the platform frostyard/core
[ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
decided on. frostnotes' M4 ([plan 0006](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0006-m4-notes-inside-lodge.md))
runs notes inside lodge, and depends on Phases 1 and 2. frostmail's move
is Phase 4.

Status (2026-10-21):
- **The ADRs were accepted on 2026-10-21.** Phases 1 to 3 were built
  ahead of that, on the user's go-ahead of 2026-10-18.
- **Phase 1 is done.** The kit, with the RPC runtime extracted from the
  apps, and the client are tagged (`kit/v0.1.0`, then `kit/v0.2.0` and
  `client/v0.2.0`), and notesd and maild run on the kit.
- **Phase 2's code is merged, and lodge runs on its first host.** Notes
  moves behind it in frostnotes' plan 0006 Phase 7, from 2026-10-26.
  Lodge's security review ran on 2026-10-19; its findings, fixes and tests
  are recorded in [Phase 2](#phase-2--lodge-v1-with-notes-as-its-only-app).
  The inference layer and the kit's later pieces landed after it. Claude Code's
  two checks passed on 2026-10-19; the other checks on the user's devices
  are still to come.
- **Phase 3 is merged,** and the `local` provider is reachable from lodge's
  host. The Codex adapter is built on Sign in with ChatGPT's plan usage
  ([ADR-0015](../adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md),
  2026-10-20). The Copilot adapter is built on the owner's Copilot plan
  ([ADR-0016](../adr/0016-copilot-uses-the-owners-copilot-plan-through-githubs-device-sign-in.md),
  Proposed, 2026-10-21), after the owner checked its terms.
- **Phase 4:** the new-app skill is built. Mail's side is frostmail's
  [plan 0011](https://github.com/frostyard/frostmail/blob/main/docs/plans/0011-m8-mail-in-lodge.md),
  in progress.
- **Phase 5 is designed** (2026-10-19). The assistant is Porter. lodge's
  side is [ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md)
  and [ADR-0014](../adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md),
  built in Phase 6. Porter's own ADRs and design doc are in
  [frostyard/porter](https://github.com/frostyard/porter).
- **Phase 6 is built** (2026-10-19): the agent socket, with tokens bound
  to their route and the kit's client of it, and the companion, with the
  shell's panel and sheet, its button and chord, `context` and `ask`, and a
  fake companion in the development build. The agent socket was checked
  on lodge's host on 2026-10-19, with Porter's notes and mail tokens
  (Porter's roadmap, Phase 1). The companion's checks on the user's
  desktop and phone wait for the deployment.

Nothing changes on the user's machines without the user's go-ahead.

## Phase 1 — The kit

The Go kit and the frontend library that every app builds on, extracted
from what frostnotes and frostmail already share.

- **`kit/`, the nested module `github.com/frostyard/lodge/kit`.** It has its
  own `go.mod` at lodge's Go version, and lodge's gates run in both modules.
  A repository check fails when `kit/` imports `tailscale.com` or anything
  under lodge's `internal/`.
- **Extracted, after comparing both copies** (frostnotes' `api` runtime and
  `internal/rpcserver`, and frostmail's):
  - the JSON-RPC 2.0 connection and server. A panicking handler answers
    `internal` and logs "request panicked" with the method and stack, which
    both copies now do (frostmail's PR 58 ported frostnotes' fix). A reply
    over the message limit answers `internal` instead of being written:
    frostnotes' `conn.write`, test `TestAReplyTooLargeAnswersInternal`,
    which frostmail still lacks;
  - the router with caller kinds and method-level callers, where a method a
    caller may not use answers `methodNotFound`;
  - `rpctest`, a real server for tests. It takes frostmail's form, an
    `Options.Services func(*api.Services)` hook that can replace any domain,
    rather than frostnotes' field per domain;
  - the contract generator (`tools/rpcgen`), if the two copies converge,
    and frostmail's Windows layer for the private socket, if frostmail's
    local maild needs it from the kit. Both stay in the apps
    ([ADR-0012](../adr/0012-the-kit-shares-the-rpc-runtime-not-the-generator.md)):
    the generators differ in what a schema means, and the kit's runtime is
    generic over each app's generated types, so neither generator changes.
- **New in the kit,** each from
  [app-contract.md](../specs/app-contract.md):
  - the app-socket server: socket activation, the peer check, and
    `LODGE_ROOT` with the development uid;
  - the `Lodge-*` caller, parsed into the request's context, with one
    caller kind per path;
  - `/health`, and the manifest type that lodge loads with too;
  - the **MCP-session helper**. It binds a session to its token name and
    scope, caps sessions per token name with least-recently-used eviction,
    ends a session idle for an hour, and answers 404 on a mismatch;
  - `kittest`, which plays lodge's side over a socket in a temp root.
- **`client/`, `@frostyard/lodge-client`:** the app half and the shell half
  of [lodge-client.md](../specs/lodge-client.md). It is built and tested in
  an nsl machine, with its toolchain pinned and verified (frostyard/core
  ADR-0023).
- **Published and pinned.** Apps pin both to a published lodge commit or
  release, never to a local path. Once the user approves publishing the
  repository, the kit is tagged `kit/v0.1.0`, and the client is released at
  a lodge tag.
- **Done when:**
  - a published, tagged `lodge/kit` and `@frostyard/lodge-client` exist;
  - notesd and maild each serve their socket API through the pinned kit,
    with their own copies of the connection code removed and both repos'
    `make check` green;
  - a test app built on the kit and the client passes the contract's
    socket rules against `kittest`.

## Phase 2 — lodge v1 with notes as its only app

lodge itself, with frostnotes' M4 moving notes behind it. Implements
[lodge-http.md](../specs/lodge-http.md),
[shell.md](../specs/shell.md), lodge's side of
[app-contract.md](../specs/app-contract.md) and
[lodge-client.md](../specs/lodge-client.md), and
[design/overview.md](../design/overview.md).

- **`internal/tailnet`:** the two nodes, TLS on 443 and HTTP/1.1 only,
  `logtail.Disable()` before start with a test that nothing dials the log
  host, and the auth-key first start.
- **`internal/gateway`:** routes by kind; the browser, content, agent and
  device checks; the headers and policies; app files; proxying. Every check
  has a failing test.
- **`internal/allow`, `internal/sessions`, `internal/tokens`,
  `internal/apps` and `internal/control`, with `cmd/lodgectl`:** the
  allow-lists, sessions with their liveness, limits and re-checks, tokens
  with immediate revocation, manifests and health, and the control socket.
- **`shell/`:** the shell page, the switcher on Ctrl+\`, frames, history,
  app states, lodge's web manifest and icon.
- **The development build:** `make e2e-build` builds `build/lodge-e2e` with
  the `e2e` tag. It runs on loopback with one fixed identity, and defines
  `LODGE`, `Origin` and `connect-src` there. Apps test against it, notes'
  Firefox runs among them. A test proves with `go tool nm` that release
  builds hold none of it.
- **Deployment.** lodge's first host is the virtual machine that runs the
  notes hub today, and notes moves in place: notesd as user `frostnotes`
  behind lodge, with devices relocating to `https://LODGE/device/notes`
  (frostnotes' plan 0006). The minimum to deploy is systemd units for lodge
  and notes' socket, `sysusers.d` entries and the install layout.
- **lodge's security review,** before notes moves behind it. Each finding
  gets a test, as M4's did.
  **Result (2026-10-19):** an independent, read-only review of lodge and
  of notes' and mail's lodge sides, at the refs before Phase 2 merged. No
  HIGH finding. Each finding, its fix and its test:

  | Finding | Fix | Test |
  | --- | --- | --- |
  | M1 (notes) notes' MCP handler leaked notesd connections | frostnotes' [plan 0006](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0006-m4-notes-inside-lodge.md#phase-6--security-review-planner-an-independent-agent) | `TestASessionHoldsOneConnectionUntilItEnds` |
  | M2 unbounded connections, headers and device bodies | `1c7e23c`: 64 connections per address and 1,024 in all, 64 KiB of headers, `Connection: close` on refusals, 30 s per read of a device body, 128 connections to an app; notes' part accepted by the user (2026-10-21) | `TestLimitPerPeer`, `TestLimitTotal`, `TestRefusalsCloseTheConnection`, `TestHeaderLimit`, `TestDeviceBodyDeadline` |
  | L1 device and agent responses carried no policy | `1c7e23c`: `default-src 'none'; sandbox`, `frame-ancestors 'none'`, `X-Frame-Options` | `TestDeviceAndAgentResponsesAreConfined` |
  | L2 an app's `script-src` covered its `x/` reads; upgrades on `x/` | `1c7e23c`: scripts and styles from `assets/` only; upgrades refused off the session and device routes | `TestUpgradesRefusedOutsideSessionsAndDevices` |
  | L3 nothing checked who serves an app socket | `1c7e23c`: the socket's owner and peer must be the manifest's user; `/run/lodge/apps` must be root's | `TestSocketPeerMustBeTheManifestUser`, `TestManifestUserMustExist`, `TestCheckSocketDir` |
  | L4 no packaging | `packaging/` (`f7f7880`), with the auth key removed after the first start | `internal/packagecheck` |
  | L5 content named by a bare hash was an oracle to the tailnet | notes names content by a MAC under its own key (frostnotes' [plan 0006](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0006-m4-notes-inside-lodge.md#phase-6--security-review-planner-an-independent-agent)); [lodge-http.md](../specs/lodge-http.md#content-routes) says that same-site is the whole tailnet | `TestContentServesBlobsWithinTheirLimits`, `TestTheContentKeyIsKept` (notes) |
  | N1 revocation re-checked a stream by the token's name | `1c7e23c`: by the token's row | `TestMCPRevocationIsByRow` |
  | N2 the login URL went to the journal | `1c7e23c` | `TestLoginURLNeverLogged`, `TestLoginNeededLoggedWithoutKey` |
  | N3 device requests named the peer without the own-address check | `1c7e23c` | `TestDevice` |
  | N4 the content policy lacked `base-uri` and `form-action` | `1c7e23c` | `TestContentChecks`, `TestContentLinksPolicy` |
  | N5 (notes) `POST /sync/hub` was an oracle and a log flood | frostnotes' [plan 0006](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0006-m4-notes-inside-lodge.md#phase-6--security-review-planner-an-independent-agent) | `TestTheRelocationProof`, `TestAPeerHasItsBucketOfRequests` (notes) |
  | N6 (notes) any peer could fill the pairing slots | frostnotes' [plan 0006](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0006-m4-notes-inside-lodge.md#phase-6--security-review-planner-an-independent-agent) | `TestANodeMayHaveTwoPairingsWaiting` (notes) |
  | N7 (kit) MCP sessions were swept only when a request came | `2017aec`: a ticker | the kit's `mcpsession` tests |
  | N8 the `nm` check missed the shipped, stripped binary | `1c7e23c` | `TestShippedBuildHoldsNoE2EIdentity` |

  It found sound: identity by WhoIs alone, the browser checks, content
  isolation, tokens and revocation, the control socket and the e2e hook.
  Its report stays outside the repository, because it names local paths.
  The inference layer, the agent socket, the companion and the Codex
  sign-in came after it.
- **Checks on the user's devices and clients:**
  - whether WebKit on iOS sends `Sec-Fetch-Site` on a WebSocket handshake
    (M4 review M3);
  - that Claude Code sends no `Origin` to `/mcp/notes` (L10), and opens a
    new session after a 404 (L11). **Both held on 2026-10-19,** with Claude
    Code 2.1.300 against `/mcp/notes` and `/mcp/mail`:
    - It sends no `Origin` and no `Sec-Fetch-*`.
    - When its session was evicted, its stream and its next POST got 404.
      It then initialized a new session and retried the tool, which
      succeeded.
    - It first probes MCP 2026-07-28's `server/discover`. The kit answers
      that 400, and Claude Code falls back to `initialize`;
  - Chrome's viewer with a sandboxed PDF in a new tab (review item 12), and
    images and media in a new tab. The PDF exception in lodge-http.md is
    decided here;
  - that nothing reaches Tailscale's log host in the live run (M6);
  - that Add to Home Screen opens standalone, with `viewport-fit=cover` and
    the safe area (L13);
  - the narrow layout's bar on the phone.
- **Done when:**
  - on the user's phone, lodge opens from the home screen, and
    `https://LODGE/notes/<vault id>/<path>` opens that note in the shell;
  - on a desktop browser, Ctrl+\` and then `n` opens notes;
  - Claude Code on a laptop reads and writes notes through `/mcp/notes`
    with a scoped token, and revoking the token cuts it off at once;
  - a notes device syncs through `https://LODGE/device/notes`, and an
    attachment opens from `lodge-content`, sandboxed;
  - every check in lodge-http.md has a failing test, and the review's
    findings are closed;
  - the development build runs on loopback with its fixed identity as a
    build apps can test against, and the `go tool nm` test proves release
    builds lack it.

## Phase 3 — The inference layer

Implements [app-contract.md's inference](../specs/app-contract.md#inference),
[inference.md](../specs/inference.md)
([ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md))
and [design/overview.md](../design/overview.md#inference).

- **`internal/inference`:**
  - the socket, with the peer's uid mapped to an app;
  - aliases from `/etc/lodge/config.json`, and per-app allow-lists that
    start open;
  - the priority queue, keeping one slot for interactive work;
  - provider credentials, with the client's `Authorization` dropped;
  - the usage log, and `lodgectl usage`.
- **The `local` provider:** the local model at `MODEL_HOST`
  (OpenAI-compatible, 4 slots, 262k context, thinking control, no vision).
- **Reachability.** The lodge host must reach each configured provider. A
  local model on another host needs a network path: a firewall rule, a
  listener or the tailnet. lodge checks it at start, and shows a provider
  as unavailable when it cannot be reached. Opening that path changes
  another machine, and waits for the user's go-ahead.
- **Copilot and Codex:** check each provider's terms for subscription
  logins used from third-party software. Build an adapter only for a
  provider that allows it, and drop the rest.
  - **Codex: built (2026-10-20).** OpenAI offers ChatGPT plan usage to
    open-source and locally hosted apps through Sign in with ChatGPT
    ([ADR-0015](../adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md);
    [specs/inference.md](../specs/inference.md#the-codex-provider)).
  - **Copilot: built (2026-10-21).** The owner checked Copilot's terms on
    2026-10-21 and found this use acceptable. lodge uses the Copilot API
    that the editors use, signed in with GitHub's device flow
    ([ADR-0016](../adr/0016-copilot-uses-the-owners-copilot-plan-through-githubs-device-sign-in.md);
    [specs/inference.md](../specs/inference.md#the-copilot-provider)).
    `TestCopilotLive` checks it against the owner's plan.
- **The kit's inference client**, with `Lodge-Priority`.
- **Done when:**
  - an app daemon streams a chat completion through `local`;
  - while one app's background requests hold every slot they may, another
    app's interactive request starts at once;
  - the usage log shows both requests, with token counts and no content;
  - an unreachable provider shows as unavailable, and its alias fails fast.

## Phase 4 — Mail in lodge

maild becomes lodge's `mail` app: one maild for every client. Implements
[app-contract.md](../specs/app-contract.md) on mail's side, in frostmail.

- **maild under user `frostmail`,** on the kit, with its manifest:
  `content`, `mcp`, and `key: "m"`. frostmail keeps a local maild for
  installs outside the owner's tailnet, such as a Windows install, and
  supports both a local and a remote maild.
- **mail's bundle, built for `/app/mail/`.** The reader frame comes from
  `lodge-content`, with HTML parts sandboxed and images embedded, as
  frostmail's
  [ADR-0014](https://github.com/frostyard/frostmail/blob/main/docs/adr/0014-images-embedded-in-the-reader-frame.md)
  has it.
- **`/mcp/mail`,** with a scope grammar of frostmail's.
- **Links between apps:** notes link to mail at its permanent route,
  `/mail/message/<Message-ID>` (the Message-ID percent-encoded as one
  segment, which maild resolves; frostmail's plan 0011), and
  mail links back to notes.
- **The new-app skill:** [`.agents/skills/new-lodge-app/`](../../.agents/skills/new-lodge-app/SKILL.md),
  a thin scaffold on the kit and the client, drawn from what notes and mail
  needed (ADR-0057). Built on 2026-10-19, ahead of the phase. Its
  daemon is on `kit/lodgeapp` and `kit/jsonrpc`, its bundle on
  `@frostyard/lodge-client`, and its staging is frostmail's. Its JSON-RPC
  methods are written by hand until the app adopts a generator
  ([ADR-0012](../adr/0012-the-kit-shares-the-rpc-runtime-not-the-generator.md)).
  `internal/scaffoldcheck` renders it and runs the contract's socket rules
  on the result.
- **The narrow layout's bar** is settled, now that there are two apps to
  switch between.
- **Done when:**
  - mail and notes switch in the shell on the phone and on a desktop
    browser;
  - a note's link opens a thread, and the thread's link opens the note;
  - an HTML mail renders in a `lodge-content` frame, with no script and no
    remote loads;
  - the skill scaffolds an app that passes the contract's tests against
    `kittest`.

## Phase 5 — The assistant's design

Design only. The assistant is **Porter**: app `porter`, daemon `porterd`,
user `frostporter`, in its own repository, frostyard/porter. Its own
decisions are there: Porter's
[ADR-0003](https://github.com/frostyard/porter/blob/main/docs/adr/0003-porter-is-a-lodge-app-that-owns-no-data.md)
(a lodge app that owns no data) and its
[design/overview.md](https://github.com/frostyard/porter/blob/main/docs/design/overview.md).
lodge's part is below. Phase 6 builds it, and Porter is built on it.

- **The companion**
  ([ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md)).
  Porter's chat panel is lodge's companion, opened beside whatever app is
  current, with that app's `context`. "Chat inside an app" is that panel,
  so there are no chat panels per app. The layout and keys are
  [shell.md](../specs/shell.md#the-companion), and the messages, `ask`
  among them, [lodge-client.md](../specs/lodge-client.md#the-companion).
- **Its tools through `/mcp/<id>`, with one token per app,** each scoped by
  the user. Porter owns none of the user's data. For it, notes' scope gains
  a folder limit (frostnotes
  [ADR-0028](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0028-an-agents-scope-can-limit-writes-to-folders.md),
  built in its [plan 0007](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0007-folder-scopes-for-agents.md)),
  and mail gains tools for calendars, tasks and contacts (frostmail
  [plan 0012](https://github.com/frostyard/frostmail/blob/main/docs/plans/0012-calendar-tasks-and-contacts-for-agents.md)).
- **Its identities:** a system user, `frostporter`, and scoped tokens,
  never the account that owns the data. It reaches `/mcp/<id>` on lodge's
  local agent socket
  ([ADR-0014](../adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md)),
  with no tailnet node of its own. Tagged nodes stay the rule for agents
  off lodge's host
  ([ADR-0006](../adr/0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)).
- **What it may do unattended** is decided in Porter's docs: its
  [ADR-0004](https://github.com/frostyard/porter/blob/main/docs/adr/0004-porter-drafts-and-the-owner-sends.md) (Porter
  drafts, and the owner sends) and
  [ADR-0005](https://github.com/frostyard/porter/blob/main/docs/adr/0005-memory-and-routines-live-in-the-vault.md) (memory
  and routines live in the vault, with
  [specs/routines.md](https://github.com/frostyard/porter/blob/main/docs/specs/routines.md)). Unattended, it writes only
  mail drafts and notes in its own folder, and everything else waits for
  the user's approval. No token of its has mail's `;send` in v1.
- **Inference:** only through lodge's inference socket, with the aliases
  that exist, `local` and `smart`, and no alias of its own. Chat asks for
  `interactive` priority, and its scheduled routines for `background`.
- **Notifications:** none in v1. Porter is pull, not push: its morning
  brief waits as a note, so Web Push stays deferred
  ([open questions](#open-questions)).
- **The companion, or chat panels per app:** decided, the companion
  (ADR-0013).
- **Done when:** the assistant's ADRs and design doc are Proposed, and
  lodge-client's companion messages, the shell's companion layout and the
  agent socket are specified. **Met on 2026-10-19,** with the specs marked
  not built until Phase 6.

## Phase 6 — The companion and the agent socket

lodge's side of the assistant, built from Phase 5's design before Porter
is. Implements [shell.md](../specs/shell.md#the-companion),
[lodge-client.md](../specs/lodge-client.md#the-companion),
[app-contract.md](../specs/app-contract.md#the-agent-socket) and
[lodge-http.md](../specs/lodge-http.md#the-agent-socket)
([ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md),
[ADR-0014](../adr/0014-agents-on-lodges-host-use-a-local-agent-socket.md)),
and design/overview.md's [shell](../design/overview.md#the-shell) and
[agents](../design/overview.md#agents).

- **`client/`:**
  - the message types: `init`'s `role` and `companion`, `ask` both ways,
    and the shell's `context` to the companion;
  - the app half: the companion's chord caught like the switcher's,
    history kept in memory in the companion's frame, and `lodge.role`,
    `lodge.companion`, `lodge.ask()`, `on("context")` and `on("ask")`;
  - the shell half keys its ports by frame rather than by app, so one app
    can have two frames.
- **`shell/`:** the companion's frame, the panel from 600 pixels wide and
  the sheet under it, the button on the rail and the bar, the chord,
  `lodge:companion`, context forwarding, `ask`, and the panel hidden while
  the companion app is current.
- **`internal/apps`:** at most one companion, with the reason in
  `lodgectl apps` when two manifests claim it.
- **The agent socket:** the socket and its group, the `agents`
  configuration, the peer check, and `/mcp/<id>` on it with the tailnet's
  proxying, log and revocation.
- **Tokens bound to their route:** `lodgectl token issue --agent`, the
  agent user in `lodgectl tokens`, and a migration of lodge's database.
- **`kit/`:** an HTTP client that dials the agent socket, honoring
  `LODGE_ROOT` (`lodgeapp.Env.AgentClient`).
- **Packaging:** group `lodge-agents` in `sysusers.d`, with lodge in it,
  and `agents` in `config.example.json`.
- **The development build:** `dev/e2e.sh` runs a fake app as the
  companion, for trying the panel and the sheet in a browser.
- **Done when:**
  - on a desktop browser, Ctrl+Shift+\` opens the companion beside notes,
    and it is told the open note's context;
  - an app's `ask` opens the companion with its prompt, and the
    companion's own `route`, `context` and `ask` change nothing;
  - on the phone, the companion opens as a sheet over notes, and the
    handle closes it with notes as it was;
  - a process running as a listed agent's user reads notes through
    `/run/lodge/agent.sock` with a token issued `--agent`, and revoking the
    token cuts it off at once;
  - a token issued for the agent socket is refused on the tailnet, a
    tailnet token is refused on the agent socket, and a user not listed
    cannot connect, each with a test that fails it;
  - Porter, built in frostyard/porter from its
    [roadmap](https://github.com/frostyard/porter/blob/main/docs/plans/0001-roadmap.md), can start on these pieces.

## Later / ideas

- One aggregated `/mcp` for clients that want a single connector.
- claude.ai and the Claude apps, which need a public endpoint and OAuth 2.1.
  This is ruled out while lodge stays on the tailnet.
- Badges in the rail, such as unread mail.
- Settings in the shell (sessions, tokens), over a WebSocket of lodge's own.
- The inference layer in a process of its own, behind the same socket.
- **frostread, the third app.** [frostyard/frostread](https://github.com/frostyard/frostread)
  is the reader: RSS, Atom and JSON feeds, and read-later, as lodge app
  `read` at `/app/read/`, user `frostread`, key `r`, with `content`,
  `contentLinks` and `mcp`. It is also an agent on the agent socket, with
  a notes token scoped to `Reading/` for save-as-note
  ([its ADR-0004](https://github.com/frostyard/frostread/blob/main/docs/adr/0004-save-as-note-goes-through-notes-mcp-on-the-agent-socket.md),
  Proposed). Its fetcher is the kit's `netguard`
  ([ADR-0017](../adr/0017-the-kit-guards-outbound-fetches-against-inside-addresses.md)),
  and its web app is the first on `@frostyard/lodge-ui`
  ([ADR-0018](../adr/0018-a-ui-package-beside-the-client.md)). It is built
  on 2026-10-21 and waits for review; the repository is private until the
  owner opens it.
- **The shared UI package.** `@frostyard/lodge-ui` 0.1.0 is built with
  frostmail's token values unchanged
  ([ADR-0018](../adr/0018-a-ui-package-beside-the-client.md),
  [ui-package.md](../specs/ui-package.md)): frostmail and frostnotes move to
  the tokens with no visual change, then the shell and Porter, then each
  React primitive in a minor release, and the look change last, as token
  values only.

## Open questions

- **Web Push.** Mail and the assistant need notifications while lodge is
  closed. That means a service worker scoped to push alone, which reverses
  `worker-src 'none'` ([ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md)).
  The user deferred it on 2026-10-19: no service worker for now. Phones
  get no notifications from lodge until then, and notifications reach only
  a page that is open. Phase 5 left it deferred: Porter is pull, not push,
  in v1. Take it up, with an ADR, when an app needs notifications while
  lodge is closed.
- *Answered 2026-10-21:* **the desktop apps.** frostmail's Tauri app
  is a lodge client over a WebSocket with its node's identity
  ([frostmail ADR-0041](https://github.com/frostyard/frostmail/blob/main/docs/adr/0041-maild-runs-as-a-lodge-app.md));
  frostnotes' stays local-first against its own notesd
  ([frostnotes ADR-0026](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0026-notesd-runs-as-a-lodge-app.md)).
- **Packaging.** Debs through apt-publisher, or a sysext, with the
  `sysusers.d` entries and socket units. Phase 2 deploys with the units
  alone. Decide before Phase 4 brings a second app.
- *Answered 2026-10-21:* **the Copilot adapter.** The owner read
  Copilot's terms and found this use acceptable; Copilot is built
  ([ADR-0016](../adr/0016-copilot-uses-the-owners-copilot-plan-through-githubs-device-sign-in.md)).
  (Codex is resolved by
  [ADR-0015](../adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md).)
- *Answered 2026-10-21:* **where the client is published.** Git tags, with
  the apps pinning `client/vX.Y.Z` and `ui/vX.Y.Z` as pnpm git
  dependencies: no registry, no CI token
  ([ADR-0018](../adr/0018-a-ui-package-beside-the-client.md)).

## References

- Implements: [design/overview.md](../design/overview.md), [specs/app-contract.md](../specs/app-contract.md), [specs/lodge-http.md](../specs/lodge-http.md), [specs/lodge-client.md](../specs/lodge-client.md), [specs/shell.md](../specs/shell.md)
- Decisions: [ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md) to [ADR-0015](../adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md), [ADR-0016](../adr/0016-copilot-uses-the-owners-copilot-plan-through-githubs-device-sign-in.md) and [ADR-0018](../adr/0018-a-ui-package-beside-the-client.md) (Proposed); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
- Related: Porter's [ADR-0003](https://github.com/frostyard/porter/blob/main/docs/adr/0003-porter-is-a-lodge-app-that-owns-no-data.md) to [ADR-0006](https://github.com/frostyard/porter/blob/main/docs/adr/0006-one-continuous-thread.md), [design/overview.md](https://github.com/frostyard/porter/blob/main/docs/design/overview.md), [specs/routines.md](https://github.com/frostyard/porter/blob/main/docs/specs/routines.md) and [roadmap](https://github.com/frostyard/porter/blob/main/docs/plans/0001-roadmap.md); frostnotes' [roadmap](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0002-roadmap.md), [plan 0006](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0006-m4-notes-inside-lodge.md), [ADR-0028](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0028-an-agents-scope-can-limit-writes-to-folders.md) and [plan 0007](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0007-folder-scopes-for-agents.md); frostmail [plan 0012](https://github.com/frostyard/frostmail/blob/main/docs/plans/0012-calendar-tasks-and-contacts-for-agents.md), [ADR-0002](https://github.com/frostyard/frostmail/blob/main/docs/adr/0002-daemon-and-thin-clients.md), [ADR-0013](https://github.com/frostyard/frostmail/blob/main/docs/adr/0013-app-as-a-local-flatpak.md)
