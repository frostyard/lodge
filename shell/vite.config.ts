// The shell is served from /usr/share/lodge/shell/ at /lodge/
// (docs/specs/lodge-http.md, Shell routes). Its policy allows scripts only
// from /lodge/, so nothing is inlined.
import { defineConfig } from "vitest/config";

export default defineConfig({
  base: "/lodge/",
  build: {
    outDir: "dist",
    assetsDir: "assets",
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
    target: "es2022",
  },
  test: {
    environment: "happy-dom",
    // Frames never load in the tests: the shell half's ports get fake
    // windows instead.
    environmentOptions: {
      happyDOM: {
        url: "https://lodge.test/",
        settings: {
          disableIframePageLoading: true,
          disableJavaScriptFileLoading: true,
          disableCSSFileLoading: true,
          handleDisabledFileLoadingAsSuccess: true,
        },
      },
    },
  },
});
