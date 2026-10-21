// The token contract (docs/specs/ui-package.md, Tokens): one light block per
// scheme, both dark blocks in step, and the values unchanged from the apps
// the package took them from.
import { describe, expect, it } from "vitest";
import { blocks, merge, read, union } from "./css.js";
import { frostmail, frostnotes } from "./fixtures/tokens.js";

const tokensCSS = read("tokens.css");
const accentsCSS = read("accents.css");

// The blocks are found by their preludes as the files write them: the light
// block at the start of a line, the media block indented inside its @media,
// and the attribute blocks at the start of a line.
const tokenLight = merge(blocks(tokensCSS, /^:root \{/m));
const accentLight = merge(blocks(accentsCSS, /^:root \{/m));
const light = union(tokenLight, accentLight);
const darkMedia = merge([
  ...blocks(tokensCSS, /^ {2}:root:not\(\[data-theme="light"\]\) \{/m),
  ...blocks(accentsCSS, /^ {2}:root:not\(\[data-theme="light"\]\) \{/m),
]);
const dark = merge([
  ...blocks(tokensCSS, /^:root\[data-theme="dark"\] \{/m),
  ...blocks(accentsCSS, /^:root\[data-theme="dark"\] \{/m),
]);
const coarse = merge(blocks(tokensCSS, /^ {2}:root \{/m));

// The density tokens are sizes, not colours: the dark blocks do not set
// them, and must not.
const density = ["--control-h", "--row-h", "--pad-x"];

describe("tokens.css and accents.css", () => {
  it("sets every colour of the light block in both dark blocks, with one value", () => {
    const colours = [...light.keys()].filter((name) => !density.includes(name));
    expect(colours.length).toBeGreaterThan(0);
    for (const name of colours) {
      expect(darkMedia.get(name), `${name} in the media dark block`).toBeDefined();
      expect(dark.get(name), `${name} in the data-theme dark block`).toBeDefined();
      expect(darkMedia.get(name), `${name} agrees between the dark blocks`).toBe(dark.get(name));
    }
  });

  it("carries frostmail's colours unchanged", () => {
    for (const [name, value] of Object.entries(frostmail)) {
      expect(light.get(name), `${name} is in the light block`).toBe(value.light);
      // frostmail does not override every token in the dark; a token it
      // leaves alone keeps its light value, written out here so that both
      // dark blocks are complete.
      const expected = value.dark ?? value.light;
      expect(darkMedia.get(name), `${name} in the media dark block`).toBe(expected);
      expect(dark.get(name), `${name} in the data-theme dark block`).toBe(expected);
    }
  });

  it("carries frostnotes' generic extras unchanged", () => {
    for (const [name, value] of Object.entries(frostnotes)) {
      expect(light.get(name), `${name} is in the light block`).toBe(value.light);
      expect(darkMedia.get(name), `${name} in the media dark block`).toBe(value.dark);
      expect(dark.get(name), `${name} in the data-theme dark block`).toBe(value.dark);
    }
  });

  it("keeps the domain tokens apart, in accents.css only", () => {
    for (const name of ["--flag-1", "--quote-1", "--avatar-0"]) {
      expect(tokenLight.has(name), `${name} is not in tokens.css`).toBe(false);
      expect(accentLight.has(name), `${name} is in accents.css`).toBe(true);
    }
  });

  it("sets the density tokens, and raises them for a coarse pointer", () => {
    for (const name of density) {
      expect(light.get(name), `${name} is in the light block`).toBeDefined();
      expect(coarse.get(name), `${name} is in the coarse block`).toBeDefined();
      expect(coarse.get(name), `${name} is larger for a finger`).not.toBe(light.get(name));
    }
    // Nothing else lives in the coarse block.
    expect([...coarse.keys()].sort()).toEqual([...density].sort());
  });
});
