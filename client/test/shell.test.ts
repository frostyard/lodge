import { afterEach, describe, expect, it, vi } from "vitest";
import { connectLodge } from "../src/index.js";
import { createShell, resolveOpen, type ShellEvents } from "../src/shell.js";
import { CONTENT, FakeFrame, FakeShellWindow, key, ORIGIN, pair, settle } from "./harness.js";

const fields = { contentOrigin: CONTENT, shortcut: "Ctrl+Backquote", companion: null, theme: "light" as const };

function setup(url = `${ORIGIN}/app/notes/1/Plan.md`) {
  const p = pair(url);
  const host = createShell({ window: p.shell, init: () => fields });
  const events: [keyof ShellEvents, unknown][] = [];
  for (const type of ["ready", "mismatch", "unresponsive", "route", "context", "ask", "open", "shortcut"] as const) {
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
      { lodge: 1, type: "init", contract: 1, ...fields, role: "app" },
      { lodge: 1, type: "visible", visible: true },
      { lodge: 1, type: "navigate", route: "/1/A.md" },
      { lodge: 1, type: "navigate", route: "/1/B.md" },
    ]);
    expect(p.sent.toApp.every((s) => s.targetOrigin === ORIGIN)).toBe(true);
    expect(p.events).toEqual([["ready", { app: "notes", role: "app" }]]);
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
      ["ready", { app: "notes", role: "app" }],
      ["route", { app: "notes", path: "/1/Plan.md", title: "Plan", replace: true }],
      ["route", { app: "notes", path: "/1/x", title: "t".repeat(200), replace: false }],
      ["context", { app: "notes", kind: "note", ref: "notes/1/Plan.md", title: "Plan" }],
      ["open", { app: "notes", role: "app", url: "/mail/thread/42" }],
      ["shortcut", { app: "notes", role: "app", key: "Ctrl+Backquote" }],
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
    expect(p.events).toEqual([["mismatch", { app: "notes", role: "app", contract: 2 }]]);
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
      ["shortcut", { app: "notes", role: "app", key: "Ctrl+Backquote" }],
    ]);
  });

  it("connect a companion frame beside an app's, and carry its context and ask", async () => {
    const p = setup(`${ORIGIN}/app/notes/1/Plan.md`);
    const host = createShell({
      window: p.shell,
      init: (_app, role) => ({
        ...fields,
        companion: "Ctrl+Shift+Backquote",
        contentOrigin: role === "app" ? CONTENT : null,
      }),
    });
    const side = pair(`${ORIGIN}/app/porter/`, p.shell);
    const port = host.attach("porter", side.frame, "companion");
    const companion = await connectLodge({ app: "porter", window: side.app });
    expect([companion.role, companion.companion, companion.contentOrigin]).toEqual([
      "companion",
      "Ctrl+Shift+Backquote",
      null,
    ]);
    const got: unknown[] = [];
    companion.on("context", (e) => got.push(["context", e]));
    companion.on("ask", (e) => got.push(["ask", e]));
    port.context({ app: "notes", kind: "note", ref: "notes/1/Plan.md", title: "Plan" });
    port.ask("notes", "What is due here?");
    await settle();
    expect(got).toEqual([
      ["context", { app: "notes", kind: "note", ref: "notes/1/Plan.md", title: "Plan" }],
      ["ask", { app: "notes", prompt: "What is due here?" }],
    ]);
    // The companion's routes stay in its frame.
    companion.history.push("/thread/2", "Thread");
    await settle();
    expect(companion.history.location.pathname).toBe("/thread/2");
    expect(side.sent.toShell.map((s) => (s.data as { type: string }).type)).toEqual(["ready"]);
    host.close();
  });
});

describe("the companion's frame", () => {
  function both() {
    const p = setup(`${ORIGIN}/app/porter/`);
    const side = pair(`${ORIGIN}/app/porter/`, p.shell);
    const sidePort = p.host.attach("porter", side.frame, "companion");
    const fromSide = (data: unknown) => side.parent.postMessage(data, ORIGIN);
    return { p, side, sidePort, fromSide };
  }

  it("has its own port beside its app's, and its init says its role", async () => {
    const { p, side, sidePort, fromSide } = both();
    expect(p.host.port("porter", "companion")).toBe(sidePort);
    expect(p.host.port("notes")).toBe(p.port);
    expect(sidePort.role).toBe("companion");
    fromSide({ lodge: 1, type: "ready", contract: 1 });
    await settle();
    expect(side.sent.toApp.map((s) => s.data)).toEqual([
      { lodge: 1, type: "init", contract: 1, ...fields, role: "companion" },
    ]);
    expect(p.events).toEqual([["ready", { app: "porter", role: "companion" }]]);
    expect(p.port.state).toBe("loading");
  });

  it("takes open and shortcut from it, and drops its route, context and ask", async () => {
    const { p, fromSide } = both();
    fromSide({ lodge: 1, type: "ready", contract: 1 });
    fromSide({ lodge: 1, type: "route", path: "/thread/1", title: "x" });
    fromSide({ lodge: 1, type: "context", kind: "thread", ref: "1", title: "x" });
    fromSide({ lodge: 1, type: "ask", prompt: "hello" });
    fromSide({ lodge: 1, type: "open", url: "/notes/1/A.md" });
    fromSide({ lodge: 1, type: "shortcut", key: "Ctrl+Shift+Backquote" });
    await settle();
    expect(p.events).toEqual([
      ["ready", { app: "porter", role: "companion" }],
      ["open", { app: "porter", role: "companion", url: "/notes/1/A.md" }],
      ["shortcut", { app: "porter", role: "companion", key: "Ctrl+Shift+Backquote" }],
    ]);
  });

  it("sends context and ask only to a ready frame, and only well-formed", async () => {
    const { side, sidePort, fromSide } = both();
    sidePort.context({ app: "notes", kind: null, ref: "", title: "" });
    sidePort.ask("notes", "early");
    expect(side.sent.toApp).toEqual([]);
    fromSide({ lodge: 1, type: "ready", contract: 1 });
    await settle();
    side.sent.toApp.length = 0;
    sidePort.ask("notes", "");
    sidePort.ask("notes", "x".repeat(4001));
    sidePort.ask("Notes", "x");
    sidePort.ask("notes", "x".repeat(4000));
    expect(side.sent.toApp.map((s) => s.data)).toEqual([
      { lodge: 1, type: "ask", app: "notes", prompt: "x".repeat(4000) },
    ]);
  });
});

describe("ask from an app's frame", () => {
  it("is reported with its app, when the prompt is 1 to 4,000 characters", async () => {
    const p = setup();
    app(p, { lodge: 1, type: "ready", contract: 1 });
    app(p, { lodge: 1, type: "ask", prompt: "What is due here?" });
    app(p, { lodge: 1, type: "ask", prompt: "" });
    app(p, { lodge: 1, type: "ask", prompt: "x".repeat(4001) });
    app(p, { lodge: 1, type: "ask", prompt: 3 });
    app(p, { lodge: 1, type: "ask", prompt: "😀".repeat(4000) });
    await settle();
    expect(p.events.filter((e) => e[0] === "ask")).toEqual([
      ["ask", { app: "notes", prompt: "What is due here?" }],
      ["ask", { app: "notes", prompt: "😀".repeat(4000) }],
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
