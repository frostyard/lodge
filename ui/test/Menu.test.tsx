// Menu (docs/specs/ui-package.md, Menu): rows with icons, check marks, keys
// and submenus; the keyboard; the outside click; the narrow window's sheet.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Search } from "lucide-react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Menu, type MenuEntry } from "../src/react/index.js";
import { ariaKeys } from "../src/react/Menu.js";

afterEach(cleanup);

/** items is a menu with every kind of row. */
function items(): MenuEntry[] {
  return [
    { kind: "item", id: "open", label: "Open", icon: <Search size={16} />, keys: "Ctrl + O" },
    { kind: "item", id: "pin", label: "Pin", checked: true },
    { kind: "item", id: "gone", label: "Gone", disabled: true },
    { kind: "separator" },
    {
      kind: "submenu",
      label: "Move to",
      items: [
        { kind: "item", id: "inbox", label: "Inbox" },
        { kind: "item", id: "archive-folder", label: "Archive" },
      ],
    },
  ];
}

describe("Menu", () => {
  it("draws the rows, with a check mark, keys and a disabled row", () => {
    render(<Menu label="Message" items={items()} onSelect={vi.fn()} onClose={vi.fn()} />);
    expect(screen.getByRole("menu", { name: "Message" })).toBeDefined();
    expect(screen.getByRole("menuitem", { name: /Open/ }).textContent).toContain("Ctrl + O");
    expect(screen.getByRole("menuitemcheckbox", { name: "Pin" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("menuitem", { name: "Gone" }).getAttribute("aria-disabled")).toBe("true");
    expect(screen.getByRole("menuitem", { name: /Move to/ }).getAttribute("aria-haspopup")).toBe("menu");
    expect(document.querySelectorAll("hr").length).toBe(1);
  });

  it("chooses with the keyboard, skipping the disabled row", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    render(<Menu items={items()} onSelect={onSelect} onClose={onClose} />);
    const menu = screen.getByRole("menu");
    fireEvent.keyDown(menu, { key: "ArrowDown" }); // Open
    fireEvent.keyDown(menu, { key: "ArrowDown" }); // Pin
    fireEvent.keyDown(menu, { key: "ArrowDown" }); // Gone is skipped: Move to
    fireEvent.keyDown(menu, { key: "ArrowUp" }); // Pin
    fireEvent.keyDown(menu, { key: "Enter" });
    expect(onSelect).toHaveBeenCalledWith("pin");
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("opens a submenu with the arrow key, and closes it with Escape", () => {
    render(<Menu items={items()} onSelect={vi.fn()} onClose={vi.fn()} />);
    const menu = screen.getByRole("menu");
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    fireEvent.keyDown(menu, { key: "ArrowRight" });
    expect(screen.getAllByRole("menu")).toHaveLength(2);
    expect(screen.getByRole("menuitem", { name: "Inbox" })).toBeDefined();
    fireEvent.keyDown(menu, { key: "ArrowLeft" });
    expect(screen.getAllByRole("menu")).toHaveLength(1);
  });

  it("chooses from the submenu", () => {
    const onSelect = vi.fn();
    const onClose = vi.fn();
    render(<Menu items={items()} onSelect={onSelect} onClose={onClose} />);
    const menu = screen.getByRole("menu");
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    fireEvent.keyDown(menu, { key: "ArrowDown" });
    fireEvent.keyDown(menu, { key: "ArrowRight" });
    fireEvent.click(screen.getByRole("menuitem", { name: "Archive" }));
    expect(onSelect).toHaveBeenCalledWith("archive-folder");
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("closes on Escape, and on a press outside it", () => {
    const onClose = vi.fn();
    render(<Menu items={items()} onSelect={vi.fn()} onClose={onClose} />);
    fireEvent.keyDown(screen.getByRole("menu"), { key: "Escape" });
    expect(onClose).toHaveBeenCalledOnce();
    fireEvent.pointerDown(document.body);
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("gives focus back to what had it", () => {
    const opener = render(<button type="button">Open</button>);
    const button = screen.getByRole("button", { name: "Open" });
    button.focus();
    const menu = render(<Menu items={items()} onSelect={vi.fn()} onClose={vi.fn()} />);
    expect(document.activeElement).toBe(screen.getByRole("menu"));
    menu.unmount();
    expect(document.activeElement).toBe(button);
    opener.unmount();
  });

  it("draws the narrow window's sheet", () => {
    render(<Menu items={items()} sheet onSelect={vi.fn()} onClose={vi.fn()} />);
    const menu = screen.getByRole("menu");
    expect(menu.className).toContain("bottom-0");
    expect(menu.className).toContain("inset-x-0");
    expect(screen.getByRole("menuitem", { name: /Open/ }).className).toContain("h-11");
  });
});

describe("a menu row's name", () => {
  it("is its label alone, with the keys in aria-keyshortcuts", () => {
    render(<Menu items={items()} onSelect={vi.fn()} onClose={vi.fn()} />);
    const open = screen.getByRole("menuitem", { name: "Open" });
    expect(open.getAttribute("aria-keyshortcuts")).toBe("Control+O");
    // No row takes its keys into its name.
    expect(screen.queryByRole("menuitem", { name: /Ctrl/ })).toBeNull();
  });

  it("leaves aria-keyshortcuts off when a part of the keys has no ARIA name", () => {
    render(
      <Menu
        items={[{ kind: "item", id: "x", label: "Compose", keys: "Ctrl+Shift+Whatever" }]}
        onSelect={vi.fn()}
        onClose={vi.fn()}
      />,
    );
    const compose = screen.getByRole("menuitem", { name: "Compose" });
    expect(compose.hasAttribute("aria-keyshortcuts")).toBe(false);
  });
});

describe("ariaKeys", () => {
  it("converts displayed keys to aria-keyshortcuts' names", () => {
    expect(ariaKeys("Ctrl+Shift+K")).toBe("Control+Shift+K");
    expect(ariaKeys("Ctrl + Alt + A")).toBe("Control+Alt+A");
    expect(ariaKeys("Ctrl+,")).toBe("Control+,");
    expect(ariaKeys("Ctrl+Shift+Backquote")).toBe("Control+Shift+`");
    expect(ariaKeys("Delete")).toBe("Delete");
    expect(ariaKeys("F2")).toBe("F2");
    expect(ariaKeys("Meta+ArrowLeft")).toBe("Meta+ArrowLeft");
  });

  it("answers nothing when a part has no name there", () => {
    expect(ariaKeys("Ctrl+Shift+Whatever")).toBe("");
    expect(ariaKeys("")).toBe("");
  });
});
