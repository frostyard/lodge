// A menu at a point in the window: items with an icon, a check mark, a
// shortcut and a submenu, separators, disabled rows, keyboard navigation and
// outside-click closing; and the narrow window's sheet, the same rows from
// the foot of the window. From frostmail's features/menu/ContextMenu.tsx and
// frostnotes' features/menu/Menu.tsx (docs/specs/ui-package.md, Menu).
//
// Render it at the app's root: the menu and its submenus are portalled onto
// the document's body, which needs react-dom (an optional peer).
import { Check, ChevronRight } from "lucide-react";
import {
  type CSSProperties,
  type KeyboardEvent,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { cx } from "./cx.js";

/** MenuEntry is one row of a menu. */
export type MenuEntry =
  | {
      kind: "item";
      /** What onSelect receives. */
      id: string;
      label: string;
      /** A 16px lucide icon or a swatch before the label. */
      icon?: ReactNode;
      /** The keys that run it, shown at the right ("Ctrl + Shift + U"). */
      keys?: string;
      /** A checkable item: checked or not; a plain item when absent. */
      checked?: boolean;
      disabled?: boolean;
    }
  | { kind: "separator" }
  | {
      kind: "submenu";
      /** What onSelect receives, when the row is also a command. */
      id?: string;
      label: string;
      icon?: ReactNode;
      disabled?: boolean;
      items: MenuEntry[];
    };

export interface MenuProps {
  /** The menu's accessible name; "Menu" by default. */
  label?: string;
  items: MenuEntry[];
  /** Where the menu's top left corner goes, in viewport pixels. */
  x?: number;
  y?: number;
  /** An enabled item was chosen; onClose follows. */
  onSelect: (id: string) => void;
  /** The menu should disappear. */
  onClose: () => void;
  /**
   * A sheet from the bottom, the window's width, rows 44 pixels high, as a
   * narrow window draws menus; x and y are then not used.
   */
  sheet?: boolean;
}

/** Level is one open menu: its rows, the highlighted row, and the row whose submenu is open. */
interface Level {
  items: MenuEntry[];
  highlighted: number | null;
  openIndex: number | null;
}

const EDGE_GAP = 4;

function usable(entry: MenuEntry | undefined): boolean {
  return entry !== undefined && entry.kind !== "separator" && entry.disabled !== true;
}

/** nextIndex returns the next usable row after from, stepping and wrapping, or null. */
function nextIndex(items: MenuEntry[], from: number, step: number): number | null {
  const n = items.length;
  for (let i = 1; i <= n; i++) {
    const index = (((from + step * i) % n) + n) % n;
    if (usable(items[index])) return index;
  }
  return null;
}

function submenuLevel(items: MenuEntry[]): Level {
  return { items, highlighted: nextIndex(items, -1, 1), openIndex: null };
}

/** withSubmenu replaces the level at depth with its opened row and appends the submenu. */
function withSubmenu(previous: Level[], depth: number, index: number, items: MenuEntry[]): Level[] {
  const level = previous[depth];
  if (!level) return previous;
  const opened: Level = { ...level, highlighted: index, openIndex: index };
  return [...previous.slice(0, depth), opened, submenuLevel(items)];
}

function clamp(value: number, max: number): number {
  return Math.max(0, Math.min(value, max));
}

/** rowClass is a row's look: highlighted, disabled, or plain. */
function rowClass(highlighted: boolean, disabled: boolean, sheet: boolean): string {
  return cx(
    "flex w-full cursor-default items-center gap-2 rounded border-0 text-left text-sidebar-row",
    sheet ? "h-11 px-2" : "h-7 px-2 pointer-coarse:h-11",
    disabled ? "opacity-40" : highlighted ? "bg-accent text-accent-contrast" : "bg-transparent text-primary",
  );
}

/** Menu is a popup menu with keyboard navigation, submenus and a sheet form. */
export function Menu(props: MenuProps) {
  const { items, x = 0, y = 0, label = "Menu", sheet = false } = props;
  const [levels, setLevels] = useState<Level[]>([{ items, highlighted: null, openIndex: null }]);
  const [pos, setPos] = useState({ left: x, top: y });
  const root = useRef<HTMLDivElement | null>(null);
  const menus = useRef<(HTMLDivElement | null)[]>([]);
  const rows = useRef<(HTMLButtonElement | null)[][]>([]);

  // Focus goes to the menu, and back to what had it when the menu closes.
  useLayoutEffect(() => {
    const before = document.activeElement;
    root.current?.focus();
    return () => {
      if (before instanceof HTMLElement && before.isConnected) before.focus();
    };
  }, []);

  useLayoutEffect(() => {
    const element = root.current;
    if (element === null || sheet) return;
    const left = clamp(x, window.innerWidth - element.offsetWidth - EDGE_GAP);
    const top = clamp(y, window.innerHeight - element.offsetHeight - EDGE_GAP);
    setPos((previous) => (previous.left === left && previous.top === top ? previous : { left, top }));
  }, [x, y, sheet]);

  useLayoutEffect(() => {
    for (let depth = 1; depth < levels.length; depth++) {
      const anchorIndex = levels[depth - 1]?.openIndex;
      if (anchorIndex === null || anchorIndex === undefined) continue;
      const anchor = rows.current[depth - 1]?.[anchorIndex];
      const sub = menus.current[depth];
      if (!anchor || !sub) continue;
      // Beside its row, flipped to the parent's left when the window ends
      // first, and moved up so its last rows stay in the window.
      const rect = anchor.getBoundingClientRect();
      const parent = menus.current[depth - 1]?.getBoundingClientRect();
      const maxLeft = window.innerWidth - sub.offsetWidth - EDGE_GAP;
      const left = rect.right > maxLeft && parent ? parent.left - sub.offsetWidth : rect.right;
      sub.style.left = `${Math.round(clamp(left, maxLeft))}px`;
      sub.style.top = `${Math.round(clamp(rect.top, window.innerHeight - sub.offsetHeight - EDGE_GAP))}px`;
    }
  }, [levels]);

  useEffect(() => {
    const outside = (event: PointerEvent) => {
      const target = event.target;
      if (!(target instanceof Node)) return;
      for (const element of menus.current) if (element?.contains(target)) return;
      props.onClose();
    };
    document.addEventListener("pointerdown", outside, true);
    return () => document.removeEventListener("pointerdown", outside, true);
  }, [props.onClose]);

  const setHighlight = (depth: number, index: number | null) => {
    setLevels((previous) => previous.map((level, i) => (i === depth ? { ...level, highlighted: index } : level)));
  };

  const openSubmenu = (depth: number, index: number) => {
    setLevels((previous) => {
      const row = previous[depth]?.items[index];
      if (row?.kind !== "submenu" || row.disabled === true) return previous;
      return withSubmenu(previous, depth, index, row.items);
    });
  };

  const closeSubmenu = (depth: number) => {
    setLevels((previous) => {
      const parent = previous[depth - 1];
      if (depth < 1 || !parent) return previous;
      const restored: Level = { ...parent, highlighted: parent.openIndex ?? parent.highlighted, openIndex: null };
      return [...previous.slice(0, depth - 1), restored];
    });
  };

  const hover = (depth: number, index: number) => {
    setLevels((previous) => {
      const level = previous[depth];
      const row = level?.items[index];
      if (!level || row === undefined || row.kind === "separator" || row.disabled === true) return previous;
      if (row.kind === "submenu") {
        if (level.openIndex === index) return previous;
        return withSubmenu(previous, depth, index, row.items);
      }
      return [...previous.slice(0, depth), { ...level, highlighted: index }];
    });
  };

  const attachRow = (depth: number, index: number) => (element: HTMLButtonElement | null) => {
    const level = rows.current[depth] ?? [];
    level[index] = element;
    rows.current[depth] = level;
    if (element === null) return;
    const enter = () => hover(depth, index);
    element.addEventListener("mouseenter", enter);
    return () => element.removeEventListener("mouseenter", enter);
  };

  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const depth = levels.length - 1;
    const level = levels[depth];
    if (!level) return;
    const current = level.highlighted === null ? undefined : level.items[level.highlighted];
    if (event.key === "Tab") {
      event.preventDefault();
      props.onClose();
      return;
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const step = event.key === "ArrowDown" ? 1 : -1;
      const from = level.highlighted ?? (step > 0 ? -1 : level.items.length);
      setHighlight(depth, nextIndex(level.items, from, step));
    } else if (event.key === "ArrowRight") {
      if (current?.kind === "submenu" && level.highlighted !== null) {
        event.preventDefault();
        openSubmenu(depth, level.highlighted);
      }
    } else if (event.key === "ArrowLeft") {
      if (depth > 0) {
        event.preventDefault();
        closeSubmenu(depth);
      }
    } else if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      if (current?.kind === "submenu") {
        if (level.highlighted !== null) openSubmenu(depth, level.highlighted);
      } else if (current?.kind === "item") {
        props.onSelect(current.id);
        props.onClose();
      }
    } else if (event.key === "Escape") {
      event.preventDefault();
      if (depth > 0) closeSubmenu(depth);
      else props.onClose();
    }
  };

  const renderRow = (depth: number, level: Level, entry: MenuEntry, index: number, deepest: boolean) => {
    if (entry.kind === "separator") {
      // biome-ignore lint/suspicious/noArrayIndexKey: a separator is known by its place alone
      return <hr key={`separator-${index}`} className="my-1 h-px border-0 bg-separator" />;
    }
    const checkable = entry.kind === "item" && entry.checked !== undefined;
    const checked = entry.kind === "item" && entry.checked === true;
    const disabled = entry.disabled === true;
    const open = level.openIndex === index;
    const highlighted = deepest && !open && level.highlighted === index;
    const choose = () => {
      if (disabled) return;
      if (entry.kind === "item") {
        props.onSelect(entry.id);
        props.onClose();
      } else {
        openSubmenu(depth, index);
      }
    };
    const content = (
      <>
        <span className="flex w-4 shrink-0 items-center justify-center" aria-hidden="true">
          {checked ? <Check size={14} /> : entry.icon}
        </span>
        <span className="min-w-0 flex-1 truncate">{entry.label}</span>
        {entry.kind === "item" && entry.keys ? (
          <span className={cx("ml-4 shrink-0 text-list-date", highlighted ? "" : "text-tertiary")}>{entry.keys}</span>
        ) : null}
        {entry.kind === "submenu" ? (
          <ChevronRight size={14} className={cx("shrink-0", highlighted ? "" : "text-secondary")} aria-hidden="true" />
        ) : null}
      </>
    );
    const common = {
      type: "button" as const,
      ref: attachRow(depth, index),
      className: rowClass(highlighted, disabled, sheet),
      "aria-disabled": disabled ? true : undefined,
      "data-highlighted": highlighted ? "true" : undefined,
      "data-open": open ? "true" : undefined,
      onClick: choose,
    };
    // ARIA allows aria-checked only on checkable roles, so checkable items
    // are menuitemcheckbox and the rest menuitem.
    if (checkable) {
      return (
        // biome-ignore lint/suspicious/noArrayIndexKey: a row is known by its place, which its keys identify
        <button key={index} role="menuitemcheckbox" aria-checked={checked} {...common}>
          {content}
        </button>
      );
    }
    return (
      // biome-ignore lint/suspicious/noArrayIndexKey: a row is known by its place, which its keys identify
      <button
        key={index}
        role="menuitem"
        aria-haspopup={entry.kind === "submenu" ? "menu" : undefined}
        aria-expanded={entry.kind === "submenu" ? open : undefined}
        {...common}
      >
        {content}
      </button>
    );
  };

  const renderMenu = (depth: number) => {
    const level = levels[depth];
    if (!level) return null;
    const deepest = depth === levels.length - 1;
    const style: CSSProperties =
      depth > 0
        ? { position: "fixed" }
        : sheet
          ? { position: "fixed" }
          : { position: "fixed", left: pos.left, top: pos.top };
    return (
      <div
        key={depth}
        ref={(element) => {
          menus.current[depth] = element;
          if (depth === 0) root.current = element;
        }}
        role="menu"
        aria-label={depth === 0 ? label : undefined}
        tabIndex={depth === 0 ? -1 : undefined}
        className={cx(
          "z-50 overflow-y-auto border-separator bg-window outline-none",
          sheet
            ? "fixed inset-x-0 bottom-0 max-h-[70vh] rounded-t-xl border-t p-2 shadow-popover"
            : "min-w-[180px] rounded-lg border p-1 shadow-popover",
        )}
        style={sheet ? { paddingBottom: "calc(0.5rem + env(safe-area-inset-bottom))" } : style}
        onKeyDown={depth === 0 ? onKeyDown : undefined}
      >
        {level.items.map((entry, index) => renderRow(depth, level, entry, index, deepest))}
        {level.openIndex === null ? null : renderMenu(depth + 1)}
      </div>
    );
  };

  return createPortal(renderMenu(0), document.body);
}
