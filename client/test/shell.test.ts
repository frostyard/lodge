import { afterEach, describe, expect, it, vi } from "vitest";
import { connectLodge } from "../src/index.js";
import { createShell, resolveOpen, type ShellEvents } from "../src/shell.js";
import { CONTENT, FakeFrame, FakeShellWindow, key, ORIGIN, pair, settle } from "./harness.js";

const fields = { contentOrigin: CONTENT, shortcut: "Ctrl+Backquote", theme: "light" as const };

function setup(url = `${ORIGIN}/app/notes/1/Plan.md`) {
  const p = pair(url);
  const host = createShell({ window: p.shell, init: () => fields });
  const events: [keyof ShellEvents, unknown][] = [];
  for (const type of ["ready", "mismatch", "unresponsive", "route", "context", "open", "shortcut"] as const) {
    host.on(type, (e) => events.push([type, e]));
  }
  const port = host.attach("notes", p.frame);
  return { ...p, host, port, events };
}

/** app sends what the app's frame would post, from the frame. */
function app(p: ReturnType<typeof setup>, data: unknown) {
  p.parent.postMessage(data, ORIGIN);
}

afterEach(() => {
  vi.useRealTimers();
});

describe("the shell half", () => {
  it("answers ready with init, then visible, then the queued navigates", async () => {
    const p = setup();
    p.port.visible(true);
    p.port.navigate("/1/A.md");
    p.port.navigate("/1/B.md");
    expect(p.sent.toApp).toEqual([]);
    app(p, { lodge: 1, type: "ready", contract: 1 });
    await settle();
    expect(p.port.state).toBe("ready");
    expect(p.sent.toApp.map((s) => s.data)).toEqual([
      { lodge: 1, type: "init", contract: 1, ...fields },
      { lodge: 1, type: "visible", visible: true },
      { lodge: 1, type: "navigate", route: "/1/A.md" },
      { lodge: 1, type: "navigate", route: "/1/B.md" },
    ]);
    expect(p.sent.toApp.every((s) => s.targetOrigin === ORIGIN)).toBe(true);
    expect(p.events).toEqual([["ready", { app: "notes" }]]);
  });

  it("reports a ready app's messages by its frame, and drops the rest", async () => {
    const p = setup();
    app(p, { lodge: 1, type: "route", path: "/early", title: "" }); // before ready
    app(p, { lodge: 1, type: "ready", contract: 1 });
    app(p, { lodge: 1, type: "route", path: "/1/Plan.md", title: "Plan", replace: true });
    app(p, { lodge: 1, type: "route", path: "/1/x", title: "t".repeat(300) });
    app(p, { lodge: 1, type: "context", kind: "note", ref: "notes/1/Plan.md", title: "Plan" });
    app(p, { lodge: 1, type: "context", kind: "Not A Kind", ref: "", title: "" });
    app(p, { lodge: 1, type: "open", url: "/mail/thread/42" });
    app(p, { lodge: 1, type: "shortcut", key: "Ctrl+Backquote" });
    app(p, { lodge: 2, type: "route", path: "/v2", title: "" });
    app(p, { lodge: 1, type: "route", path: "relative", title: "" });
    app(p, { lodge: 1, type: "route", path: `/${"x".repeat(2048)}`, title: "" });
    // Right origin, wrong source; and right source, wrong origin.
    p.shell.deliver({ lodge: 1, type: "open", url: "/forged" }, ORIGIN, ORIGIN, {});
    p.shell.deliver({ lodge: 1, type: "open", url: "/forged" }, ORIGIN, "https://evil.test", p.frameWindow);
    await settle();
    expect(p.events).toEqual([
      ["ready", { app: "notes" }],
      ["route", { app: "notes", path: "/1/Plan.md", title: "Plan", replace: true }],
      ["route", { app: "notes", path: "/1/x", title: "t".repeat(200), replace: false }],
      ["context", { app: "notes", kind: "note", ref: "notes/1/Plan.md", title: "Plan" }],
      ["open", { app: "notes", url: "/mail/thread/42" }],
      ["shortcut", { app: "notes", key: "Ctrl+Backquote" }],
    ]);
  });

  it("marks a frame of another major as mismatched, and sends it nothing", async () => {
    const p = setup();
    p.port.navigate("/1/A.md");
    app(p, { lodge: 2, type: "ready", contract: 2 });
    app(p, { lodge: 1, type: "open", url: "/x" });
    await settle();
    expect(p.port.state).toBe("mismatched");
    expect(p.sent.toApp).toEqual([]);
    expect(p.events).toEqual([["mismatch", { app: "notes", contract: 2 }]]);
  });

  it("shows a frame that sends no ready within 10 s of load as unresponsive", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const p = setup();
    p.frame.load();
    vi.advanceTimersByTime(9_999);
    expect(p.port.state).toBe("loading");
    vi.advanceTimersByTime(1);
    expect(p.port.state).toBe("unresponsive");
    app(p, { lodge: 1, type: "ready", contract: 1 });
    await settle();
    expect(p.port.state).toBe("ready");
    expect(p.events.map((e) => e[0])).toEqual(["unresponsive", "ready"]);
  });

  it("keeps a ready that came before load, and waits again after a reload", async () => {
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    const p = setup();
    app(p, { lodge: 1, type: "ready", contract: 1 });
    await settle();
    p.frame.load(); // the same document's load
    vi.advanceTimersByTime(20_000);
    expect(p.port.state).toBe("ready");
    p.frame.load(); // a reload whose document never answers
    expect(p.port.state).toBe("loading");
    p.port.navigate("/1/Later.md");
    vi.advanceTimersByTime(10_000);
    expect(p.port.state).toBe("unresponsive");
    p.sent.toApp.length = 0;
    app(p, { lodge: 1, type: "ready", contract: 1 });
    await settle();
    expect(p.sent.toApp.map((s) => (s.data as { type: string }).type)).toEqual(["init", "navigate"]);
  });

  it("sends theme to ready frames, and stops on detach and close", async () => {
    const p = setup();
    const other = pair(`${ORIGIN}/app/mail/`, p.shell);
    const mail = p.host.attach("mail", other.frame);
    app(p, { lodge: 1, type: "ready", contract: 1 });
    await settle();
    p.host.theme("dark");
    expect(p.sent.toApp.at(-1)?.data).toEqual({ lodge: 1, type: "theme", scheme: "dark" });
    expect(other.sent.toApp).toEqual([]); // mail is still loading
    mail.detach();
    expect(p.host.port("mail")).toBeUndefined();
    p.host.close();
    app(p, { lodge: 1, type: "open", url: "/x" });
    await settle();
    expect(p.events.filter((e) => e[0] === "open")).toEqual([]);
  });

  it("refuses an app id that is not one", () => {
    const shell = createShell({ window: new FakeShellWindow(), init: () => fields });
    expect(() => shell.attach("Lodge", new FakeFrame({ postMessage() {} }))).toThrow(TypeError);
  });
});

describe("both halves", () => {
  it("connect, route, navigate and catch the shortcut", async () => {
    const p = setup(`${ORIGIN}/app/notes/1/Plan.md`);
    p.port.visible(true);
    p.port.navigate("/1/Deep.md");
    const lodge = await connectLodge({ app: "notes", window: p.app });
    const navigated: string[] = [];
    lodge.on("navigate", (e) => navigated.push(e.route));
    await settle();
    expect([lodge.theme, lodge.contentOrigin, lodge.visible]).toEqual(["light", CONTENT, true]);
    expect(navigated).toEqual(["/1/Deep.md"]);
    lodge.history.push("/1/Deep.md", "Deep");
    p.app.dispatchEvent(key("Backquote", { ctrlKey: true }));
    await settle();
    expect(p.events.slice(1)).toEqual([
      ["route", { app: "notes", path: "/1/Deep.md", title: "Deep", replace: false }],
      ["shortcut", { app: "notes", key: "Ctrl+Backquote" }],
    ]);
  });
});

describe("resolveOpen", () => {
  const origins = { lodge: ORIGIN, content: CONTENT };
  it.each([
    ["/mail/thread/42", { kind: "app", app: "mail", route: "/thread/42" }],
    ["/mail", { kind: "app", app: "mail", route: "/" }],
    ["/notes/1/A.md?q=1#h", { kind: "app", app: "notes", route: "/1/A.md?q=1#h" }],
    [`${ORIGIN}/notes/1/A.md`, { kind: "app", app: "notes", route: "/1/A.md" }],
    [`${CONTENT}/notes/blobs/x.pdf`, { kind: "content", url: `${CONTENT}/notes/blobs/x.pdf` }],
    ["https://example.com/a", { kind: "external", url: "https://example.com/a" }],
    ["http://example.com/", { kind: "external", url: "http://example.com/" }],
    ["mailto:alice@example.com", { kind: "external", url: "mailto:alice@example.com" }],
  ])("%s", (url, want) => {
    expect(resolveOpen(url, origins)).toEqual(want);
  });
  it.each([
    "/app/notes/x",
    "/lodge/apps",
    "/mcp/notes",
    "/device/notes/pair",
    "/",
    "/Notes/x",
    "relative/path",
    "//example.com/x",
    "javascript:alert(1)",
    "data:text/html,x",
    "file:///etc/passwd",
    `${ORIGIN}/app/mail/`,
  ])("refuses %s", (url) => {
    expect(resolveOpen(url, origins)).toBeNull();
  });
  it("has no content kind without a content origin", () => {
    expect(resolveOpen(`${CONTENT}/notes/x`, { lodge: ORIGIN, content: null })).toEqual({
      kind: "external",
      url: `${CONTENT}/notes/x`,
    });
  });
});
