// The narrow-window hook: the page follows window.matchMedia(NARROW_QUERY)
// (docs/specs/ui-package.md, theme).
import { useSyncExternalStore } from "react";
import { isNarrow, onNarrowChange } from "../theme.js";

/** useNarrow reports whether the window is narrow, and follows its width. */
export function useNarrow(): boolean {
  return useSyncExternalStore(
    (onChange) => onNarrowChange(globalThis.window, () => onChange()),
    () => isNarrow(globalThis.window),
  );
}
