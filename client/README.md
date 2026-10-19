# @frostyard/lodge-client

The frame protocol between lodge's shell and its apps
([lodge-client.md](../docs/specs/lodge-client.md)): the app half, which
every app bundle includes, and the shell half, `@frostyard/lodge-client/shell`.
It is ESM TypeScript with no runtime dependencies, and ships as source: a
bundler such as Vite compiles it with the app.

Apps pin it to a pushed lodge commit, as a pnpm git dependency in this
subdirectory:

```json
"@frostyard/lodge-client": "github:frostyard/lodge#<commit>&path:client"
```

```ts
import { connectLodge } from "@frostyard/lodge-client";

const lodge = await connectLodge({ app: "notes" });
lodge.route("/1/Projects/Plan.md", "Plan");
lodge.on("navigate", ({ route }) => router.go(route));
```

Build and test only in the nsl machine (`make client-check`;
[dev/nsl/provision.sh](../dev/nsl/provision.sh)), never on the host.
