import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { LodgeShell } from "../src/lodge.js";
import type { Rail } from "../src/model.js";
import { Settings, type Store } from "../src/storage.js";

const CONTENT = "https://lodge-content.test";

const rail = (): Rail => ({
  contract: 1,
  contentOrigin: CONTENT,
  apps: [
    {
      id: "mail",
      name: "Mail",
      icon: "/app/mail/icon.svg",
      key: "m",
      content: true,
      companion: false,
      state: "up",
      reason: "",
    },
    {
      id: "notes",
      name: "Notes",
      icon: "/app/notes/icon.svg",
      key: "n",
      content: true,
      companion: false,
      state: "up",
      reason: "",
    },
    {
      id: "off",
      name: "<b>Off</b>",
      icon: "",
      key: "",
      content: false,
      companion: false,
      state: "disabled",
      reason: "contract 9 is not one this lodge speaks",
    },
    {
      id: "plain",
      name: "Plain",
      icon: "/app/plain/icon.svg",
      key: "",
      content: false,
      companion: false,
      state: "up",
      reason: "",
    },
  ],
});

/** A rail whose Porter is the companion. */
const withCompanion = (): Rail => {
  const r = rail();
  r.apps.push({
    id: "porter",
    name: "Porter",
    icon: "/app/porter/icon.svg",
    key: "p",
    content: false,
    companion: true,
    state: "up",
    reason: "",
  });
  return r;
};

function memory(init: Record<string, string> = {}): Store & { data: Record<string, string> } {
  const data = { ...init };
  return {
    data,
    getItem: (k) => data[k] ?? null,
    setItem: (k, v) => {
      data[k] = v;
    },
    removeItem: (k) => {
      delete data[k];
    },
  };
}

type Sent = unknown[];

interface Setup {
  shell: LodgeShell;
  fetchRail: ReturnType<typeof vi.fn>;
  store: ReturnType<typeof memory>;
  sent: Map<string, Sent>;
  frame(app: string): HTMLIFrameElement;
  view(app: string): HTMLElement;
  /** fromApp posts a message as app's frame would. */
  fromApp(app: string, data: unknown): void;
  ready(app: string): void;
  /** panel is the companion's frame, if created; sent.get("companion") is what it was sent. */
  panel(): HTMLIFrameElement | null;
  /** fromPanel posts a message as the companion's frame would. */
  fromPanel(data: unknown): void;
}

const fakes = new Map<string, { postMessage(m: unknown, o: string): void }>();
// What each test started, stopped after it.
const cleanups: (() => void)[] = [];

async function setup(
  path = "/",
  opts: {
    rail?: () => Rail | Promise<Rail>;
    store?: Record<string, string>;
    now?: () => number;
    /** Whether the window is under 600 pixels wide. */
    narrow?: boolean;
  } = {},
): Promise<Setup> {
  history.replaceState(null, "", path);
  document.body.replaceChildren();
  const store = memory(opts.store);
  const sent = new Map<string, Sent>();
  const fetchRail = vi.fn(async () => (opts.rail ?? rail)());
  if (opts.narrow !== undefined) {
    const real = window.matchMedia;
    const narrow = opts.narrow;
    window.matchMedia = ((q: string) => ({
      matches: q.includes("max-width") ? narrow : false,
      media: q,
      addEventListener() {},
      removeEventListener() {},
    })) as unknown as typeof window.matchMedia;
    cleanups.push(() => {
      window.matchMedia = real;
    });
  }
  const shell = new LodgeShell({
    window,
    settings: new Settings(store),
    fetchRail,
    ...(opts.now ? { now: opts.now } : {}),
  });
  document.body.append(shell.root);
  // Each frame gets a fake window as soon as it is created.
  const observer = new MutationObserver(() => {
    for (const f of document.querySelectorAll<HTMLIFrameElement>("iframe")) wire(f);
  });
  observer.observe(document.body, { childList: true, subtree: true });
  cleanups.push(
    () => observer.disconnect(),
    () => shell.stop(),
  );
  const wire = (f: HTMLIFrameElement) => {
    const app = f.closest<HTMLElement>(".view")?.dataset.app ?? (f.closest(".companion") ? "companion" : "");
    if (fakes.has(app) && Object.hasOwn(f, "contentWindow")) return;
    const fake = {
      postMessage(m: unknown, origin: string) {
        expect(origin).toBe(location.origin);
        const list = sent.get(app) ?? [];
        list.push(structuredClone(m));
        sent.set(app, list);
      },
      focus() {},
    };
    fakes.set(app, fake);
    Object.defineProperty(f, "contentWindow", { value: fake, configurable: true });
  };
  await shell.start();
  for (const f of document.querySelectorAll<HTMLIFrameElement>("iframe")) wire(f);
  const s: Setup = {
    shell,
    fetchRail,
    store,
    sent,
    frame: (app) => {
      const f = document.querySelector<HTMLIFrameElement>(`.view[data-app="${app}"] iframe`);
      if (!f) throw new Error(`no frame for ${app}`);
      wire(f);
      return f;
    },
    view: (app) => {
      const v = document.querySelector<HTMLElement>(`.view[data-app="${app}"]`);
      if (!v) throw new Error(`no view for ${app}`);
      return v;
    },
    fromApp(app, data) {
      s.frame(app);
      window.dispatchEvent(
        Object.assign(new Event("message"), { data, origin: location.origin, source: fakes.get(app) }),
      );
    },
    ready(app) {
      s.fromApp(app, { lodge: 1, type: "ready", contract: 1 });
    },
    panel: () => {
      const f = document.querySelector<HTMLIFrameElement>(".companion iframe");
      if (f) wire(f);
      return f;
    },
    fromPanel(data) {
      if (!s.panel()) throw new Error("no companion frame");
      window.dispatchEvent(
        Object.assign(new Event("message"), { data, origin: location.origin, source: fakes.get("companion") }),
      );
    },
  };
  return s;
}

const key = (code: string, k: string, mods: Partial<KeyboardEventInit> = {}) =>
  new KeyboardEvent("keydown", { code, key: k, bubbles: true, cancelable: true, ...mods });

const filter = () => document.querySelector<HTMLInputElement>(".switcher-filter") as HTMLInputElement;

beforeEach(() => {
  fakes.clear();
});

afterEach(() => {
  for (const f of cleanups.splice(0)) f();
  vi.useRealTimers();
});

describe("the rail", () => {
  it("shows the apps by name, with their states, as text", async () => {
    const s = await setup("/");
    const buttons = [...document.querySelectorAll<HTMLButtonElement>(".rail .app-button")];
    expect(buttons.map((b) => b.dataset.app)).toEqual(["mail", "notes", "off", "plain"]);
    const off = buttons[2] as HTMLButtonElement;
    expect(off.querySelector(".mark")?.getAttribute("data-state")).toBe("disabled");
    expect(off.getAttribute("aria-label")).toBe("<b>Off</b>");
    expect(off.querySelector("b")).toBeNull();
    expect(off.querySelector("img")).toBeNull();
    expect(buttons[0]?.querySelector("img")?.getAttribute("src")).toBe("/app/mail/icon.svg");
    s.shell.stop();
  });

  it("says when lodge is out of reach, and keeps the frames", async () => {
    let fail = false;
    const s = await setup("/notes", {
      rail: () => {
        if (fail) throw new Error("offline");
        return rail();
      },
    });
    fail = true;
    await s.shell.poll();
    expect(s.shell.reachable).toBe(false);
    expect(document.querySelector(".lodge-state")?.hasAttribute("data-unreachable")).toBe(true);
    expect(s.frame("notes")).toBeTruthy();
    fail = false;
    await s.shell.poll();
    expect(document.querySelector(".lodge-state")?.hasAttribute("data-unreachable")).toBe(false);
  });
});

describe("addresses and history", () => {
  it("opens the address bar's app at its route", async () => {
    const s = await setup("/notes/1/Projects/Plan.md?q=1#h");
    expect(s.shell.current).toBe("notes");
    expect(s.frame("notes").getAttribute("src")).toBe("/app/notes/1/Projects/Plan.md?q=1#h");
    expect(s.frame("notes").getAttribute("referrerpolicy")).toBe("no-referrer");
    expect(s.frame("notes").hasAttribute("sandbox")).toBe(false);
    expect(s.frame("notes").title).toBe("Notes");
    expect(history.state).toEqual({ lodge: 1, app: "notes", route: "/1/Projects/Plan.md?q=1#h" });
  });

  it("opens the last app on /, or the first enabled one", async () => {
    let s = await setup("/", { store: { "lodge:last": "plain" } });
    expect(s.shell.current).toBe("plain");
    expect(location.pathname).toBe("/plain");
    s.shell.stop();
    s = await setup("/");
    expect(s.shell.current).toBe("mail");
    expect(s.store.data["lodge:last"]).toBe("mail");
  });

  it("follows route messages, pushing or replacing", async () => {
    const s = await setup("/notes");
    s.ready("notes");
    const before = history.length;
    s.fromApp("notes", { lodge: 1, type: "route", path: "/1/Plan.md", title: "Plan" });
    await vi.waitFor(() => expect(location.pathname).toBe("/notes/1/Plan.md"));
    expect(history.length).toBe(before + 1);
    expect(document.title).toBe("Plan — Notes");
    s.fromApp("notes", { lodge: 1, type: "route", path: "/1/Other.md", title: "Other", replace: true });
    await vi.waitFor(() => expect(location.pathname).toBe("/notes/1/Other.md"));
    expect(history.length).toBe(before + 1);
    // The same route again changes nothing.
    s.fromApp("notes", { lodge: 1, type: "route", path: "/1/Other.md", title: "Other" });
    expect(history.length).toBe(before + 1);
  });

  it("shows the entry's app on Back and Forward, and navigates it", async () => {
    const s = await setup("/notes");
    s.ready("notes");
    s.shell.switchTo("mail");
    s.ready("mail");
    window.dispatchEvent(new PopStateEvent("popstate", { state: { lodge: 1, app: "notes", route: "/1/Back.md" } }));
    expect(s.shell.current).toBe("notes");
    expect(s.sent.get("notes")).toContainEqual({ lodge: 1, type: "navigate", route: "/1/Back.md" });
    expect(s.view("mail").hidden).toBe(true);
  });
});

describe("frames", () => {
  it("answers ready with init, with the content origin only for content apps", async () => {
    const s = await setup("/notes", { store: { "lodge:theme": "dark" } });
    s.ready("notes");
    expect(s.sent.get("notes")?.[0]).toEqual({
      lodge: 1,
      type: "init",
      contract: 1,
      contentOrigin: CONTENT,
      shortcut: "Ctrl+Backquote",
      theme: "dark",
      role: "app",
      companion: null,
    });
    s.shell.switchTo("plain");
    s.ready("plain");
    expect(s.sent.get("plain")?.[0]).toMatchObject({ type: "init", contentOrigin: null });
  });

  it("keeps frames loaded, hidden and inert, and tells them", async () => {
    const s = await setup("/notes");
    s.ready("notes");
    const notes = s.frame("notes");
    s.shell.switchTo("mail");
    s.ready("mail");
    expect(s.view("notes").hidden).toBe(true);
    expect(s.view("notes").inert).toBe(true);
    expect(s.view("mail").hidden).toBe(false);
    expect(s.sent.get("notes")).toContainEqual({ lodge: 1, type: "visible", visible: false });
    expect(s.sent.get("mail")).toContainEqual({ lodge: 1, type: "visible", visible: true });
    expect(location.pathname).toBe("/mail");
    s.shell.switchTo("notes");
    expect(s.frame("notes")).toBe(notes);
    expect(document.querySelectorAll(".view[data-app=notes] iframe").length).toBe(1);
  });

  it("shows a disabled app's reason instead of a frame", async () => {
    const s = await setup("/off");
    expect(document.querySelector(".view[data-app=off] iframe")).toBeNull();
    expect(s.view("off").querySelector(".notice")?.textContent).toContain("disabled: contract 9");
    expect(s.view("off").querySelector("b")).toBeNull();
  });

  it("says when a frame does not respond, and reloads it", async () => {
    vi.useFakeTimers();
    const s = await setup("/notes");
    s.frame("notes").dispatchEvent(new Event("load"));
    vi.advanceTimersByTime(9_999);
    expect(s.view("notes").querySelector(".notice")?.hasAttribute("hidden")).toBe(true);
    vi.advanceTimersByTime(1);
    const notice = s.view("notes").querySelector(".notice") as HTMLElement;
    expect(notice.textContent).toContain("Notes is not responding");
    const src = s.frame("notes").getAttribute("src");
    s.frame("notes").setAttribute("src", "about:blank");
    (notice.querySelector(".reload") as HTMLButtonElement).click();
    expect(s.frame("notes").getAttribute("src")).toBe(src);
    expect(notice.hidden).toBe(true);
  });

  it("sends a frame that loads again back to its last route", async () => {
    const s = await setup("/notes");
    s.ready("notes");
    s.fromApp("notes", { lodge: 1, type: "route", path: "/1/Plan.md", title: "Plan" });
    s.frame("notes").dispatchEvent(new Event("load"));
    s.ready("notes");
    expect(s.sent.get("notes")).toContainEqual({ lodge: 1, type: "navigate", route: "/1/Plan.md" });
  });

  it("says when a frame speaks another contract", async () => {
    const s = await setup("/notes");
    s.fromApp("notes", { lodge: 2, type: "ready", contract: 2 });
    await vi.waitFor(() => expect(s.view("notes").querySelector(".notice")?.textContent).toContain("another version"));
    expect(s.sent.get("notes") ?? []).toEqual([]);
  });

  it("keeps a down app's frame under a notice, and offers a reload when it is back", async () => {
    let state: "up" | "down" = "up";
    const s = await setup("/notes", {
      rail: () => {
        const r = rail();
        const n = r.apps[1];
        if (n) Object.assign(n, { state, reason: state === "down" ? "its socket does not answer" : "" });
        return r;
      },
    });
    state = "down";
    await s.shell.poll();
    const notice = s.view("notes").querySelector(".notice") as HTMLElement;
    expect(notice.textContent).toContain("Notes is down: its socket does not answer");
    expect(notice.hasAttribute("data-covering")).toBe(false);
    expect(s.frame("notes")).toBeTruthy();
    state = "up";
    await s.shell.poll();
    expect(notice.textContent).toContain("back");
    expect(notice.querySelector(".reload")).not.toBeNull();
  });
});

describe("the switcher", () => {
  it("opens on Ctrl+`, and an app's key opens that app", async () => {
    const s = await setup("/notes");
    window.dispatchEvent(key("Backquote", "`", { ctrlKey: true }));
    expect(s.shell.switcherOpen).toBe(true);
    expect(document.activeElement).toBe(filter());
    const items = [...document.querySelectorAll(".switcher-item")];
    expect(items.map((li) => li.getAttribute("data-app"))).toEqual(["mail", "notes", "off", "plain"]);
    expect(items[1]?.hasAttribute("data-current")).toBe(true);
    filter().dispatchEvent(key("KeyM", "m"));
    expect(s.shell.switcherOpen).toBe(false);
    expect(s.shell.current).toBe("mail");
  });

  it("opens on the frame's shortcut message, and again opens the previous app", async () => {
    const s = await setup("/notes");
    s.ready("notes");
    s.shell.switchTo("mail");
    s.ready("mail");
    s.fromApp("mail", { lodge: 1, type: "shortcut", key: "Ctrl+Backquote" });
    await vi.waitFor(() => expect(s.shell.switcherOpen).toBe(true));
    filter().dispatchEvent(key("Backquote", "`", { ctrlKey: true }));
    expect(s.shell.switcherOpen).toBe(false);
    expect(s.shell.current).toBe("notes");
  });

  it("filters, moves and opens with the keyboard, and closes on Escape", async () => {
    const s = await setup("/notes");
    s.shell.openSwitcher();
    filter().value = "pl";
    filter().dispatchEvent(new Event("input"));
    expect([...document.querySelectorAll(".switcher-item")].map((li) => li.getAttribute("data-app"))).toEqual([
      "plain",
    ]);
    filter().value = "";
    filter().dispatchEvent(new Event("input"));
    filter().dispatchEvent(key("ArrowDown", "ArrowDown"));
    expect(document.querySelector('.switcher-item[aria-selected="true"]')?.getAttribute("data-app")).toBe("off");
    filter().dispatchEvent(key("ArrowUp", "ArrowUp"));
    filter().dispatchEvent(key("ArrowUp", "ArrowUp"));
    filter().dispatchEvent(key("Enter", "Enter"));
    expect(s.shell.current).toBe("mail");
    s.shell.openSwitcher();
    filter().dispatchEvent(key("Escape", "Escape"));
    expect(s.shell.switcherOpen).toBe(false);
    expect(s.shell.current).toBe("mail");
  });

  it("uses a stored shortcut, but never a ruled-out one", async () => {
    let s = await setup("/notes", { store: { "lodge:shortcut": "Ctrl+Alt+KeyJ" } });
    expect(s.shell.shortcut).toBe("Ctrl+Alt+KeyJ");
    window.dispatchEvent(key("Backquote", "`", { ctrlKey: true }));
    expect(s.shell.switcherOpen).toBe(false);
    window.dispatchEvent(key("KeyJ", "j", { ctrlKey: true, altKey: true }));
    expect(s.shell.switcherOpen).toBe(true);
    s.shell.stop();
    s = await setup("/notes", { store: { "lodge:shortcut": "Ctrl+Space" } });
    expect(s.shell.shortcut).toBe("Ctrl+Backquote");
  });
});

describe("open", () => {
  it("opens lodge URLs in the shell, and others in a new tab", async () => {
    const s = await setup("/notes");
    s.ready("notes");
    const open = vi.spyOn(window, "open").mockImplementation(() => null);
    s.fromApp("notes", { lodge: 1, type: "open", url: "/mail/thread/42" });
    await vi.waitFor(() => expect(s.shell.current).toBe("mail"));
    expect(s.frame("mail").getAttribute("src")).toBe("/app/mail/thread/42");
    expect(location.pathname).toBe("/mail/thread/42");
    s.ready("mail");
    s.fromApp("mail", { lodge: 1, type: "open", url: `${CONTENT}/mail/m/1.pdf` });
    s.fromApp("mail", { lodge: 1, type: "open", url: "https://example.com/" });
    s.fromApp("mail", { lodge: 1, type: "open", url: "/app/notes/" });
    s.fromApp("mail", { lodge: 1, type: "open", url: "javascript:alert(1)" });
    await vi.waitFor(() => expect(open).toHaveBeenCalledTimes(2));
    expect(open).toHaveBeenNthCalledWith(1, `${CONTENT}/mail/m/1.pdf`, "_blank", "noopener,noreferrer");
    expect(open).toHaveBeenNthCalledWith(2, "https://example.com/", "_blank", "noopener,noreferrer");
    expect(s.shell.current).toBe("mail");
  });
});

describe("the companion", () => {
  const companion = () => document.querySelector<HTMLElement>(".companion") as HTMLElement;
  const railButton = () => document.querySelector<HTMLButtonElement>(".rail .companion-button") as HTMLButtonElement;
  const shown = () => !companion().hidden && document.querySelector(".lodge")?.hasAttribute("data-companion") === true;
  const chord = () => key("Backquote", "~", { ctrlKey: true, shiftKey: true });

  it("has a button on the rail and the bar only when an app is the companion", async () => {
    let s = await setup("/notes");
    expect([...document.querySelectorAll<HTMLButtonElement>(".companion-button")].map((b) => b.hidden)).toEqual([
      true,
      true,
    ]);
    window.dispatchEvent(chord());
    expect(s.shell.companionShown).toBe(false);
    s.shell.stop();
    s = await setup("/notes", { rail: withCompanion });
    const buttons = [...document.querySelectorAll<HTMLButtonElement>(".companion-button")];
    expect(buttons.map((b) => b.hidden)).toEqual([false, false]);
    expect(railButton().getAttribute("aria-label")).toBe("Porter (Ctrl+Shift+`)");
    expect(railButton().getAttribute("aria-pressed")).toBe("false");
    expect(document.querySelector(".bar .bar-end .companion-button")).toBe(buttons[1]);
    expect(companion().hidden).toBe(true);
    expect(s.panel()).toBeNull();
  });

  it("opens beside the current app from its button and its chord, in a frame of its own, and remembers it", async () => {
    const s = await setup("/notes", { rail: withCompanion });
    s.ready("notes");
    railButton().click();
    expect(shown()).toBe(true);
    expect(railButton().getAttribute("aria-pressed")).toBe("true");
    const panel = s.panel() as HTMLIFrameElement;
    expect(panel.getAttribute("src")).toBe("/app/porter/");
    expect(panel.getAttribute("title")).toBe("Porter (companion)");
    expect(document.querySelector(".view[data-app=porter]")).toBeNull();
    expect(s.store.data["lodge:companion"]).toBe("open");
    expect(s.shell.current).toBe("notes");
    expect(location.pathname).toBe("/notes");
    window.dispatchEvent(chord());
    expect(shown()).toBe(false);
    expect(s.store.data["lodge:companion"]).toBeUndefined();
    // The frame stays loaded, hidden and inert.
    expect(s.panel()).toBe(panel);
    expect(companion().inert).toBe(true);
    // A frame's lodge-client posts the chord pressed inside it.
    s.fromApp("notes", { lodge: 1, type: "shortcut", key: "Ctrl+Shift+Backquote" });
    await vi.waitFor(() => expect(shown()).toBe(true));
    s.fromApp("notes", { lodge: 1, type: "shortcut", key: "Ctrl+Backquote" });
    await vi.waitFor(() => expect(s.shell.switcherOpen).toBe(true));
    expect(shown()).toBe(true);
    filter().dispatchEvent(chord());
    expect(s.shell.switcherOpen).toBe(false);
    expect(shown()).toBe(false);
  });

  it("says its role in init, and is told it is visible", async () => {
    const s = await setup("/notes", { rail: withCompanion });
    s.shell.toggleCompanion();
    s.fromPanel({ lodge: 1, type: "ready", contract: 1 });
    await vi.waitFor(() => expect(s.sent.get("companion")?.length).toBeGreaterThan(0));
    expect(s.sent.get("companion")?.[0]).toEqual({
      lodge: 1,
      type: "init",
      contract: 1,
      contentOrigin: null,
      shortcut: "Ctrl+Backquote",
      theme: "light",
      role: "companion",
      companion: "Ctrl+Shift+Backquote",
    });
    expect(s.sent.get("companion")).toContainEqual({ lodge: 1, type: "visible", visible: true });
    s.ready("notes");
    expect(s.sent.get("notes")?.[0]).toMatchObject({ role: "app", companion: "Ctrl+Shift+Backquote" });
    s.shell.toggleCompanion();
    expect(s.sent.get("companion")?.at(-1)).toEqual({ lodge: 1, type: "visible", visible: false });
  });

  it("is told the current app's latest context, and none for an app that reported none", async () => {
    const s = await setup("/notes", { rail: withCompanion });
    s.ready("notes");
    s.fromApp("notes", { lodge: 1, type: "context", kind: "note", ref: "notes/1/Plan.md", title: "Plan" });
    s.shell.toggleCompanion();
    s.fromPanel({ lodge: 1, type: "ready", contract: 1 });
    const contexts = () => (s.sent.get("companion") ?? []).filter((m) => (m as { type: string }).type === "context");
    await vi.waitFor(() =>
      expect(contexts()).toEqual([
        { lodge: 1, type: "context", app: "notes", kind: "note", ref: "notes/1/Plan.md", title: "Plan" },
      ]),
    );
    s.fromApp("notes", { lodge: 1, type: "context", kind: "note", ref: "notes/1/Other.md", title: "Other" });
    await vi.waitFor(() => expect(contexts()).toHaveLength(2));
    s.shell.switchTo("mail");
    expect(contexts().at(-1)).toEqual({ lodge: 1, type: "context", app: "mail", kind: null, ref: "", title: "" });
    // Only the current app's context is forwarded.
    s.fromApp("notes", { lodge: 1, type: "context", kind: "note", ref: "notes/1/Third.md", title: "Third" });
    await new Promise((r) => setTimeout(r, 0));
    expect(contexts()).toHaveLength(3);
    s.shell.switchTo("notes");
    expect(contexts().at(-1)).toMatchObject({ app: "notes", ref: "notes/1/Third.md" });
    // Nothing goes while it is closed; opening sends the latest.
    s.shell.toggleCompanion();
    s.shell.switchTo("mail");
    expect(contexts()).toHaveLength(4);
    s.shell.toggleCompanion();
    expect(contexts()).toHaveLength(5);
  });

  it("opens on the current app's ask, once a second, with the context first", async () => {
    let now = 10_000;
    const s = await setup("/notes", { rail: withCompanion, now: () => now });
    s.ready("notes");
    s.fromApp("notes", { lodge: 1, type: "context", kind: "note", ref: "notes/1/Plan.md", title: "Plan" });
    s.fromApp("notes", { lodge: 1, type: "ask", prompt: "What is due here?" });
    await vi.waitFor(() => expect(shown()).toBe(true));
    expect(s.store.data["lodge:companion"]).toBe("open");
    // The frame is new: the ask waits for its ready.
    s.fromPanel({ lodge: 1, type: "ready", contract: 1 });
    const fromShell = () =>
      (s.sent.get("companion") ?? []).filter((m) => ["context", "ask"].includes((m as { type: string }).type));
    await vi.waitFor(() =>
      expect(fromShell()).toEqual([
        { lodge: 1, type: "context", app: "notes", kind: "note", ref: "notes/1/Plan.md", title: "Plan" },
        { lodge: 1, type: "ask", app: "notes", prompt: "What is due here?" },
      ]),
    );
    now += 999;
    s.fromApp("notes", { lodge: 1, type: "ask", prompt: "Too soon" });
    s.shell.switchTo("mail");
    s.ready("mail");
    now += 1;
    s.fromApp("notes", { lodge: 1, type: "ask", prompt: "From a hidden frame" });
    s.fromPanel({ lodge: 1, type: "ask", prompt: "From the companion" });
    s.fromApp("mail", { lodge: 1, type: "ask", prompt: "Draft a yes" });
    await vi.waitFor(() =>
      expect(fromShell().at(-1)).toEqual({ lodge: 1, type: "ask", app: "mail", prompt: "Draft a yes" }),
    );
    expect(
      fromShell()
        .map((m) => (m as { prompt?: string }).prompt)
        .filter(Boolean),
    ).toEqual(["What is due here?", "Draft a yes"]);
  });

  it("ignores the companion frame's route, context and ask, and follows its open", async () => {
    const s = await setup("/notes", { rail: withCompanion });
    s.ready("notes");
    s.shell.toggleCompanion();
    s.fromPanel({ lodge: 1, type: "ready", contract: 1 });
    const before = history.length;
    s.fromPanel({ lodge: 1, type: "route", path: "/thread/1", title: "Thread" });
    s.fromPanel({ lodge: 1, type: "context", kind: "thread", ref: "1", title: "Thread" });
    s.fromPanel({ lodge: 1, type: "open", url: "/mail/thread/42" });
    await vi.waitFor(() => expect(s.shell.current).toBe("mail"));
    expect(location.pathname).toBe("/mail/thread/42");
    expect(history.length).toBe(before + 1);
    expect(document.title).not.toContain("Thread");
    expect(shown()).toBe(true);
  });

  it("hides while the companion app is current, and then its toggle shows the app before it beside it", async () => {
    const s = await setup("/notes", { rail: withCompanion });
    s.shell.toggleCompanion();
    const panel = s.panel();
    s.shell.switchTo("porter");
    expect(s.shell.current).toBe("porter");
    expect(shown()).toBe(false);
    expect(s.frame("porter")).not.toBe(panel);
    expect(railButton().getAttribute("aria-pressed")).toBe("false");
    // It is still open: it returns beside the next app.
    s.shell.switchTo("mail");
    expect(shown()).toBe(true);
    s.shell.switchTo("porter");
    railButton().click();
    expect(s.shell.current).toBe("mail");
    expect(shown()).toBe(true);
    expect(s.panel()).toBe(panel);
  });

  it("opens again on load from 600 pixels", async () => {
    const s = await setup("/notes", { rail: withCompanion, store: { "lodge:companion": "open" }, narrow: false });
    expect(shown()).toBe(true);
    expect(s.panel()?.getAttribute("src")).toBe("/app/porter/");
  });

  it("is a sheet under 600 pixels that starts closed, covers the app, and closes from its handle", async () => {
    const s = await setup("/notes", { rail: withCompanion, store: { "lodge:companion": "open" }, narrow: true });
    expect(shown()).toBe(false);
    const views = document.querySelector<HTMLElement>(".views") as HTMLElement;
    const bar = document.querySelector<HTMLElement>(".bar") as HTMLElement;
    const handle = document.querySelector<HTMLButtonElement>(".companion-handle") as HTMLButtonElement;
    s.shell.toggleCompanion();
    expect(shown()).toBe(true);
    expect([views.inert, bar.inert]).toEqual([true, true]);
    expect(s.frame("notes")).toBeTruthy(); // the app stays loaded underneath
    handle.click();
    expect(shown()).toBe(false);
    expect([views.inert, bar.inert]).toEqual([false, false]);
    // A short drag springs back; a long one closes it.
    s.shell.toggleCompanion();
    const drag = (dy: number) => {
      handle.dispatchEvent(new PointerEvent("pointerdown", { clientY: 100, pointerId: 1, bubbles: true }));
      handle.dispatchEvent(new PointerEvent("pointermove", { clientY: 100 + dy, pointerId: 1, bubbles: true }));
      expect(companion().style.transform).toBe(`translateY(${dy}px)`);
      handle.dispatchEvent(new PointerEvent("pointerup", { clientY: 100 + dy, pointerId: 1, bubbles: true }));
      handle.click(); // a drag ends in a click too
    };
    drag(40);
    expect(shown()).toBe(true);
    expect(companion().style.transform).toBe("");
    drag(120);
    expect(shown()).toBe(false);
  });

  it("uses a stored chord, but never the switcher's or a ruled-out one", async () => {
    let s = await setup("/notes", { rail: withCompanion, store: { "lodge:companionShortcut": "Ctrl+Alt+KeyJ" } });
    expect(s.shell.companionShortcut).toBe("Ctrl+Alt+KeyJ");
    window.dispatchEvent(key("KeyJ", "j", { ctrlKey: true, altKey: true }));
    expect(s.shell.companionShown).toBe(true);
    s.shell.stop();
    s = await setup("/notes", { rail: withCompanion, store: { "lodge:companionShortcut": "Ctrl+Backquote" } });
    expect(s.shell.companionShortcut).toBe("Ctrl+Shift+Backquote");
    s.shell.stop();
    s = await setup("/notes", { rail: withCompanion, store: { "lodge:companionShortcut": "Ctrl+KeyT" } });
    expect(s.shell.companionShortcut).toBe("Ctrl+Shift+Backquote");
  });
});
