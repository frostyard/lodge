# @frostyard/lodge-client

The frame protocol between lodge's shell and its apps
([lodge-client.md](../docs/specs/lodge-client.md)): the app half, which
every app bundle includes, and the shell half, `@frostyard/lodge-client/shell`.
It is ESM TypeScript with no runtime dependencies, and ships as source: a
bundler such as Vite compiles it with the app.

Apps pin it to a release, a lodge tag `client/vX.Y.Z`, as a pnpm git
dependency in this subdirectory:

```json
"@frostyard/lodge-client": "github:frostyard/lodge#client/v0.2.0&path:client"
```

Between releases an app may pin a pushed lodge commit in its place
(`#<commit>&path:client`). The Go kit has its own tags, `kit/vX.Y.Z`.
What each release changed is in the [changelog](CHANGELOG.md); older
versions are in the git tags `client/vX.Y.Z`.

```ts
import { connectLodge } from "@frostyard/lodge-client";

const lodge = await connectLodge({ app: "notes" });
lodge.route("/1/Projects/Plan.md", "Plan");
lodge.on("navigate", ({ route }) => router.go(route));
lodge.context({ kind: "note", ref: "notes/1/Projects/Plan.md", title: "Plan" });
if (lodge.companion !== null) lodge.ask("What is due here?"); // "Ask about this"
```

In the companion's own frame, `lodge.role` is `"companion"`, and
`lodge.on("context")` and `lodge.on("ask")` hear the current app's.

## The rpc subpath

An app's API is JSON-RPC 2.0 over the WebSocket lodge proxies at
`lodge.apiURL()`. `@frostyard/lodge-client/rpc` is its client, the
TypeScript counterpart of the kit's `kit/jsonrpc`
([ADR-0012](../docs/adr/0012-the-kit-shares-the-rpc-runtime-not-the-generator.md)).
It adds no dependency.

```ts
import { connectLodgeAPI } from "@frostyard/lodge-client/rpc";

const api = await connectLodgeAPI(lodge, { hello: { protocol: 1, client: "notes-web" } });
const counts = await api.call<Counts>("counts", {});
api.onEvent(({ event, data }) => {
  if (event === "items.changed") refetch(data);
});
```

- It opens only while the page is visible, pings `rpc.ping` every 30 s so
  lodge keeps the session (which ends after 90 s without a frame), and
  reconnects with a growing delay after a drop.
- `pingTimeoutMs` closes a socket whose ping goes unanswered for that long,
  as a drop, so the reconnect runs. It is off unless an app asks: pass
  `10_000` for a phone that may sleep without closing its sockets.
- `hello` is sent after every open, when given, and the transport is ready
  only after its answer.
- It resolves on the first ready connection and rejects when that first
  attempt fails; a call made while the socket is down rejects with
  `CodeNotConnected`. A failed `rpc.hello` rejects with that call's own
  `RPCError`, so its `code` reaches you, such as a protocol mismatch's
  1003 ([changelog](CHANGELOG.md)).
- `JsonRpcSession` is the session alone, for a channel of your own, and
  `RPCError` carries the daemon's `code`, `message` and `data`.

Build and test only in the nsl machine (`make client-check`;
[dev/nsl/provision.sh](../dev/nsl/provision.sh)), never on the host.
