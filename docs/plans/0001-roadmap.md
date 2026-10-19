# Plan: Roadmap

What lodge delivers, in order, from the shared kit to the assistant's
design. lodge is the platform frostyard/core
[ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
decided on. frostnotes' M4 ([plan 0006](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0006-m4-notes-inside-lodge.md))
runs notes inside lodge, and depends on Phases 1 and 2. frostmail's move
is Phase 4.

Status (2026-10-18): the founding documents are Proposed. They are
reviewed and published together with core's ADR-0057 and frostnotes' M4
rescope. Phase 1's new kit pieces and `client/` are built and in review
(2026-10-19), with apps pinned to their commits; the extraction from the
apps and the tags are still to come. No phase starts before the ADRs are accepted, and nothing
changes on the user's machines without the user's go-ahead.

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
    local maild needs it from the kit. Both are decided while extracting.
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
- **Checks on the user's devices and clients:**
  - whether WebKit on iOS sends `Sec-Fetch-Site` on a WebSocket handshake
    (M4 review M3);
  - that Claude Code sends no `Origin` to `/mcp/notes` (L10), and opens a
    new session after a 404 (L11);
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

Implements [app-contract.md's inference](../specs/app-contract.md#inference)
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
- **Links between apps:** notes link to threads at `/mail/<route>`, and
  mail links back to notes.
- **The new-app skill:** `.agents/skills/new-lodge-app/`, a thin scaffold
  on the kit and the client, drawn from what notes and mail needed
  (ADR-0057).
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

Design only. The assistant is built later, from these documents.

- **ADRs and a design doc for the assistant,** user `frostassist`:
  - the companion panel, and how `context` reaches it
    ([lodge-client.md](../specs/lodge-client.md#context),
    [shell.md](../specs/shell.md#the-companion-reserved));
  - its tools through `/mcp/<id>`, with one token per app;
  - its identities: a machine account, scoped tokens and a tagged node,
    never the account that owns the data;
  - what it may do unattended;
  - the inference aliases it uses.
- **The companion, or chat panels per app:** decided here. The strawman
  leaned towards the companion: "chat inside notes" is the assistant opened
  beside notes, with notes' tools and the open note as context.
- **Done when:** the assistant's ADRs and design doc are Proposed, and
  lodge-client's companion messages and the shell's companion layout are
  specified.

## Later / ideas

- One aggregated `/mcp` for clients that want a single connector.
- claude.ai and the Claude apps, which need a public endpoint and OAuth 2.1.
  This is ruled out while lodge stays on the tailnet.
- Badges in the rail, such as unread mail.
- Settings in the shell (sessions, tokens), over a WebSocket of lodge's own.
- The inference layer in a process of its own, behind the same socket.

## Open questions

- **Web Push.** Mail and the assistant need notifications while lodge is
  closed. That means a service worker scoped to push alone, which reverses
  `worker-src 'none'` ([ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md)).
  Decide with an ADR by Phase 4.
- **The desktop apps.** frostmail's Tauri app could become a lodge client
  over a WebSocket with its node's identity, or give way to the home-screen
  app. frostnotes' Tauri app stays local-first against its own notesd.
  Decide by Phase 4.
- **Packaging.** Debs through apt-publisher, or a sysext, with the
  `sysusers.d` entries and socket units. Phase 2 deploys with the units
  alone. Decide before Phase 4 brings a second app.
- **The Copilot and Codex adapters.** Subscription logins used from
  third-party software are a terms-of-service grey area. Verify per
  provider in Phase 3.
- **Where the client is published:** npm, GitHub Packages, or a release
  tarball pinned with its hash. Decide in Phase 1.

## References

- Implements: [design/overview.md](../design/overview.md), [specs/app-contract.md](../specs/app-contract.md), [specs/lodge-http.md](../specs/lodge-http.md), [specs/lodge-client.md](../specs/lodge-client.md), [specs/shell.md](../specs/shell.md)
- Decisions: [ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md) to [ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
- Related: frostnotes' [roadmap](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0002-roadmap.md) and [plan 0006](https://github.com/frostyard/frostnotes/blob/main/docs/plans/0006-m4-notes-inside-lodge.md); frostmail [ADR-0002](https://github.com/frostyard/frostmail/blob/main/docs/adr/0002-daemon-and-thin-clients.md), [ADR-0013](https://github.com/frostyard/frostmail/blob/main/docs/adr/0013-app-as-a-local-flatpak.md)
