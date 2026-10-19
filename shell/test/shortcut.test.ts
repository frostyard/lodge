import { parseChord } from "@frostyard/lodge-client/shell";
import { describe, expect, it } from "vitest";
import { checkShortcut, DEFAULT_SHORTCUT, label, normalize, RULED_OUT } from "../src/shortcut.js";

describe("the switcher's shortcut", () => {
  it("defaults to Ctrl+` and collides with none of the ruled-out keys", () => {
    expect(DEFAULT_SHORTCUT).toBe("Ctrl+Backquote");
    expect(checkShortcut(DEFAULT_SHORTCUT)).toBeNull();
    for (const r of RULED_OUT) {
      expect(parseChord(r.chord), r.chord).not.toBeNull();
      expect(normalize(r.chord)).not.toBe(normalize(DEFAULT_SHORTCUT));
    }
  });

  it("lists the keys shell.md rules out", () => {
    const listed = new Set(RULED_OUT.map((r) => normalize(r.chord)));
    const must = [
      "Ctrl+Space", // notes' completion
      "Ctrl+Semicolon", // a property in notes
      "Ctrl+KeyO", // Obsidian's quick switcher
      "Ctrl+KeyP", // Obsidian's command palette
      "Ctrl+Tab", // the browser's tabs
      "Ctrl+KeyT",
      "Ctrl+KeyW",
      ...Array.from({ length: 9 }, (_, i) => `Alt+Digit${i + 1}`), // Firefox on Linux
    ];
    for (const c of must) expect(listed.has(normalize(c)), c).toBe(true);
  });

  it("refuses what cannot be the shortcut", () => {
    expect(checkShortcut("Ctrl+Space")).toMatch(/completion/);
    expect(checkShortcut("Alt+Digit3")).toMatch(/Firefox/);
    expect(checkShortcut("Shift+Ctrl+KeyT")).toMatch(/reopen tab/);
    expect(checkShortcut("KeyN")).toMatch(/needs Ctrl, Alt or Meta/);
    expect(checkShortcut("Ctrl+")).toMatch(/not a chord/);
    expect(checkShortcut("Ctrl+Alt+KeyJ")).toBeNull();
  });

  it("writes chords for people", () => {
    expect(label("Ctrl+Backquote")).toBe("Ctrl+`");
    expect(label("Ctrl+Alt+KeyJ")).toBe("Ctrl+Alt+J");
  });
});
