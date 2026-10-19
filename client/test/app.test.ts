import { describe, expect, it } from "vitest";
import { connectLodge, frameRoute, type Lodge, splitRoute } from "../src/index.js";
import { CONTENT, FakeAppWindow, key, ORIGIN, pair, settle } from "./harness.js";

const init = { lodge: 1, type: "init", contract: 1, contentOrigin: CONTENT, shortcut: "Ctrl+Backquote", theme: "dark" };

/** connected returns an app connected to a scripted shell, and what it posted. */
async function connected(url = `${ORIGIN}/app/notes/1/Plan.md?x=1#h`, answer: object = init) {
  const p = pair(url);
  const pending = connectLodge({ app: "notes", window: p.app });
  await settle();
  p.frameWindow.postMessage(answer, p.app.origin);
  const lodge = await pending;
  p.sent.toShell.length = 0;
  return { ...p, lodge };
}

async function sent(p: { sent: { toShell: { data: unknown }[] } }) {
  await settle();
  return p.sent.toShell.map((s) => s.data);
}

describe("connectLodge", () => {
  it("posts ready to the parent on its own origin, and resolves on init", async () => {
    const p = pair(`${ORIGIN}/app/notes/`);
    const pending = connectLodge({ app: "notes", window: p.app });
    expect(p.sent.toShell).toEqual([{ data: { lodge: 1, type: "ready", contract: 1 }, targetOrigin: ORIGIN }]);
    p.frameWindow.postMessage(init, ORIGIN);
    const lodge = await pending;
    expect([lodge.contract, lodge.theme, lodge.shortcut, lodge.contentOrigin]).toEqual([
      1,
      "dark",
      "Ctrl+Backquote",
      CONTENT,
    ]);
  });

  it("ignores an init from another origin, another source, or another major", async () => {
    const p = pair(`${ORIGIN}/app/notes/`);
    let done = false;
    void connectLodge({ app: "notes", window: p.app }).then(() => {
      done = true;
    });
    p.app.deliver(init, ORIGIN, "https://evil.test", p.parent); // another origin
    p.app.deliver(init, ORIGIN, ORIGIN, p.app); // another source
    p.frameWindow.postMessage({ ...init, lodge: 2 }, ORIGIN);
    p.frameWindow.postMessage({ ...init, theme: "blue" }, ORIGIN);
    p.frameWindow.postMessage({ lodge: 1, type: "navigate", route: "/x" }, ORIGIN);
    await settle();
    expect(done).toBe(false);
    p.frameWindow.postMessage(init, ORIGIN);
    await settle();
    expect(done).toBe(true);
  });

  it("outside a frame, replaces the page with the canonical URL", async () => {
    const top = new FakeAppWindow(`${ORIGIN}/app/notes/1/Plan.md?q=1#h`);
    let resolved = false;
    void connectLodge({ app: "notes", window: top }).then(() => {
      resolved = true;
    });
    await settle();
    expect(top.location.replaced).toEqual(["/notes/1/Plan.md?q=1#h"]);
    expect(resolved).toBe(false);
    const root = new FakeAppWindow(`${ORIGIN}/app/notes/`);
    void connectLodge({ app: "notes", window: root });
    expect(root.location.replaced).toEqual(["/notes"]);
  });

  it("refuses an id that is not an app's", () => {
    for (const app of ["", "Notes", "lodge", "mcp", "1x", "a234567890123456x"]) {
      expect(() => connectLodge({ app, window: pair(`${ORIGIN}/`).app })).toThrow(TypeError);
    }
  });
});

describe("Lodge", () => {
  it("builds the api, content and storage names", async () => {
    const { lodge } = await connected();
    expect(lodge.apiURL()).toBe("wss://lodge.test/app/notes/api");
    expect(lodge.contentURL("blobs/ab12.png")).toBe(`${CONTENT}/notes/blobs/ab12.png`);
    expect(() => lodge.contentURL("/blobs/x")).toThrow(TypeError);
    expect(lodge.storageKey("workspace")).toBe("notes:workspace");
    const dev = await connected("http://127.0.0.1:8080/app/notes/", { ...init, contentOrigin: null });
    expect(dev.lodge.apiURL()).toBe("ws://127.0.0.1:8080/app/notes/api");
    expect(() => dev.lodge.contentURL("x")).toThrow(/no content origin/);
  });

  it("posts route, context, open and shortcut", async () => {
    const p = await connected();
    p.lodge.route("/1/Projects/Plan.md", "Plan");
    p.lodge.route("/1/Projects/Plan.md#top", "Plan", { replace: true });
    p.lodge.context({ kind: "note", ref: "notes/1/Projects/Plan.md", title: "Plan" });
    p.lodge.open("/mail/thread/42");
    p.lodge.openSwitcher();
    expect(await sent(p)).toEqual([
      { lodge: 1, type: "route", path: "/1/Projects/Plan.md", title: "Plan" },
      { lodge: 1, type: "route", path: "/1/Projects/Plan.md#top", title: "Plan", replace: true },
      { lodge: 1, type: "context", kind: "note", ref: "notes/1/Projects/Plan.md", title: "Plan" },
      { lodge: 1, type: "open", url: "/mail/thread/42" },
      { lodge: 1, type: "shortcut", key: "Ctrl+Backquote" },
    ]);
    expect(() => p.lodge.route("relative")).toThrow(TypeError);
  });

  it("follows navigate, visible and theme from the shell only", async () => {
    const p = await connected();
    const got: unknown[] = [];
    p.lodge.on("navigate", (e) => got.push(["navigate", e.route]));
    p.lodge.on("visible", (e) => got.push(["visible", e.visible]));
    const off = p.lodge.on("theme", (e) => got.push(["theme", e.scheme]));
    p.frameWindow.postMessage({ lodge: 1, type: "visible", visible: true }, ORIGIN);
    p.frameWindow.postMessage({ lodge: 1, type: "navigate", route: "/2/Inbox.md" }, ORIGIN);
    p.frameWindow.postMessage({ lodge: 1, type: "theme", scheme: "light" }, ORIGIN);
    p.app.deliver({ lodge: 1, type: "navigate", route: "/forged" }, ORIGIN, "https://evil.test", p.parent);
    p.frameWindow.postMessage({ lodge: 1, type: "navigate", route: "no-slash" }, ORIGIN);
    p.frameWindow.postMessage({ lodge: 1, type: "unknown" }, ORIGIN);
    await settle();
    off();
    p.frameWindow.postMessage({ lodge: 1, type: "theme", scheme: "dark" }, ORIGIN);
    await settle();
    expect(got).toEqual([
      ["visible", true],
      ["navigate", "/2/Inbox.md"],
      ["theme", "light"],
    ]);
    expect([p.lodge.visible, p.lodge.theme]).toEqual([true, "dark"]);
  });

  it("defaults role and companion for a shell that sends neither", async () => {
    const p = await connected();
    expect([p.lodge.role, p.lodge.companion]).toEqual(["app", null]);
  });

  it("posts ask, and refuses a prompt that is empty or over 4,000 characters", async () => {
    const p = await connected(undefined, { ...init, role: "app", companion: "Ctrl+Shift+Backquote" });
    p.lodge.ask("What is due here?");
    expect(await sent(p)).toEqual([{ lodge: 1, type: "ask", prompt: "What is due here?" }]);
    expect(() => p.lodge.ask("")).toThrow(TypeError);
    expect(() => p.lodge.ask("x".repeat(4001))).toThrow(TypeError);
  });

  it("keeps a companion frame's history in memory, and hears context and ask", async () => {
    const p = await connected(`${ORIGIN}/app/notes/`, {
      ...init,
      role: "companion",
      companion: "Ctrl+Shift+Backquote",
    });
    expect(p.lodge.role).toBe("companion");
    const updates: string[] = [];
    p.lodge.history.listen((u) => updates.push(`${u.action} ${u.location.pathname}`));
    p.lodge.history.push("/thread/1", "One");
    p.lodge.history.replace("/thread/2", "Two");
    p.lodge.route("/thread/3");
    expect(updates).toEqual(["push /thread/1", "replace /thread/2"]);
    expect(p.lodge.history.location.pathname).toBe("/thread/3");
    const got: unknown[] = [];
    p.lodge.on("context", (e) => got.push(e));
    p.lodge.on("ask", (e) => got.push(e));
    p.frameWindow.postMessage(
      { lodge: 1, type: "context", app: "mail", kind: "thread", ref: "t1", title: "Re: Saturday" },
      ORIGIN,
    );
    p.frameWindow.postMessage({ lodge: 1, type: "ask", app: "mail", prompt: "Draft a yes" }, ORIGIN);
    p.frameWindow.postMessage({ lodge: 1, type: "ask", app: "mail", prompt: "" }, ORIGIN);
    expect(await sent(p)).toEqual([]);
    expect(got).toEqual([
      { app: "mail", kind: "thread", ref: "t1", title: "Re: Saturday" },
      { app: "mail", prompt: "Draft a yes" },
    ]);
  });

  it("keeps a history driven by route and navigate", async () => {
    const p = await connected();
    const { history } = p.lodge;
    expect(history.location).toEqual({ pathname: "/1/Plan.md", search: "?x=1", hash: "#h" });
    const updates: string[] = [];
    history.listen((u) => updates.push(`${u.action} ${u.location.pathname}${u.location.search}`));
    history.push("/1/A.md?q", "A");
    history.replace("/1/B.md", "B");
    p.frameWindow.postMessage({ lodge: 1, type: "navigate", route: "/1/C.md#x" }, ORIGIN);
    await settle();
    expect(updates).toEqual(["push /1/A.md?q", "replace /1/B.md", "navigate /1/C.md"]);
    expect(history.location.hash).toBe("#x");
    expect((await sent(p)).map((m) => (m as { replace?: boolean }).replace ?? false)).toEqual([false, true]);
  });
});

describe("the shortcut", () => {
  async function press(lodge: Lodge, win: EventTarget, ev: Event) {
    let seen = false;
    win.addEventListener("keydown", () => {
      seen = true;
    });
    win.dispatchEvent(ev);
    void lodge;
    return { seen, prevented: ev.defaultPrevented };
  }

  it("is caught before the app sees it, and posts shortcut", async () => {
    const p = await connected();
    expect(await press(p.lodge, p.app, key("Backquote", { ctrlKey: true }))).toEqual({ seen: false, prevented: true });
    expect(await sent(p)).toEqual([{ lodge: 1, type: "shortcut", key: "Ctrl+Backquote" }]);
  });

  it("passes every other key, and a composition", async () => {
    const p = await connected();
    for (const ev of [
      key("Backquote"),
      key("Backquote", { ctrlKey: true, shiftKey: true }),
      key("Backquote", { ctrlKey: true, isComposing: true }),
      key("KeyN", { ctrlKey: true }),
    ]) {
      expect(await press(p.lodge, p.app, ev)).toEqual({ seen: true, prevented: false });
    }
    expect(await sent(p)).toEqual([]);
  });

  it("catches the companion's chord too, and posts it as the shortcut's key", async () => {
    const p = await connected(undefined, { ...init, companion: "Ctrl+Shift+Backquote" });
    expect(await press(p.lodge, p.app, key("Backquote", { ctrlKey: true, shiftKey: true }))).toEqual({
      seen: false,
      prevented: true,
    });
    expect(await press(p.lodge, p.app, key("Backquote", { ctrlKey: true }))).toEqual({ seen: false, prevented: true });
    expect(await sent(p)).toEqual([
      { lodge: 1, type: "shortcut", key: "Ctrl+Shift+Backquote" },
      { lodge: 1, type: "shortcut", key: "Ctrl+Backquote" },
    ]);
  });

  it("follows the chord init names, and stops on close", async () => {
    const p = await connected(undefined, { ...init, shortcut: "Alt+Shift+KeyK" });
    expect((await press(p.lodge, p.app, key("KeyK", { altKey: true, shiftKey: true }))).prevented).toBe(true);
    p.lodge.close();
    expect((await press(p.lodge, p.app, key("KeyK", { altKey: true, shiftKey: true }))).prevented).toBe(false);
  });
});

describe("routes", () => {
  it("splits a frame URL into the app's route", () => {
    const loc = (pathname: string, search = "", hash = "") => ({ pathname, search, hash });
    expect(frameRoute("notes", loc("/app/notes"))).toBe("/");
    expect(frameRoute("notes", loc("/app/notes/"))).toBe("/");
    expect(frameRoute("notes", loc("/app/notes/1/A.md", "?q", "#h"))).toBe("/1/A.md?q#h");
    expect(frameRoute("notes", loc("/app/notesx/1"))).toBe("/app/notesx/1");
    expect(splitRoute("/a/b?c=d#e?f")).toEqual({ pathname: "/a/b", search: "?c=d", hash: "#e?f" });
    expect(splitRoute("/")).toEqual({ pathname: "/", search: "", hash: "" });
  });
});
