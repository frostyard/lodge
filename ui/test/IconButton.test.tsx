// IconButton (docs/specs/ui-package.md, IconButton): a button with an
// accessible name, its keys in the tooltip, and a size.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Search } from "lucide-react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { IconButton } from "../src/react/index.js";

afterEach(cleanup);

describe("IconButton", () => {
  it("renders a button named by its label, with its keys in the tooltip", () => {
    const onClick = vi.fn();
    render(<IconButton label="Search" keys="Ctrl + F" icon={Search} onClick={onClick} />);
    const button = screen.getByRole("button", { name: "Search" });
    expect(button.title).toBe("Search (Ctrl + F)");
    expect(button.getAttribute("aria-pressed")).toBeNull();
    fireEvent.click(button);
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("names a toggle, and reports it pressed or not", () => {
    const onClick = vi.fn();
    render(<IconButton label="Sidebar" icon={Search} pressed onClick={onClick} />);
    expect(screen.getByRole("button", { name: "Sidebar" }).getAttribute("aria-pressed")).toBe("true");
  });

  it("takes 28 or 24 pixels, and is disabled when asked", () => {
    const onClick = vi.fn();
    const { rerender } = render(<IconButton label="Next" icon={Search} size={24} onClick={onClick} />);
    expect(screen.getByRole("button", { name: "Next" }).className).toContain("size-6");
    rerender(<IconButton label="Next" icon={Search} disabled onClick={onClick} />);
    const button = screen.getByRole("button", { name: "Next" });
    expect(button.className).toContain("size-7");
    expect(button.hasAttribute("disabled")).toBe(true);
    fireEvent.click(button);
    expect(onClick).not.toHaveBeenCalled();
  });
});
