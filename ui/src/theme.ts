// The scheme and the narrow query, for every consumer of the package: the
// apps' React code, the shell and Porter. No React here, and no DOM
// assumption beyond the root element and window.matchMedia
// (docs/specs/ui-package.md, theme).

/** Scheme is a scheme the tokens answer to: the value of data-theme. */
export type Scheme = "light" | "dark";

/**
 * applyScheme sets the page's scheme, which tokens.css reads from
 * data-theme. A null scheme removes the attribute, so the system's
 * prefers-color-scheme applies.
 */
export function applyScheme(root: HTMLElement, scheme: Scheme | null): void {
  if (scheme === null) delete root.dataset.theme;
  else root.dataset.theme = scheme;
}

/**
 * ThemeSource is the part of lodge-client's Lodge that this package needs:
 * its `theme`, and its `theme` event (lodge-client's `on("theme")`). A Lodge
 * is one.
 */
export interface ThemeSource {
  readonly theme: Scheme;
  on(type: "theme", fn: (event: { scheme: Scheme }) => void): () => void;
}

/**
 * followLodge applies the shell's scheme, and follows it while it changes.
 * It returns the unsubscribe. Call it once, where the app connects to lodge;
 * it works without a shell too, when an app passes its own ThemeSource.
 */
export function followLodge(lodge: ThemeSource, root: HTMLElement = globalThis.document.documentElement): () => void {
  applyScheme(root, lodge.theme);
  return lodge.on("theme", ({ scheme }) => applyScheme(root, scheme));
}

/** NARROW_QUERY is the media query of a narrow window, and of theme.css's narrow variant. */
export const NARROW_QUERY = "(max-width: 599px)";

/** MediaWindow is the part of a window this module reads: window, or a test's. */
export interface MediaWindow {
  matchMedia(query: string): MediaQueryList;
}

/** isNarrow reports whether the window is narrow now. */
export function isNarrow(win: MediaWindow = globalThis.window): boolean {
  return win.matchMedia(NARROW_QUERY).matches;
}

/** onNarrowChange calls `onChange` when the window becomes narrow or wide; it returns the unsubscribe. */
export function onNarrowChange(win: MediaWindow, onChange: (narrow: boolean) => void): () => void {
  const query = win.matchMedia(NARROW_QUERY);
  const listener = (event: MediaQueryListEvent) => onChange(event.matches);
  query.addEventListener("change", listener);
  return () => query.removeEventListener("change", listener);
}
