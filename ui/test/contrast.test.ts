// The text tokens' contrast (docs/specs/ui-package.md, Tokens; ADR-0021):
// each one reaches 4.5:1 on each of the four surfaces, in the light and in
// both dark blocks, and the three keep their order on every surface.
import { describe, expect, it } from "vitest";
import { blocks, merge, read } from "./css.js";

const tokensCSS = read("tokens.css");

// The blocks are found by their preludes, as test/tokens.test.ts finds them.
const lightOpener = /^:root \{/m;
const darkMediaOpener = /^ {2}:root:not\(\[data-theme="light"\]\) \{/m;
const darkThemeOpener = /^:root\[data-theme="dark"\] \{/m;

const schemes: { where: string; tokens: Map<string, string> }[] = [
  { where: "the light block", tokens: merge(blocks(tokensCSS, lightOpener)) },
  { where: "the media dark block", tokens: merge(blocks(tokensCSS, darkMediaOpener)) },
  { where: "the data-theme dark block", tokens: merge(blocks(tokensCSS, darkThemeOpener)) },
];

const texts = ["--text-primary", "--text-secondary", "--text-tertiary"];
const surfaces = ["--bg-window", "--bg-sidebar", "--bg-toolbar", "--bg-banner"];

// The steps the three text tokens keep, the brighter one first.
const steps: [string, string][] = [
  ["--text-primary", "--text-secondary"],
  ["--text-secondary", "--text-tertiary"],
];

// WCAG 2.x (AA) asks 4.5:1 of body text.
const MIN = 4.5;

/** hex parses a "#rrggbb" colour, and throws on any other form of value. */
function hex(value: string): [number, number, number] {
  const m = /^#([0-9a-f]{2})([0-9a-f]{2})([0-9a-f]{2})$/i.exec(value);
  const r = m?.[1];
  const g = m?.[2];
  const b = m?.[3];
  if (r === undefined || g === undefined || b === undefined) {
    throw new Error(`not a #rrggbb colour: ${value}`);
  }
  return [Number.parseInt(r, 16), Number.parseInt(g, 16), Number.parseInt(b, 16)];
}

/** luminance returns the WCAG 2.x relative luminance of a colour. */
function luminance(value: string): number {
  const [r, g, b] = hex(value);
  const channel = (c: number): number => {
    const v = c / 255;
    return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

/** ratio returns the WCAG 2.x contrast ratio of two colours. */
function ratio(a: string, b: string): number {
  const one = luminance(a);
  const two = luminance(b);
  return (Math.max(one, two) + 0.05) / (Math.min(one, two) + 0.05);
}

/** value returns the value a block sets for a token. */
function value(tokens: Map<string, string>, name: string, where: string): string {
  const got = tokens.get(name);
  if (got === undefined) throw new Error(`${name} is not set in ${where}`);
  return got;
}

/** contrast returns the ratio of a text token on a surface, in one block. */
function contrast(tokens: Map<string, string>, text: string, surface: string, where: string): number {
  return ratio(value(tokens, text, where), value(tokens, surface, where));
}

/** found names the block, the token, the surface and the ratio it reaches. */
function found(where: string, text: string, surface: string, got: number): string {
  return `${where}: ${text} on ${surface} is ${got.toFixed(2)}:1`;
}

describe("the text tokens", () => {
  it("read only #rrggbb values, so that a new form fails here", () => {
    expect(() => hex("rgb(29 29 31)")).toThrow("not a #rrggbb colour");
    expect(() => hex("#1d1d1")).toThrow("not a #rrggbb colour");
    expect(hex("#1d1d1f")).toEqual([0x1d, 0x1d, 0x1f]);
  });

  for (const { where, tokens } of schemes) {
    for (const text of texts) {
      it(`puts ${text} at ${MIN}:1 or more on every surface in ${where}`, () => {
        for (const surface of surfaces) {
          const got = contrast(tokens, text, surface, where);
          expect(got, found(where, text, surface, got)).toBeGreaterThanOrEqual(MIN);
        }
      });
    }

    for (const [above, below] of steps) {
      it(`keeps ${above} above ${below} in ${where}`, () => {
        for (const surface of surfaces) {
          const high = contrast(tokens, above, surface, where);
          const low = contrast(tokens, below, surface, where);
          expect(high, `${found(where, above, surface, high)}, over ${below}`).toBeGreaterThan(low);
        }
      });
    }
  }
});
