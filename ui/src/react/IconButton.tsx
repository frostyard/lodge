// A chrome button: an icon, an accessible name, and a tooltip naming it and
// its keys. 28 × 28 in a title bar row and a ribbon, 24 × 24 in toolbars and
// view headers, and 44 × 44 under a coarse (touch) pointer. From frostnotes'
// features/chrome/IconButton.tsx (docs/specs/ui-package.md, IconButton).
import type { LucideIcon } from "lucide-react";
import type { MouseEvent } from "react";
import { cx } from "./cx.js";

export interface IconButtonProps {
  /** The accessible name, and the tooltip's first word. */
  label: string;
  /** The keys that run it, as a keyboard shortcut is written ("Ctrl + O"). */
  keys?: string;
  /** A lucide icon component, drawn at 16px. */
  icon: LucideIcon;
  size?: 28 | 24;
  /** A toggle that is on. */
  pressed?: boolean;
  disabled?: boolean;
  onClick: (event: MouseEvent<HTMLButtonElement>) => void;
}

/** iconButtonClass is a chrome button's look at a size, pressed or not. */
export function iconButtonClass(size: 28 | 24 = 28, selected = false): string {
  return cx(
    "flex shrink-0 items-center justify-center rounded-md text-secondary hover:bg-selection-inactive disabled:opacity-40 disabled:hover:bg-transparent",
    size === 24 ? "size-6" : "size-7",
    "pointer-coarse:size-11 pointer-coarse:[&_svg]:size-5",
    selected && "bg-selection-sidebar",
  );
}

/** IconButton is a chrome button. */
export function IconButton(props: IconButtonProps) {
  const Icon = props.icon;
  return (
    <button
      type="button"
      aria-pressed={props.pressed}
      aria-label={props.label}
      title={props.keys ? `${props.label} (${props.keys})` : props.label}
      disabled={props.disabled}
      onClick={props.onClick}
      className={iconButtonClass(props.size, props.pressed)}
    >
      <Icon size={16} aria-hidden="true" />
    </button>
  );
}
