// Splitter (docs/specs/ui-package.md, Splitter): a focusable separator that
// moves 16 pixels per arrow key.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Splitter } from "../src/react/index.js";

afterEach(cleanup);

describe("Splitter", () => {
  it("is a separator at its value, with a name", () => {
    render(<Splitter orientation="vertical" label="Sidebar width" value={42.4} onStart={vi.fn()} onMove={vi.fn()} />);
    const splitter = screen.getByRole("separator", { name: "Sidebar width" });
    expect(splitter.getAttribute("aria-orientation")).toBe("vertical");
    expect(splitter.getAttribute("aria-valuenow")).toBe("42");
    expect(splitter.getAttribute("aria-valuemin")).toBe("0");
    expect(splitter.getAttribute("aria-valuemax")).toBe("100");
  });

  it("moves with the arrow keys of its orientation", () => {
    const onStart = vi.fn();
    const onMove = vi.fn();
    const onEnd = vi.fn();
    const { rerender } = render(
      <Splitter
        orientation="vertical"
        label="Sidebar width"
        value={50}
        onStart={onStart}
        onMove={onMove}
        onEnd={onEnd}
      />,
    );
    const splitter = screen.getByRole("separator");
    fireEvent.keyDown(splitter, { key: "ArrowRight" });
    expect(onMove).toHaveBeenLastCalledWith(16);
    fireEvent.keyDown(splitter, { key: "ArrowLeft" });
    expect(onMove).toHaveBeenLastCalledWith(-16);
    // A key the splitter does not take moves nothing.
    fireEvent.keyDown(splitter, { key: "ArrowUp" });
    expect(onMove).toHaveBeenCalledTimes(2);
    expect(onStart).toHaveBeenCalledTimes(2);
    expect(onEnd).toHaveBeenCalledTimes(2);

    rerender(
      <Splitter
        orientation="horizontal"
        label="Sidebar width"
        value={50}
        onStart={onStart}
        onMove={onMove}
        onEnd={onEnd}
      />,
    );
    fireEvent.keyDown(screen.getByRole("separator"), { key: "ArrowDown" });
    expect(onMove).toHaveBeenLastCalledWith(16);
    expect(screen.getByRole("separator").getAttribute("aria-orientation")).toBe("horizontal");
  });
});
