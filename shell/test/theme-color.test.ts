// The page's theme-color metas and the manifest name the same chrome as the
// UI package's tokens (docs/specs/shell.md, Colours and type;
// docs/adr/0008-lodge-installs-as-one-home-screen-app.md). A mobile browser
// takes the metas over the manifest, so all 3 must agree with the token.
import tokens from "@frostyard/lodge-ui/tokens.css?raw";
import { describe, expect, it } from "vitest";
import html from "../index.html?raw";
import manifest from "../public/manifest.webmanifest?raw";

const LIGHT = ":root {";
const DARK = "@media (prefers-color-scheme: dark)";
const LIGHT_META = 'meta[name="theme-color"][media="(prefers-color-scheme: light)"]';
const DARK_META = 'meta[name="theme-color"][media="(prefers-color-scheme: dark)"]';

// token is the first #rrggbb value of the named token after start, the block
// that holds the light or the dark values.
function token(start: string, name: string): string {
  const at = tokens.indexOf(start);
  if (at < 0) throw new Error(`tokens.css has no ${start}`);
  const found = new RegExp(`${name}: (#[0-9a-f]{6})`).exec(tokens.slice(at));
  const value = found?.[1];
  if (value === undefined) throw new Error(`tokens.css has no ${name} after ${start}`);
  return value;
}

const doc = new DOMParser().parseFromString(html, "text/html");
const web = JSON.parse(manifest) as { background_color?: string; theme_color?: string };

describe("the shell page's theme colour", () => {
  it("is the chrome in the light, as the token and the manifest say", () => {
    const chrome = token(LIGHT, "--bg-sidebar");
    const meta = doc.querySelector(LIGHT_META);
    expect(meta).not.toBeNull();
    expect(meta?.getAttribute("content")).toBe(chrome);
    expect(web.theme_color).toBe(chrome);
  });

  it("is the chrome in the dark", () => {
    const meta = doc.querySelector(DARK_META);
    expect(meta).not.toBeNull();
    expect(meta?.getAttribute("content")).toBe(token(DARK, "--bg-sidebar"));
  });

  it("leaves the manifest's background the window's", () => {
    expect(web.background_color).toBe(token(LIGHT, "--bg-window"));
  });
});
