# 0012 — The kit shares the RPC runtime, not the generator

- **Status:** Accepted (2026-10-21)
- **Date:** 2026-10-19

## Context

frostnotes and frostmail each carry the same JSON-RPC machinery, copied
from one to the other and then grown apart:

- **The hand-written runtime of each app's `api` package.** This covers
  `Error`, the router, the client and the wire types, and it is nearly
  identical in both apps.
- **`internal/rpcserver`'s connection loop.** It does framing, the gating
  on `rpc.hello`, concurrent dispatch, a panic answering `internal`, and an
  oversized reply answering `internal`. frostnotes has all of these.
  frostmail lacks the last two on its desktop branch.
- **`internal/rpctest`.** It runs a real daemon on a temporary socket.
- **`tools/rpcgen`.** It generates each app's `zz_generated.go` from its
  `schema/rpc`.

The two apps' types differ:

- frostnotes' caller is a hand-written string kind with four values, and
  its methods carry a `writes` mark that an agent's read scope refuses.
- frostmail's caller is a struct whose kind is a generated enum with three
  values, and its schema has no `writes`.
- Both generate an `ErrorCode` with a `String` method, and codes beyond the
  shared 1001–1005.

The roadmap's Phase 1 extracts this machinery into the kit, and left two
questions open: whether the generator converges, and whether frostmail's
Windows private socket moves too.

## Decision

- **The kit holds the runtime, generic over each app's own types.** Package
  `kit/jsonrpc` has:
  - `Error[C]`, `Frame[C]`, `Router[K, C]`, `Server[C]` and `Client[C]`,
    where `C` is the app's `ErrorCode` and `K` its caller kind;
  - `DecodeParams`, the event envelope and `Listen` for the private socket.

  An app's `api` package aliases or embeds these (`type Error =
  jsonrpc.Error[ErrorCode]`, and `type Router struct{ *jsonrpc.Router[…] }`
  with its unexported `handle`). So each app's generated code compiles on
  the kit unchanged, and its `ErrorCode.String` still names its codes.
- **The connection loop is the kit's.** An app's `rpcserver` keeps only what
  is its own:
  - its `rpc.hello`, which readies the connection (`Conn.SetReady`);
  - `events.subscribe` over its broker, started by `AfterReply` and run by
    `Conn.Go`;
  - its per-connection state, carried in the context it gives `ServeConn`.
- **`kit/rpctest` is the test harness.** It runs a server on a temporary
  private socket, dials it, pipes a connection served as another caller,
  and stops it. `rpctest.Options[S]` carries frostmail's `Services func(*S)`
  hook. Building the engine stays in each app's own rpctest.
- **`tools/rpcgen` stays in each app.** The copies do not converge without
  changing what each app's schema means:
  - **Method callers.** A method's callers narrow its domain's in frostnotes,
    and replace them in frostmail.
  - **Domains without callers.** frostnotes requires every domain to list
    its callers, and frostmail defaults to local and session.
  - **Caller kinds.** frostmail declares them as a schema enum and
    frostnotes in Go, with different sets.
  - **Notes' scope rules.** Only frostnotes has `writes` and the `vaultId`
    rule.
  - **Output.** The initialisms and the TypeScript output differ.

  A shared generator would have to choose one meaning for each, which is
  each app's call. With the runtime generic, nothing forces that choice.
- **frostmail's Windows named pipe stays in frostmail.** `jsonrpc.Listen` is
  Linux's. On Windows, frostmail's own listener checks the peer, and the
  kit's `Server` serves it unchanged.

## Consequences

- An app adopts the kit mostly by deleting code:
  - its connection loop, client and router body;
  - its copies of `MaxMessageSize`, the frame and the event envelope.

  Its generated code and its callers' code (`api.X` everywhere) do not
  change.
- A fix to framing, panics or limits lands once, in the kit, and reaches
  both apps when they bump their pin.
- The kit's API has type parameters: an app names
  `jsonrpc.Router[Caller, ErrorCode]` once, in its `api` package.
- The generators may drift further. A generator change that one app wants
  is made in that app's copy.
- The kit's server answers `notReady` with code 1004 itself, so an app's
  notReady stays 1004.

## Alternatives considered

- **A non-generic `jsonrpc.Error` with one `ErrorCode` type.** Each app's
  generated `String` method could no longer be declared on an alias, so
  each generator would change, and app-specific codes would print as
  numbers.
- **Moving `tools/rpcgen` into the kit with options for each difference.**
  The options would encode two schema languages in one tool. The
  differences are semantic choices each app owns, so they don't belong in
  the kit as flags.
- **Leaving the connection loop in each app.** Then the panic and
  reply-size fixes stay per app, which is how frostmail came to lack them.

## References

- Shapes: [design/overview.md](../design/overview.md#the-kit-and-the-client), [roadmap — Phase 1](../plans/0001-roadmap.md#phase-1--the-kit)
- Builds on: [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md) (apps behind lodge's sockets), frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md) (shared code is a library)
