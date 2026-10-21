// Sheet (docs/specs/ui-package.md, Sheet): a modal frame named by its title,
// closed by Escape, the close button or the overlay, which takes focus and
// gives it back.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { createRef } from "react";
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

describe("Sheet's options", () => {
  it("takes a width in pixels or as a CSS length", () => {
    const { rerender } = render(
      <Sheet title="Add a feed" width={360} onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
    );
    expect((screen.getByRole("dialog") as HTMLElement).style.width).toBe("360px");
    rerender(
      <Sheet title="Add a feed" width="42rem" onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
    );
    expect((screen.getByRole("dialog") as HTMLElement).style.width).toBe("42rem");
  });

  it("is 480 pixels wide unless it is told otherwise", () => {
    render(
      <Sheet title="Settings" onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
    );
    expect((screen.getByRole("dialog") as HTMLElement).style.width).toBe("480px");
  });

  it("takes classes for its frame and for its title", () => {
    render(
      <Sheet title="Time" className="rounded-[10px]" titleClassName="text-[13px] font-semibold" onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
    );
    const frame = screen.getByRole("dialog") as HTMLElement;
    expect(frame.className).toContain("rounded-xl");
    expect(frame.className).toContain("rounded-[10px]");
    expect(screen.getByRole("heading", { name: "Time" }).className).toBe("text-[13px] font-semibold");
  });

  it("draws its close button unless it is told not to", () => {
    const { rerender } = render(
      <Sheet title="Settings" showClose={false} onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
    );
    expect(screen.queryByRole("button", { name: "Close" })).toBeNull();
    rerender(
      <Sheet title="Settings" onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
    );
    expect(screen.getByRole("button", { name: "Close" })).toBeDefined();
  });

  it("keeps the overlay from closing it when a form asks", () => {
    const onClose = vi.fn();
    const { rerender } = render(
      <Sheet title="Time" closeOnOverlay={false} onClose={onClose}>
        <p>Body</p>
      </Sheet>,
    );
    const overlay = () => screen.getByRole("dialog").parentElement as HTMLElement;
    fireEvent.pointerDown(overlay());
    expect(onClose).not.toHaveBeenCalled();
    rerender(
      <Sheet title="Time" onClose={onClose}>
        <p>Body</p>
      </Sheet>,
    );
    fireEvent.pointerDown(overlay());
    expect(onClose).toHaveBeenCalledOnce();
  });

  it('starts focus on the first field with initialFocus="first"', () => {
    render(
      <Sheet title="Time" initialFocus="first" onClose={vi.fn()}>
        <p>Body</p>
        <input aria-label="Date" />
      </Sheet>,
    );
    expect(document.activeElement).toBe(screen.getByRole("textbox", { name: "Date" }));
  });

  it("starts focus where a ref says", () => {
    const send = createRef<HTMLButtonElement>();
    render(
      <Sheet title="Time" initialFocus={send} onClose={vi.fn()}>
        <p>Body</p>
        <button type="button" ref={send}>
          Send
        </button>
      </Sheet>,
    );
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Send" }));
  });
});

describe("Sheet's name, role and portal", () => {
  it("is named by ariaLabel when it draws no heading", () => {
    render(
      <Sheet ariaLabel="Manage vaults" onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
    );
    expect(screen.getByRole("dialog", { name: "Manage vaults" })).toBeDefined();
    expect(screen.queryByRole("heading")).toBeNull();
  });

  it("cannot be built without a title or an ariaLabel", () => {
    // @ts-expect-error one of the two names is required
    const nameless = <Sheet onClose={vi.fn()} />;
    void nameless;
  });

  it("takes an alertdialog role", () => {
    render(
      <Sheet title="Delete?" role="alertdialog" onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
    );
    expect(screen.getByRole("alertdialog", { name: "Delete?" })).toBeDefined();
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("portals onto the body when it is asked to", () => {
    const container = document.createElement("div");
    document.body.append(container);
    render(
      <Sheet title="Settings" portal onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
      { container },
    );
    const dialog = screen.getByRole("dialog");
    expect(container.contains(dialog)).toBe(false);
    expect(document.body.contains(dialog)).toBe(true);
    container.remove();
  });

  it("stays where it is rendered by default", () => {
    const container = document.createElement("div");
    document.body.append(container);
    render(
      <Sheet title="Settings" onClose={vi.fn()}>
        <p>Body</p>
      </Sheet>,
      { container },
    );
    expect(container.contains(screen.getByRole("dialog"))).toBe(true);
    container.remove();
  });
});
