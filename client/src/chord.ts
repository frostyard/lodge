// The switcher's shortcut as a chord: zero or more modifiers from Ctrl, Alt,
// Shift and Meta, joined by +, then a KeyboardEvent.code value
// (docs/specs/lodge-client.md, The shortcut).

export interface Chord {
  ctrl: boolean;
  alt: boolean;
  shift: boolean;
  meta: boolean;
  /** A KeyboardEvent.code value, such as "Backquote" or "KeyN". */
  code: string;
}

/** The parts of a KeyboardEvent a chord is matched on. */
export interface KeyLike {
  code: string;
  ctrlKey: boolean;
  altKey: boolean;
  shiftKey: boolean;
  metaKey: boolean;
  isComposing: boolean;
}

const MODIFIERS = { Ctrl: "ctrl", Alt: "alt", Shift: "shift", Meta: "meta" } as const;
const CODE = /^[A-Za-z][A-Za-z0-9]*$/;

/** parseChord parses "Ctrl+Backquote" and the like, or returns null. Each modifier appears at most once. */
export function parseChord(s: string): Chord | null {
  const parts = s.split("+");
  const code = parts.pop();
  if (code === undefined || !CODE.test(code) || code in MODIFIERS) return null;
  const c: Chord = { ctrl: false, alt: false, shift: false, meta: false, code };
  for (const p of parts) {
    if (!Object.hasOwn(MODIFIERS, p)) return null;
    const k = MODIFIERS[p as keyof typeof MODIFIERS];
    if (c[k]) return null;
    c[k] = true;
  }
  return c;
}

/** matchesChord reports whether a key event is exactly the chord, outside a composition. */
export function matchesChord(e: KeyLike, c: Chord): boolean {
  return (
    !e.isComposing &&
    e.code === c.code &&
    e.ctrlKey === c.ctrl &&
    e.altKey === c.alt &&
    e.shiftKey === c.shift &&
    e.metaKey === c.meta
  );
}
