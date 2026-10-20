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

Build and test only in the nsl machine (`make client-check`;
[dev/nsl/provision.sh](../dev/nsl/provision.sh)), never on the host.
