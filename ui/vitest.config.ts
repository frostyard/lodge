// The tests read a few files and render a few components, so they need a DOM
// and nothing else: no build, no network, no server.
import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    environment: "happy-dom",
    include: ["test/**/*.test.ts", "test/**/*.test.tsx"],
  },
});
