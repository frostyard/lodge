// Sheet (docs/specs/ui-package.md, Sheet): a modal frame named by its title,
// closed by Escape, the close button or the overlay, which takes focus and
// gives it back.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Sheet } from "../src/react/index.js";

afterEach(cleanup);

describe("Sheet", () => {
  it("is a modal dialog named by its title, and described when it says so", () => {
    render(
      <Sheet title="Add a feed" description="The feed's address." onClose={vi.fn()}>
        <input aria-label="Feed address" />
      </Sheet>,
    );
    const dialog = screen.getByRole("dialog", { name: "Add a feed" });
    expect(dialog.getAttribute("aria-modal")).toBe("true");
    expect(dialog.getAttribute("aria-describedby")).not.toBeNull();
    expect(screen.getByRole("textbox", { name: "Feed address" })).toBeDefined();
  });

  it("closes on Escape, stopping the keys from reaching the app", () => {
    const onClose = vi.fn();
    const onApp = vi.fn();
    render(
      // The page's own key handler: a sheet's keys must not run it.
      // biome-ignore lint/a11y/noStaticElementInteractions: the parent listens for keys, to prove the sheet stops them
      <div onKeyDown={onApp}>
        <Sheet title="Settings" onClose={onClose}>
          <p>Body</p>
        </Sheet>
      </div>,
    );
    const dialog = screen.getByRole("dialog");
    fireEvent.keyDown(dialog, { key: "Escape" });
    expect(onClose).toHaveBeenCalledOnce();
    expect(onApp).not.toHaveBeenCalled();
  });

  it("closes from its close button, and from the overlay outside it", () => {
    const onClose = vi.fn();
    render(
      <Sheet title="Settings" closeLabel="Dismiss" onClose={onClose}>
        <p>Body</p>
      </Sheet>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(onClose).toHaveBeenCalledOnce();
    const overlay = screen.getByRole("dialog").parentElement;
    expect(overlay).not.toBeNull();
    if (overlay !== null) fireEvent.pointerDown(overlay);
    expect(onClose).toHaveBeenCalledTimes(2);
  });

  it("takes focus when it opens, and gives it back when it goes", () => {
    const page = render(<button type="button">Open</button>);
    const opener = screen.getByRole("button", { name: "Open" });
    opener.focus();
    expect(document.activeElement).toBe(opener);

    const sheet = render(
      <Sheet title="Settings" onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
    );
    expect(document.activeElement).toBe(screen.getByRole("dialog"));

    sheet.unmount();
    expect(document.activeElement).toBe(opener);
    page.unmount();
  });
});
