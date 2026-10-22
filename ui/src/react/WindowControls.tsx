// The window's three buttons, Minimize, Maximize (or Restore) and Close, for
// an app that draws its own title bar on the desktop (a browser draws its
// own). From frostnotes' features/chrome/WindowControls.tsx and frostmail's
// features/toolbar/Toolbar.tsx, which drew the same three IconButtons
// (docs/specs/ui-package.md, WindowControls).
//
// A macOS platform may come later, with its traffic lights at the left; no
// code is here for it.
import { Copy, Minus, Square, X } from "lucide-react";
import { cx } from "./cx.js";
import { IconButton } from "./IconButton.js";

/** WindowCommand is what a window button asks for; an app's own command type may hold these kinds too. */
export type WindowCommand = { kind: "minimize" } | { kind: "toggleMaximize" } | { kind: "close" };

/** WindowPlatform is whose title bar buttons are drawn. */
export type WindowPlatform = "linux" | "windows";

export interface WindowControlsProps {
  /** The window is maximized, so the second button restores it. */
  maximized: boolean;
  onWindow: (command: WindowCommand) => void;
  /** "linux" draws the apps' IconButtons; "windows" draws Windows' flat caption buttons. "linux" by default. */
  platform?: WindowPlatform;
  /** Classes for the group, after the package's own. */
  className?: string;
}

/** The parts of the browser's navigator that windowPlatform reads. */
export interface PlatformHints {
  userAgent?: string;
  userAgentData?: { platform?: string };
}

/** windowPlatform is "windows" when the navigator says Windows, and "linux" otherwise, or with no navigator. */
export function windowPlatform(
  nav: PlatformHints | undefined = typeof navigator === "undefined" ? undefined : navigator,
): WindowPlatform {
  const named = `${nav?.userAgentData?.platform ?? ""} ${nav?.userAgent ?? ""}`;
  return /\bwin(dows|32|64)?\b/i.test(named) ? "windows" : "linux";
}

// Windows' caption close button is red on hover, #c42b1c, with white text.
// It is the platform's colour, not a token: it does not follow the accent or
// the scheme.
const WINDOWS_CLOSE_HOVER = "hover:bg-[#c42b1c] hover:text-white";

/** captionClass is a Windows caption button: 46px wide, the row's height, flat. */
function captionClass(close: boolean): string {
  return cx(
    "flex h-full w-[46px] shrink-0 items-center justify-center rounded-none text-secondary",
    close ? WINDOWS_CLOSE_HOVER : "hover:bg-selection-inactive",
  );
}

/** WindowControls draws Minimize, Maximize or Restore, and Close. */
export function WindowControls(props: WindowControlsProps) {
  const { maximized, onWindow, platform = "linux", className } = props;
  const toggleLabel = maximized ? "Restore" : "Maximize";
  const ToggleIcon = maximized ? Copy : Square;

  if (platform === "windows") {
    // A group that fills the row's height, with no gap between its buttons.
    const caption = (label: string, Icon: typeof Minus, command: WindowCommand) => (
      <button
        type="button"
        aria-label={label}
        title={label}
        onClick={() => onWindow(command)}
        className={captionClass(command.kind === "close")}
      >
        <Icon size={16} aria-hidden="true" />
      </button>
    );
    return (
      // biome-ignore lint/a11y/useSemanticElements: a fieldset draws a border and a legend; this is a row of buttons.
      <div role="group" aria-label="Window controls" className={cx("flex h-full shrink-0 items-stretch", className)}>
        {caption("Minimize", Minus, { kind: "minimize" })}
        {caption(toggleLabel, ToggleIcon, { kind: "toggleMaximize" })}
        {caption("Close", X, { kind: "close" })}
      </div>
    );
  }

  return (
    // biome-ignore lint/a11y/useSemanticElements: a fieldset draws a border and a legend; this is a row of buttons.
    <div role="group" aria-label="Window controls" className={cx("flex shrink-0 items-center gap-1", className)}>
      <IconButton label="Minimize" icon={Minus} onClick={() => onWindow({ kind: "minimize" })} />
      <IconButton label={toggleLabel} icon={ToggleIcon} onClick={() => onWindow({ kind: "toggleMaximize" })} />
      <IconButton label="Close" icon={X} onClick={() => onWindow({ kind: "close" })} />
    </div>
  );
}
