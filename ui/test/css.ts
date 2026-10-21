// A reader for the package's own CSS: just enough to check the contract the
// stylesheets make, with no CSS parser.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// The paths come from this file's own name, not from new URL(…, import.meta.url):
// under happy-dom the page's URL class resolves a relative path against the
// page's origin, not the file's.
const HERE = dirname(fileURLToPath(import.meta.url));
const SRC = join(HERE, "..", "src");
const ROOT = join(HERE, "..");

/** read returns a file of src/, as text. */
export function read(name: string): string {
  return readFileSync(join(SRC, name), "utf8");
}

/** readRoot returns a file of the package, as text. */
export function readRoot(name: string): string {
  return readFileSync(join(ROOT, name), "utf8");
}

/** sources lists every file under src/, as paths relative to it. */
export function sources(): string[] {
  return readdirSync(SRC, { recursive: true, withFileTypes: true })
    .filter((entry) => entry.isFile())
    .map((entry) => join(entry.parentPath, entry.name).slice(SRC.length + 1))
    .sort();
}

/**
 * blocks returns the bodies of the declaration blocks whose prelude matches
 * opener's match, which must take in the opening brace. A declaration block
 * holds no brace of its own, so the body ends at the next "}".
 */
export function blocks(css: string, opener: RegExp): string[] {
  const flags = opener.flags.includes("g") ? opener.flags : `${opener.flags}g`;
  const out: string[] = [];
  for (const m of css.matchAll(new RegExp(opener.source, flags))) {
    const start = (m.index ?? 0) + m[0].length;
    const end = css.indexOf("}", start);
    if (end < 0) throw new Error(`no end for ${m[0].trim()}`);
    out.push(css.slice(start, end));
  }
  return out;
}

/** parse returns the custom properties a declaration block sets. */
export function parse(body: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const declaration of body.replace(/\/\*[\s\S]*?\*\//g, "").split(";")) {
    const colon = declaration.indexOf(":");
    if (colon < 0) continue;
    const name = declaration.slice(0, colon).trim();
    const value = declaration.slice(colon + 1).trim();
    if (name.startsWith("--") && value !== "") out.set(name, value);
  }
  return out;
}

/** merge parses the blocks in order, so a later block wins. */
export function merge(bodies: string[]): Map<string, string> {
  return union(...bodies.map((body) => parse(body)));
}

/** declarations returns every custom property a stylesheet sets, in any block. */
export function declarations(css: string): Map<string, string> {
  const out = new Map<string, string>();
  for (const m of css.replace(/\/\*[\s\S]*?\*\//g, "").matchAll(/\{([^{}]*)\}/g)) {
    for (const [name, value] of parse(m[1] ?? "")) out.set(name, value);
  }
  return out;
}

/** union joins maps, so a later one wins. */
export function union(...maps: Map<string, string>[]): Map<string, string> {
  const out = new Map<string, string>();
  for (const map of maps) for (const [name, value] of map) out.set(name, value);
  return out;
}
