// WindowControls (docs/specs/ui-package.md, WindowControls): the window's
// three buttons, in the apps' look or in Windows'; and windowPlatform.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { WindowControls, windowPlatform } from "../src/react/index.js";

afterEach(cleanup);

/** names are the accessible names of the group's buttons, in order. */
function names(): string[] {
  return screen.getAllByRole("button").map((button) => button.getAttribute("aria-label") ?? "");
}

describe("WindowControls", () => {
  it("draws Minimize, Maximize and Close in a named group", () => {
    render(<WindowControls maximized={false} onWindow={vi.fn()} />);
    const group = screen.getByRole("group", { name: "Window controls" });
    expect(names()).toEqual(["Minimize", "Maximize", "Close"]);
    expect(group.querySelectorAll("button").length).toBe(3);
    expect(group.hasAttribute("data-tauri-drag-region")).toBe(false);
  });

  it("draws what the apps drew: a row of three IconButtons with a gap of 4px", () => {
    render(<WindowControls maximized={false} onWindow={vi.fn()} className="ml-2" />);
    const group = screen.getByRole("group", { name: "Window controls" });
    expect(group.className).toBe("flex shrink-0 items-center gap-1 ml-2");
    for (const button of screen.getAllByRole("button")) expect(button.className).toContain("size-7");
  });

  it("says Restore when the window is maximized", () => {
    render(<WindowControls maximized onWindow={vi.fn()} />);
    expect(names()).toEqual(["Minimize", "Restore", "Close"]);
    expect(screen.queryByRole("button", { name: "Maximize" })).toBeNull();
  });

  it("asks for each command", () => {
    const onWindow = vi.fn();
    render(<WindowControls maximized={false} onWindow={onWindow} />);
    fireEvent.click(screen.getByRole("button", { name: "Minimize" }));
    fireEvent.click(screen.getByRole("button", { name: "Maximize" }));
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onWindow.mock.calls).toEqual([[{ kind: "minimize" }], [{ kind: "toggleMaximize" }], [{ kind: "close" }]]);
  });

  it("draws the same buttons, in the same order, for Windows", () => {
    const onWindow = vi.fn();
    const { rerender } = render(<WindowControls maximized={false} onWindow={onWindow} platform="windows" />);
    const group = screen.getByRole("group", { name: "Window controls" });
    expect(names()).toEqual(["Minimize", "Maximize", "Close"]);
    expect(screen.getByRole("button", { name: "Close" }).title).toBe("Close");
    rerender(<WindowControls maximized onWindow={onWindow} platform="windows" />);
    expect(names()).toEqual(["Minimize", "Restore", "Close"]);
    expect(group.hasAttribute("data-tauri-drag-region")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Minimize" }));
    fireEvent.click(screen.getByRole("button", { name: "Restore" }));
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(onWindow.mock.calls).toEqual([[{ kind: "minimize" }], [{ kind: "toggleMaximize" }], [{ kind: "close" }]]);
  });

  it("draws Windows' buttons flat, 46px wide, the row's height, with a red Close", () => {
    render(<WindowControls maximized={false} onWindow={vi.fn()} platform="windows" />);
    const group = screen.getByRole("group", { name: "Window controls" });
    expect(group.className).toContain("h-full");
    expect(group.className).not.toContain("gap-");
    for (const button of screen.getAllByRole("button")) {
      expect(button.className).toContain("w-[46px]");
      expect(button.className).toContain("h-full");
      expect(button.className).toContain("rounded-none");
    }
    expect(screen.getByRole("button", { name: "Minimize" }).className).toContain("hover:bg-selection-inactive");
    expect(screen.getByRole("button", { name: "Close" }).className).toContain("hover:bg-[#c42b1c]");
    expect(screen.getByRole("button", { name: "Close" }).className).toContain("hover:text-white");
  });
});

describe("windowPlatform", () => {
  it("says windows for a Windows user agent", () => {
    expect(windowPlatform({ userAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36" })).toBe(
      "windows",
    );
  });

  it("says linux for a Linux user agent, and for the macOS one it has no code for", () => {
    expect(windowPlatform({ userAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36" })).toBe("linux");
    expect(windowPlatform({ userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Darwin" })).toBe("linux");
  });

  it("reads userAgentData's platform first", () => {
    expect(windowPlatform({ userAgent: "", userAgentData: { platform: "Windows" } })).toBe("windows");
    expect(windowPlatform({ userAgent: "", userAgentData: { platform: "Linux" } })).toBe("linux");
  });

  it("does not throw with no navigator, and says linux", () => {
    expect(windowPlatform(undefined)).toBe("linux");
    expect(windowPlatform({})).toBe("linux");
    vi.stubGlobal("navigator", undefined);
    try {
      expect(windowPlatform()).toBe("linux");
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
