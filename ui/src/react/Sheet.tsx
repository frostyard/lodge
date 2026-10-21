// A modal sheet: a frame over the page, named by its title, closed by
// Escape, the close button or a click on the overlay outside it. Focus moves
// into the frame and returns where it was. Modelled on frostnotes'
// features/dialog/ConfirmDialog.tsx and frostmail's sheets
// (docs/specs/ui-package.md, Sheet).
//
// Render it at the app's root, beside the page it covers: the overlay is
// fixed, so it covers the frame the app fills.
import { X } from "lucide-react";
import { type KeyboardEvent, type PointerEvent, type ReactNode, useEffect, useId, useRef } from "react";
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
  children: ReactNode;
}

/** Sheet is a modal frame. */
export function Sheet(props: SheetProps) {
  const { title, description, onClose, closeLabel = "Close", children } = props;
  const titleId = useId();
  const descriptionId = useId();
  const frame = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const before = document.activeElement;
    frame.current?.focus();
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
    if (event.target === event.currentTarget) onClose();
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
        className="max-h-[90%] w-[480px] max-w-[90%] overflow-auto rounded-xl border border-separator bg-window p-5 shadow-popover focus:outline-none"
      >
        <div className="flex items-start justify-between gap-4">
          <h2 id={titleId} className="font-semibold text-empty">
            {title}
          </h2>
          <IconButton label={closeLabel} icon={X} size={24} onClick={onClose} />
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
