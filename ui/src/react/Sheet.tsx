// A modal sheet: a frame over the page, named by its title, closed by
// Escape, the close button or a click on the overlay outside it. Focus moves
// into the frame and returns where it was. Modelled on frostnotes'
// features/dialog/ConfirmDialog.tsx and frostmail's sheets
// (docs/specs/ui-package.md, Sheet).
//
// Render it at the app's root, beside the page it covers: the overlay is
// fixed, so it covers the frame the app fills.
import { X } from "lucide-react";
import { type KeyboardEvent, type PointerEvent, type ReactNode, type RefObject, useEffect, useId, useRef } from "react";
import { cx } from "./cx.js";
import { IconButton } from "./IconButton.js";

export interface SheetProps {
  /** The heading, which names the dialog (aria-labelledby). */
  title: string;
  /** A sentence under the heading (aria-describedby), when there is one. */
  description?: string;
  /** Escape, the close button, or a click on the overlay. */
  onClose: () => void;
  /** The close button's accessible name; "Close" by default. */
  closeLabel?: string;
  /** The frame's width: a number of pixels, or a CSS length; 480 by default. */
  width?: number | string;
  /** Classes for the frame, after the package's own (a radius, a height). */
  className?: string;
  /** Draw the close button; true by default. */
  showClose?: boolean;
  /** A click on the overlay closes it; true by default. A form sets false. */
  closeOnOverlay?: boolean;
  /**
   * Where focus starts: the frame by default, "first" for the first field a
   * user can type in or press, or a ref to the element itself.
   */
  initialFocus?: "first" | RefObject<HTMLElement | null>;
  /** Classes for the title, in place of the package's size and weight. */
  titleClassName?: string;
  children: ReactNode;
}

/** FIELDS are what "first" prefers: a place to type. */
const FIELDS = "input:not([type='hidden']), select, textarea";

/** CONTROLS are what it takes when the sheet has no field. */
const CONTROLS = "button, a[href], [tabindex]:not([tabindex='-1'])";

/** firstFocusable is the first field in root that takes focus, else the first control. */
function firstFocusable(root: HTMLElement): HTMLElement | null {
  for (const selector of [FIELDS, CONTROLS]) {
    for (const element of root.querySelectorAll<HTMLElement>(selector)) {
      if (element.hasAttribute("disabled") || element.getAttribute("aria-hidden") === "true") continue;
      if (element.closest("[hidden]") !== null) continue;
      return element;
    }
  }
  return null;
}

/** Sheet is a modal frame. */
export function Sheet(props: SheetProps) {
  const {
    title,
    description,
    onClose,
    closeLabel = "Close",
    width = 480,
    className,
    showClose = true,
    closeOnOverlay = true,
    initialFocus,
    titleClassName = "font-semibold text-empty",
    children,
  } = props;
  const titleId = useId();
  const descriptionId = useId();
  const frame = useRef<HTMLDivElement>(null);

  // The focus target is read once, when the sheet opens: a later change of
  // the prop does not move focus.
  const focusTarget = useRef(initialFocus);
  useEffect(() => {
    const before = document.activeElement;
    const root = frame.current;
    const target = focusTarget.current;
    if (root !== null) {
      if (target === "first") (firstFocusable(root) ?? root).focus();
      else if (target !== undefined) target.current?.focus();
      else root.focus();
    }
    return () => {
      if (before instanceof HTMLElement && before.isConnected) before.focus();
    };
  }, []);

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    // The app's own shortcuts do not run while the sheet has the keys.
    event.stopPropagation();
    if (event.key === "Escape") {
      event.preventDefault();
      onClose();
    }
  };

  const onPointerDown = (event: PointerEvent<HTMLDivElement>) => {
    if (closeOnOverlay && event.target === event.currentTarget) onClose();
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-backdrop" onPointerDown={onPointerDown}>
      <div
        ref={frame}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description === undefined ? undefined : descriptionId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
        style={{ width: typeof width === "number" ? `${width}px` : width }}
        className={cx(
          "max-h-[90%] max-w-[90%] overflow-auto rounded-xl border border-separator bg-window p-5 shadow-popover focus:outline-none",
          className,
        )}
      >
        <div className="flex items-start justify-between gap-4">
          <h2 id={titleId} className={titleClassName}>
            {title}
          </h2>
          {showClose && <IconButton label={closeLabel} icon={X} size={24} onClick={onClose} />}
        </div>
        {description === undefined ? null : (
          <p id={descriptionId} className="mt-2 text-secondary text-sidebar-row">
            {description}
          </p>
        )}
        <div className="mt-4">{children}</div>
      </div>
    </div>
  );
}
