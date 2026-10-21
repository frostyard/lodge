// theme.ts (docs/specs/ui-package.md, theme): the scheme from lodge, and the
// narrow query, with no React and a window a test can fake.
import { describe, expect, it } from "vitest";
import {
  applyScheme,
  followLodge,
  isNarrow,
  NARROW_QUERY,
  onNarrowChange,
  type Scheme,
  type ThemeSource,
} from "../src/theme.js";

describe("applyScheme", () => {
  it("sets data-theme, and removes it for the system's scheme", () => {
    const root = document.createElement("div");
    applyScheme(root, "dark");
    expect(root.dataset.theme).toBe("dark");
    applyScheme(root, "light");
    expect(root.dataset.theme).toBe("light");
    applyScheme(root, null);
    expect(root.dataset.theme).toBeUndefined();
  });
});

describe("followLodge", () => {
  it("applies the shell's scheme at once, then follows its theme event", () => {
    const root = document.createElement("div");
    const listeners = new Set<(event: { scheme: Scheme }) => void>();
    const lodge: ThemeSource = {
      theme: "dark",
      on: (type, fn) => {
        expect(type).toBe("theme");
        listeners.add(fn);
        return () => listeners.delete(fn);
      },
    };
    const stop = followLodge(lodge, root);
    expect(root.dataset.theme).toBe("dark");
    for (const fn of listeners) fn({ scheme: "light" });
    expect(root.dataset.theme).toBe("light");
    stop();
    expect(listeners.size).toBe(0);
  });
});

/** fakeWindow is a window whose matchMedia answers NARROW_QUERY and reports changes. */
function fakeWindow(narrow: boolean) {
  const listeners = new Set<(event: MediaQueryListEvent) => void>();
  let matches = narrow;
  const query = {
    get matches() {
      return matches;
    },
    addEventListener: (_type: string, fn: (event: MediaQueryListEvent) => void) => {
      listeners.add(fn);
    },
    removeEventListener: (_type: string, fn: (event: MediaQueryListEvent) => void) => {
      listeners.delete(fn);
    },
    media: NARROW_QUERY,
  };
  return {
    win: {
      matchMedia: (media: string) => {
        expect(media).toBe(NARROW_QUERY);
        return query as unknown as MediaQueryList;
      },
    },
    set(next: boolean) {
      matches = next;
      for (const fn of listeners) fn({ matches: next } as MediaQueryListEvent);
    },
    listeners,
  };
}

describe("the narrow query", () => {
  it("is the one theme.css's variant uses", () => {
    expect(NARROW_QUERY).toBe("(max-width: 599px)");
  });

  it("is read from matchMedia, and followed until the unsubscribe", () => {
    const fake = fakeWindow(false);
    expect(isNarrow(fake.win)).toBe(false);
    const heard: boolean[] = [];
    const stop = onNarrowChange(fake.win, (narrow) => heard.push(narrow));
    fake.set(true);
    expect(isNarrow(fake.win)).toBe(true);
    stop();
    fake.set(false);
    expect(heard).toEqual([true]);
    expect(fake.listeners.size).toBe(0);
  });
});
