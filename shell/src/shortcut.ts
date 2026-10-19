// The switcher's shortcut (docs/specs/shell.md, The switcher): Ctrl+` by
// default, a shell setting kept in local storage, and never one of the
// keys the apps or the browser already use.

import { parseChord } from "@frostyard/lodge-client/shell";

/** The default: Ctrl with the key left of 1. */
export const DEFAULT_SHORTCUT = "Ctrl+Backquote";

/** A chord ruled out as the shortcut, and why. */
export interface RuledOut {
  chord: string;
  why: string;
}

const range = (from: number, to: number, f: (n: number) => RuledOut): RuledOut[] =>
  Array.from({ length: to - from + 1 }, (_, i) => f(from + i));

/**
 * The chords ruled out (shell.md): notes' completion and property keys,
 * Obsidian's default hotkeys as notes binds them (frostnotes' ui.md,
 * Keyboard map), the browser's tab keys, and Alt+1 to 9, Firefox's tab
 * keys on Linux.
 */
export const RULED_OUT: readonly RuledOut[] = [
  { chord: "Ctrl+Space", why: "notes' completion (CodeMirror)" },
  { chord: "Ctrl+Semicolon", why: "adds a property in notes" },
  ...["KeyO", "KeyP", "KeyN", "KeyE", "KeyF", "KeyH", "KeyS", "KeyB", "KeyI", "KeyK", "KeyL", "KeyD", "KeyG"].map(
    (k) => ({
      chord: `Ctrl+${k}`,
      why: "an Obsidian hotkey notes binds",
    }),
  ),
  ...["Comma", "Slash", "Enter", "Equal", "Minus", "Digit0"].map((k) => ({
    chord: `Ctrl+${k}`,
    why: "an Obsidian hotkey notes binds",
  })),
  { chord: "Ctrl+Shift+KeyN", why: "an Obsidian hotkey notes binds" },
  { chord: "Ctrl+Shift+KeyF", why: "an Obsidian hotkey notes binds" },
  { chord: "Ctrl+Alt+ArrowLeft", why: "an Obsidian hotkey notes binds" },
  { chord: "Ctrl+Alt+ArrowRight", why: "an Obsidian hotkey notes binds" },
  { chord: "Ctrl+Alt+Enter", why: "an Obsidian hotkey notes binds" },
  { chord: "Alt+Enter", why: "an Obsidian hotkey notes binds" },
  { chord: "Ctrl+KeyT", why: "the browser's new tab" },
  { chord: "Ctrl+KeyW", why: "the browser's close tab" },
  { chord: "Ctrl+Shift+KeyT", why: "the browser's reopen tab" },
  { chord: "Ctrl+Shift+KeyW", why: "the browser's close window" },
  { chord: "Ctrl+Tab", why: "the browser's next tab" },
  { chord: "Ctrl+Shift+Tab", why: "the browser's previous tab" },
  { chord: "Ctrl+PageDown", why: "the browser's next tab" },
  { chord: "Ctrl+PageUp", why: "the browser's previous tab" },
  ...range(1, 9, (n) => ({ chord: `Ctrl+Digit${n}`, why: "the browser's tab keys" })),
  ...range(1, 9, (n) => ({ chord: `Alt+Digit${n}`, why: "Firefox's tab keys on Linux" })),
];

/** normalize writes a chord with its modifiers in one order, for comparing. */
export function normalize(s: string): string | null {
  const c = parseChord(s);
  if (!c) return null;
  return [c.ctrl && "Ctrl", c.alt && "Alt", c.shift && "Shift", c.meta && "Meta", c.code].filter(Boolean).join("+");
}

/**
 * checkShortcut returns why chord cannot be the shortcut, or null when it
 * can: it must parse, hold Ctrl, Alt or Meta, and not be ruled out.
 */
export function checkShortcut(chord: string): string | null {
  const c = parseChord(chord);
  if (!c) return "not a chord: modifiers joined by +, then a key code, such as Ctrl+Backquote";
  if (!c.ctrl && !c.alt && !c.meta) return "a shortcut needs Ctrl, Alt or Meta";
  const n = normalize(chord);
  const hit = RULED_OUT.find((r) => normalize(r.chord) === n);
  return hit ? `${hit.chord} is ${hit.why}` : null;
}

/** label writes a chord for people: Ctrl+` for Ctrl+Backquote. */
export function label(chord: string): string {
  const names: Record<string, string> = { Backquote: "`", Space: "Space", Semicolon: ";", Comma: ",", Slash: "/" };
  return chord
    .split("+")
    .map((p) => names[p] ?? p.replace(/^Key/, "").replace(/^Digit/, ""))
    .join("+");
}
