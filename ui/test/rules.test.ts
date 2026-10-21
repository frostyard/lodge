// The package's rules (docs/specs/ui-package.md, Rules), checked on the
// sources themselves.
import { describe, expect, it } from "vitest";
import { declarations, read, readRoot, sources, union } from "./css.js";

const files = sources();
const css = files.filter((name) => name.endsWith(".css"));
const code = files.filter((name) => name.endsWith(".ts") || name.endsWith(".tsx"));

/** specifiers returns every module a source imports or re-exports. */
function specifiers(source: string): string[] {
  return [...source.matchAll(/(?:from|import)\s+["']([^"']+)["']/g)].map((m) => m[1] ?? "");
}

describe("the sources", () => {
  it("are all found", () => {
    expect(css.sort()).toEqual(["accents.css", "base.css", "fonts.css", "theme.css", "tokens.css"]);
    expect(code).toContain("theme.ts");
    expect(code).toContain("react/Sheet.tsx");
  });

  it("never import Tailwind, as a whole or in part", () => {
    for (const name of css) {
      for (const m of read(name).matchAll(/@import\s+["']([^"']+)["']/g)) {
        expect(m[1]?.startsWith("tailwindcss"), `${name} imports ${m[1]}`).toBe(false);
      }
    }
    for (const name of code) {
      for (const specifier of specifiers(read(name))) {
        expect(specifier.startsWith("tailwindcss"), `${name} imports ${specifier}`).toBe(false);
      }
    }
  });

  it("keep React out of the stylesheets and out of theme.ts", () => {
    for (const name of css) {
      expect(read(name).includes('"react"'), `${name} imports React`).toBe(false);
    }
    expect(specifiers(read("theme.ts")).includes("react"), "theme.ts imports React").toBe(false);
  });

  it("makes theme.css map only names tokens.css or accents.css define", () => {
    const defined = union(declarations(read("tokens.css")), declarations(read("accents.css")));
    const used = new Set([...read("theme.css").matchAll(/var\(\s*(--[a-z0-9-]+)/g)].map((m) => m[1] ?? ""));
    expect(used.size).toBeGreaterThan(10);
    for (const name of used) {
      expect(defined.has(name), `theme.css uses ${name}, which no token file defines`).toBe(true);
    }
  });

  it("is ESM with no runtime dependency but the font", () => {
    const pkg = JSON.parse(readRoot("package.json")) as {
      type?: string;
      sideEffects?: string[];
      dependencies?: Record<string, string>;
      peerDependencies?: Record<string, string>;
    };
    expect(pkg.type).toBe("module");
    expect(pkg.sideEffects).toEqual(["*.css"]);
    expect(Object.keys(pkg.dependencies ?? {})).toEqual(["@fontsource-variable/inter"]);
    expect(pkg.peerDependencies).toEqual({ "lucide-react": "^1", react: "^19" });
  });
});
