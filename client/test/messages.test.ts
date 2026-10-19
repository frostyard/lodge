import { describe, expect, it } from "vitest";
import { matchesChord, parseChord } from "../src/chord.js";
import { parseAppMessage, parseShellMessage, validApp } from "../src/messages.js";
import { key } from "./harness.js";

describe("chords", () => {
  it("parses the default and combinations", () => {
    expect(parseChord("Ctrl+Backquote")).toEqual({
      ctrl: true,
      alt: false,
      shift: false,
      meta: false,
      code: "Backquote",
    });
    expect(parseChord("Meta+Shift+KeyK")).toEqual({ ctrl: false, alt: false, shift: true, meta: true, code: "KeyK" });
    expect(parseChord("F1")).toEqual({ ctrl: false, alt: false, shift: false, meta: false, code: "F1" });
  });
  it.each(["", "Ctrl+", "Ctrl+Ctrl+KeyK", "Hyper+KeyK", "Ctrl", "ctrl+KeyK", "Ctrl+Key K"])("refuses %j", (s) => {
    expect(parseChord(s)).toBeNull();
  });
  it("matches exactly", () => {
    const c = parseChord("Ctrl+Backquote");
    if (c === null) throw new Error("no chord");
    expect(matchesChord(key("Backquote", { ctrlKey: true }), c)).toBe(true);
    expect(matchesChord(key("Backquote", { ctrlKey: true, altKey: true }), c)).toBe(false);
    expect(matchesChord(key("Backquote", { ctrlKey: true, isComposing: true }), c)).toBe(false);
  });
});

describe("parsing", () => {
  it("drops other majors, unknown types and malformed fields", () => {
    for (const m of [
      null,
      "ready",
      [],
      { type: "ready", contract: 1 },
      { lodge: 2, type: "route", path: "/", title: "" },
      { lodge: 1, type: "nope" },
      { lodge: 1, type: "route", path: "/", title: 3 },
      { lodge: 1, type: "route", path: "/", title: "", replace: "yes" },
      { lodge: 1, type: "context", kind: "x".repeat(33), ref: "", title: "" },
      { lodge: 1, type: "context", kind: "note", ref: "r".repeat(1025), title: "" },
      { lodge: 1, type: "open", url: 1 },
    ]) {
      expect(parseAppMessage(m)).toBeNull();
    }
    for (const m of [
      { lodge: 1, type: "init", contract: 1, contentOrigin: 3, shortcut: "Ctrl+Backquote", theme: "dark" },
      { lodge: 1, type: "navigate", route: "x" },
      { lodge: 1, type: "visible", visible: "true" },
      { lodge: 1, type: "theme", scheme: "sepia" },
      { lodge: 1, type: "context", app: "Lodge", kind: null, ref: "", title: "" },
    ]) {
      expect(parseShellMessage(m)).toBeNull();
    }
  });
  it("copies only the known fields", () => {
    expect(parseAppMessage({ lodge: 1, type: "open", url: "/a", extra: "<script>" })).toEqual({
      lodge: 1,
      type: "open",
      url: "/a",
    });
    expect(parseAppMessage({ lodge: 1, type: "context", kind: null, ref: "", title: "" })).toEqual({
      lodge: 1,
      type: "context",
      kind: null,
      ref: "",
      title: "",
    });
    expect(parseShellMessage({ lodge: 1, type: "context", app: "notes", kind: "note", ref: "r", title: "t" })).toEqual({
      lodge: 1,
      type: "context",
      app: "notes",
      kind: "note",
      ref: "r",
      title: "t",
    });
  });
  it("cuts titles by characters, not halves of one", () => {
    const m = parseAppMessage({ lodge: 1, type: "route", path: "/", title: "😀".repeat(201) });
    expect(m?.type === "route" && Array.from(m.title).length).toBe(200);
  });
  it("knows app ids", () => {
    expect(["notes", "mail", "a1"].map(validApp)).toEqual([true, true, true]);
    expect(["app", "device", "lodge", "mcp", "", "N", "a-b"].some(validApp)).toBe(false);
  });
});
