// The shell page (docs/specs/shell.md): the rail or the bar, one frame per
// opened app that stays loaded, the switcher, the address bar and history,
// app states, and the theme. It talks to frames only through
// @frostyard/lodge-client's shell half.

import {
  type Chord,
  createShell,
  type FramePort,
  matchesChord,
  parseChord,
  resolveOpen,
  type Shell,
  type Theme,
} from "@frostyard/lodge-client/shell";
import {
  canonical,
  filterApps,
  firstApp,
  frameURL,
  type Place,
  parsePlace,
  parseRail,
  type Rail,
  type RailApp,
  titleFor,
} from "./model.js";
import { checkShortcut, DEFAULT_SHORTCUT, label } from "./shortcut.js";
import type { Settings } from "./storage.js";

/** How often the rail is read while the page is visible. */
export const POLL_EVERY = 15_000;

export interface Deps {
  window: Window;
  fetchRail(): Promise<unknown>;
  settings: Settings;
  /** How long a frame has to send ready after its load: 10 s. */
  readyTimeout?: number;
}

/** One opened app: its view, frame and port. */
interface View {
  app: string;
  el: HTMLElement;
  frame: HTMLIFrameElement | null;
  port: FramePort | null;
  notice: HTMLElement;
  route: string;
  title: string;
  /** Why the frame is covered: not responding, mismatched, or down. */
  problem: "" | "unresponsive" | "mismatched" | "down";
  /** How many times the frame has sent ready. */
  readies: number;
}

type HistoryState = { lodge: 1; app: string; route: string };

/** h makes an element. Text goes in as text, never as HTML. */
function h<K extends keyof HTMLElementTagNameMap>(
  doc: Document,
  tag: K,
  attrs: Record<string, string> = {},
  ...children: (Node | string)[]
): HTMLElementTagNameMap[K] {
  const el = doc.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, v);
  for (const c of children) el.append(c);
  return el;
}

export class LodgeShell {
  readonly #win: Window;
  readonly #doc: Document;
  readonly #deps: Deps;
  readonly #shell: Shell;
  #apps: RailApp[] = [];
  #contentOrigin: string | null = null;
  #reachable = true;
  readonly #views = new Map<string, View>();
  #current: string | null = null;
  #previous: string | null = null;
  #shortcut: string;
  #chord: Chord;
  #timer: ReturnType<typeof setInterval> | null = null;
  #unlisten: (() => void)[] = [];

  // The page's parts.
  readonly root: HTMLElement;
  readonly #railApps: HTMLElement;
  readonly #barApp: HTMLButtonElement;
  readonly #views_: HTMLElement;
  readonly #switcher: HTMLElement;
  readonly #filter: HTMLInputElement;
  readonly #list: HTMLElement;
  readonly #menu: HTMLElement;
  #selected = 0;
  #shown: RailApp[] = [];

  constructor(deps: Deps) {
    this.#deps = deps;
    this.#win = deps.window;
    this.#doc = deps.window.document;
    const stored = deps.settings.get("shortcut");
    this.#shortcut = stored && checkShortcut(stored) === null ? stored : DEFAULT_SHORTCUT;
    this.#chord = parseChord(this.#shortcut) ?? (parseChord(DEFAULT_SHORTCUT) as Chord);
    this.#shell = createShell({
      window: this.#win,
      readyTimeout: deps.readyTimeout ?? 10_000,
      init: (app) => ({
        contentOrigin: this.#apps.find((a) => a.id === app)?.content ? this.#contentOrigin : null,
        shortcut: this.#shortcut,
        theme: this.theme(),
      }),
    });

    const d = this.#doc;
    this.#railApps = h(d, "div", { class: "rail-apps" });
    this.#barApp = h(d, "button", { class: "bar-app", type: "button", "aria-label": "Switch app" });
    this.#views_ = h(d, "main", { class: "views" });
    this.#filter = h(d, "input", {
      class: "switcher-filter",
      type: "text",
      "aria-label": "Filter apps",
      autocomplete: "off",
      spellcheck: "false",
    });
    this.#list = h(d, "ul", { class: "switcher-list", role: "listbox", "aria-label": "Apps" });
    this.#switcher = h(
      d,
      "div",
      { class: "switcher", role: "dialog", "aria-modal": "true", "aria-label": "Switch app", hidden: "" },
      h(d, "div", { class: "switcher-box" }, this.#filter, this.#list),
    );
    this.#menu = this.#buildMenu();
    const state = () => h(d, "span", { class: "lodge-state", role: "status", title: "lodge is reachable" });
    const menuButton = () =>
      h(
        d,
        "button",
        { class: "menu-button", type: "button", "aria-label": "Lodge menu", "aria-haspopup": "dialog" },
        "⋯",
      );
    this.root = h(
      d,
      "div",
      { class: "lodge" },
      h(
        d,
        "nav",
        { class: "rail", "aria-label": "Apps" },
        this.#railApps,
        h(d, "div", { class: "rail-end" }, state(), menuButton()),
      ),
      h(d, "header", { class: "bar" }, this.#barApp, h(d, "div", { class: "bar-end" }, state(), menuButton())),
      this.#views_,
      this.#switcher,
      this.#menu,
    );
    this.#wire();
  }

  /** start reads the rail, opens the address bar's app, and starts polling. */
  async start(): Promise<void> {
    this.#applyTheme();
    await this.poll();
    const loc = this.#win.location;
    const place = parsePlace(loc.pathname, loc.search, loc.hash);
    const first = place ?? this.#defaultPlace();
    if (first) this.open(first.app, first.route, "replace");
    this.#timer = setInterval(() => {
      if (this.#doc.visibilityState === "visible") void this.poll();
    }, POLL_EVERY);
  }

  /** stop stops polling and listening. */
  stop(): void {
    if (this.#timer !== null) clearInterval(this.#timer);
    this.#shell.close();
    for (const off of this.#unlisten) off();
    this.#unlisten = [];
  }

  get current(): string | null {
    return this.#current;
  }

  #defaultPlace(): Place | null {
    const a = firstApp(this.#apps, this.#deps.settings.get("last"));
    return a ? { app: a.id, route: "/" } : null;
  }

  // --- The rail's data ---

  /** poll reads /lodge/apps and redraws the rail and the views' notices. */
  async poll(): Promise<void> {
    let rail: Rail | null = null;
    try {
      rail = parseRail(await this.#deps.fetchRail());
    } catch {
      rail = null;
    }
    if (!rail) {
      this.#setReachable(false);
      return;
    }
    this.#setReachable(true);
    this.#apps = rail.apps;
    this.#contentOrigin = rail.contentOrigin || null;
    this.#drawRail();
    for (const v of this.#views.values()) this.#drawNotice(v);
    if (!this.#switcher.hidden) this.#drawList();
  }

  #setReachable(ok: boolean): void {
    this.#reachable = ok;
    for (const s of this.root.querySelectorAll(".lodge-state")) {
      s.toggleAttribute("data-unreachable", !ok);
      s.setAttribute("title", ok ? "lodge is reachable" : "lodge is out of reach");
      s.textContent = ok ? "" : "lodge is out of reach";
    }
  }

  get reachable(): boolean {
    return this.#reachable;
  }

  #icon(a: RailApp): HTMLElement {
    const d = this.#doc;
    if (a.icon && a.state !== "disabled") return h(d, "img", { class: "app-icon", src: a.icon, alt: "" });
    return h(d, "span", { class: "app-icon app-letter", "aria-hidden": "true" }, a.name.slice(0, 1).toUpperCase());
  }

  #drawRail(): void {
    const d = this.#doc;
    const buttons = this.#apps.map((a) => {
      const b = h(
        d,
        "button",
        { class: "app-button", type: "button", "data-app": a.id, title: this.#describe(a), "aria-label": a.name },
        this.#icon(a),
        h(d, "span", { class: "mark", "data-state": a.state }),
      );
      if (a.id === this.#current) b.setAttribute("aria-current", "page");
      b.addEventListener("click", () => this.switchTo(a.id));
      return b;
    });
    this.#railApps.replaceChildren(...buttons);
    this.#drawBar();
  }

  #describe(a: RailApp): string {
    return a.state === "up" ? a.name : `${a.name} (${a.state}${a.reason ? `: ${a.reason}` : ""})`;
  }

  #drawBar(): void {
    const a = this.#apps.find((x) => x.id === this.#current);
    const d = this.#doc;
    this.#barApp.replaceChildren(
      ...(a
        ? [
            this.#icon(a),
            h(d, "span", { class: "bar-name" }, a.name),
            h(d, "span", { class: "mark", "data-state": a.state }),
          ]
        : [h(d, "span", { class: "bar-name" }, "Lodge")]),
    );
  }

  // --- Views and frames ---

  #view(app: string, route: string): View {
    const existing = this.#views.get(app);
    if (existing) return existing;
    const d = this.#doc;
    const a = this.#apps.find((x) => x.id === app);
    const notice = h(d, "div", { class: "notice", role: "alert", hidden: "" });
    const el = h(d, "section", { class: "view", "data-app": app, hidden: "", inert: "" }, notice);
    const v: View = { app, el, frame: null, port: null, notice, route, title: "", problem: "", readies: 0 };
    if (a && a.state !== "disabled") this.#createFrame(v, a);
    this.#views.set(app, v);
    this.#views_.append(el);
    this.#drawNotice(v);
    return v;
  }

  #createFrame(v: View, a: RailApp): void {
    const frame = h(this.#doc, "iframe", { title: a.name, referrerpolicy: "no-referrer", class: "frame" });
    // The port listens before the frame loads.
    const port = this.#shell.attach(a.id, frame);
    v.frame = frame;
    v.port = port;
    frame.src = frameURL(a.id, v.route);
    v.el.prepend(frame);
  }

  /** reload loads an app's frame again, at its last route. */
  reload(app: string): void {
    const v = this.#views.get(app);
    const a = this.#apps.find((x) => x.id === app);
    if (!v || !a || a.state === "disabled") return;
    v.problem = "";
    if (!v.frame) this.#createFrame(v, a);
    else if (v.frame) v.frame.src = frameURL(app, v.route);
    this.#drawNotice(v);
  }

  #drawNotice(v: View): void {
    const d = this.#doc;
    const a = this.#apps.find((x) => x.id === v.app);
    const name = a?.name ?? v.app;
    const parts: (Node | string)[] = [];
    let covered = false;
    if (!a) {
      parts.push(h(d, "p", {}, `No app is installed at /${v.app}.`));
      covered = true;
    } else if (a.state === "disabled") {
      parts.push(h(d, "p", {}, `${name} is disabled: ${a.reason || "no reason given"}.`));
      covered = true;
    } else if (v.problem === "mismatched") {
      parts.push(
        h(d, "p", {}, `${name} speaks another version of lodge's protocol than this shell. Update one of them.`),
      );
      covered = true;
    } else if (v.problem === "unresponsive") {
      parts.push(h(d, "p", {}, `${name} is not responding.`), this.#reloadButton(v.app));
      covered = true;
    } else if (a.state === "down") {
      v.problem = "down";
      parts.push(h(d, "p", {}, `${name} is down${a.reason ? `: ${a.reason}` : ""}.`));
    } else if (v.problem === "down") {
      parts.push(h(d, "p", {}, `${name} is back.`), this.#reloadButton(v.app));
    }
    v.notice.replaceChildren(...parts);
    v.notice.hidden = parts.length === 0;
    v.notice.toggleAttribute("data-covering", covered);
  }

  #reloadButton(app: string): HTMLButtonElement {
    const b = h(this.#doc, "button", { type: "button", class: "reload" }, "Reload");
    b.addEventListener("click", () => this.reload(app));
    return b;
  }

  /**
   * open shows app at route: it creates the frame, or sends navigate to a
   * loaded one, and pushes or replaces a history entry.
   */
  open(app: string, route: string, history: "push" | "replace" | "none" = "push"): void {
    const had = this.#views.has(app);
    const v = this.#view(app, route);
    if (had && v.route !== route) {
      v.route = route;
      v.port?.navigate(route);
    }
    this.#show(app);
    this.#record(history, app, v.route);
  }

  /** switchTo shows app at its current route, from the rail or the switcher. */
  switchTo(app: string): void {
    const v = this.#views.get(app);
    if (app === this.#current) {
      this.#focusFrame();
      return;
    }
    this.open(app, v?.route ?? "/");
  }

  #show(app: string): void {
    const prev = this.#current;
    if (prev !== app) {
      if (prev !== null) {
        this.#previous = prev;
        const pv = this.#views.get(prev);
        if (pv) {
          pv.el.hidden = true;
          pv.el.inert = true;
          pv.port?.visible(false);
        }
      }
      this.#current = app;
      this.#deps.settings.set("last", app);
    }
    const v = this.#views.get(app);
    if (v) {
      v.el.hidden = false;
      v.el.inert = false;
      v.port?.visible(true);
    }
    for (const b of this.#railApps.querySelectorAll(".app-button")) {
      if (b.getAttribute("data-app") === app) b.setAttribute("aria-current", "page");
      else b.removeAttribute("aria-current");
    }
    this.#drawBar();
    this.#title();
    this.#focusFrame();
  }

  #focusFrame(): void {
    const v = this.#current ? this.#views.get(this.#current) : undefined;
    if (!v?.frame) return;
    v.frame.focus();
    try {
      v.frame.contentWindow?.focus();
    } catch {
      // A frame that is not loaded yet takes focus when it loads.
    }
  }

  #title(): void {
    const app = this.#current;
    const a = this.#apps.find((x) => x.id === app);
    const v = app ? this.#views.get(app) : undefined;
    this.#doc.title = a ? titleFor(v?.title ?? "", a.name) : "Lodge";
  }

  #record(how: "push" | "replace" | "none", app: string, route: string): void {
    if (how === "none") return;
    const state: HistoryState = { lodge: 1, app, route };
    const url = canonical(app, route);
    const cur = this.#win.history.state as HistoryState | null;
    if (how === "push" && cur?.app === app && cur?.route === route) return;
    if (how === "push") this.#win.history.pushState(state, "", url);
    else this.#win.history.replaceState(state, "", url);
  }

  #onPop = (e: PopStateEvent): void => {
    const s = e.state as HistoryState | null;
    let place: Place | null = s?.lodge === 1 ? { app: s.app, route: s.route } : null;
    if (!place) {
      const loc = this.#win.location;
      place = parsePlace(loc.pathname, loc.search, loc.hash) ?? this.#defaultPlace();
    }
    if (place) this.open(place.app, place.route, "none");
  };

  // --- Messages from frames ---

  #onRoute = ({ app, path, title, replace }: { app: string; path: string; title: string; replace: boolean }): void => {
    const v = this.#views.get(app);
    if (!v) return;
    const t = title.slice(0, 200);
    const same = v.route === path;
    v.route = path;
    v.title = t;
    if (app === this.#current) {
      this.#title();
      const cur = this.#win.history.state as HistoryState | null;
      // A route equal to the app's current entry changes nothing.
      if (!(same && cur?.app === app && cur?.route === path)) this.#record(replace ? "replace" : "push", app, path);
    }
  };

  #onOpen = ({ url }: { app: string; url: string }): void => {
    const t = resolveOpen(url, { lodge: this.#win.location.origin, content: this.#contentOrigin });
    if (!t) return;
    if (t.kind === "app") {
      this.open(t.app, t.route);
      return;
    }
    // Content opens in a new tab, still sandboxed; anything else in a new
    // browsing context, without opener or referrer.
    this.#win.open(t.url, "_blank", "noopener,noreferrer");
  };

  // --- The switcher ---

  get switcherOpen(): boolean {
    return !this.#switcher.hidden;
  }

  openSwitcher(): void {
    this.#closeMenu();
    this.#filter.value = "";
    this.#switcher.hidden = false;
    this.#drawList();
    this.#filter.focus();
  }

  closeSwitcher(focusFrame = true): void {
    this.#switcher.hidden = true;
    if (focusFrame) this.#focusFrame();
  }

  /** onShortcut opens the switcher, or, when it is open, the app shown before the current one. */
  onShortcut(): void {
    if (this.switcherOpen) {
      this.closeSwitcher(false);
      if (this.#previous && this.#apps.some((a) => a.id === this.#previous)) this.switchTo(this.#previous);
      else this.#focusFrame();
      return;
    }
    this.openSwitcher();
  }

  #drawList(): void {
    const d = this.#doc;
    this.#shown = filterApps(this.#apps, this.#filter.value);
    if (this.#filter.value === "") {
      const i = this.#shown.findIndex((a) => a.id === this.#current);
      this.#selected = Math.max(0, i);
    } else {
      this.#selected = 0;
    }
    const items = this.#shown.map((a, i) => {
      const li = h(
        d,
        "li",
        {
          class: "switcher-item",
          role: "option",
          "data-app": a.id,
          "aria-selected": String(i === this.#selected),
        },
        this.#icon(a),
        h(d, "span", { class: "switcher-name" }, a.name),
        h(d, "span", { class: "mark", "data-state": a.state, title: a.state }),
        h(d, "kbd", { class: "switcher-key" }, a.key),
      );
      if (a.id === this.#current) li.setAttribute("data-current", "");
      li.addEventListener("click", () => this.#choose(a.id));
      return li;
    });
    if (items.length === 0) items.push(h(d, "li", { class: "switcher-empty" }, "No app matches."));
    this.#list.replaceChildren(...items);
  }

  #select(i: number): void {
    if (this.#shown.length === 0) return;
    this.#selected = (i + this.#shown.length) % this.#shown.length;
    this.#list.querySelectorAll(".switcher-item").forEach((li, j) => {
      li.setAttribute("aria-selected", String(j === this.#selected));
    });
  }

  #choose(app: string): void {
    this.closeSwitcher(false);
    this.switchTo(app);
  }

  #onFilterKey = (e: KeyboardEvent): void => {
    if (matchesChord(e, this.#chord)) {
      e.preventDefault();
      e.stopPropagation();
      this.onShortcut();
      return;
    }
    switch (e.key) {
      case "Escape":
        e.preventDefault();
        this.closeSwitcher();
        return;
      case "ArrowDown":
        e.preventDefault();
        this.#select(this.#selected + 1);
        return;
      case "ArrowUp":
        e.preventDefault();
        this.#select(this.#selected - 1);
        return;
      case "Enter": {
        e.preventDefault();
        const a = this.#shown[this.#selected];
        if (a) this.#choose(a.id);
        return;
      }
    }
    // An app's key, as the first key pressed, opens it at once.
    const plain = !e.ctrlKey && !e.altKey && !e.metaKey && e.key.length === 1;
    if (plain && this.#filter.value === "") {
      const a = this.#apps.find((x) => x.key !== "" && x.key === e.key.toLowerCase());
      if (a) {
        e.preventDefault();
        this.#choose(a.id);
      }
    }
  };

  // --- The menu: theme and shortcut ---

  #buildMenu(): HTMLElement {
    const d = this.#doc;
    const themes = (["system", "light", "dark"] as const).map((t) => {
      const input = h(d, "input", { type: "radio", name: "lodge-theme", value: t });
      input.addEventListener("change", () => {
        this.#deps.settings.set("theme", t === "system" ? null : t);
        this.#applyTheme();
      });
      return h(d, "label", {}, input, ` ${t[0]?.toUpperCase()}${t.slice(1)}`);
    });
    const shortcut = h(d, "input", { type: "text", class: "menu-shortcut", "aria-label": "Switcher shortcut" });
    const why = h(d, "p", { class: "menu-why", role: "status" });
    const save = h(d, "button", { type: "button" }, "Save");
    const reset = h(d, "button", { type: "button" }, "Default");
    save.addEventListener("click", () => {
      const err = checkShortcut(shortcut.value.trim());
      if (err) {
        why.textContent = err;
        return;
      }
      this.#setShortcut(shortcut.value.trim());
      why.textContent = "Saved. Apps already open learn it when they next load.";
    });
    reset.addEventListener("click", () => {
      this.#setShortcut(DEFAULT_SHORTCUT);
      shortcut.value = DEFAULT_SHORTCUT;
      why.textContent = "";
    });
    const menu = h(
      d,
      "div",
      { class: "menu", role: "dialog", "aria-label": "Lodge", hidden: "" },
      h(d, "fieldset", {}, h(d, "legend", {}, "Theme"), ...themes),
      h(
        d,
        "fieldset",
        {},
        h(d, "legend", {}, "Switcher shortcut"),
        shortcut,
        h(d, "div", { class: "menu-buttons" }, save, reset),
        why,
      ),
    );
    return menu;
  }

  #setShortcut(chord: string): void {
    this.#shortcut = chord;
    this.#chord = parseChord(chord) ?? this.#chord;
    this.#deps.settings.set("shortcut", chord === DEFAULT_SHORTCUT ? null : chord);
  }

  get shortcut(): string {
    return this.#shortcut;
  }

  #toggleMenu(): void {
    if (!this.#menu.hidden) {
      this.#closeMenu();
      return;
    }
    const setting = this.#deps.settings.get("theme") ?? "system";
    for (const r of this.#menu.querySelectorAll<HTMLInputElement>("input[type=radio]")) r.checked = r.value === setting;
    const s = this.#menu.querySelector<HTMLInputElement>(".menu-shortcut");
    if (s) s.value = this.#shortcut;
    this.#menu.hidden = false;
    this.#menu.querySelector<HTMLElement>("input")?.focus();
  }

  #closeMenu(): void {
    this.#menu.hidden = true;
  }

  /** theme is the scheme in effect: the menu's, or the system's. */
  theme(): Theme {
    const s = this.#deps.settings.get("theme");
    if (s === "light" || s === "dark") return s;
    return this.#win.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }

  #applyTheme(): void {
    const t = this.theme();
    this.#doc.documentElement.dataset.theme = t;
    this.#shell.theme(t);
  }

  // --- Wiring ---

  #wire(): void {
    this.#shell.on("route", this.#onRoute);
    this.#shell.on("open", this.#onOpen);
    this.#shell.on("shortcut", () => this.onShortcut());
    this.#shell.on("ready", ({ app }) => {
      const v = this.#views.get(app);
      // A frame that loads again, discarded by the browser or reloaded,
      // goes back to its last route.
      if (v && v.readies++ > 0) v.port?.navigate(v.route);
      if (v && (v.problem === "unresponsive" || v.problem === "mismatched")) {
        v.problem = "";
        this.#drawNotice(v);
      }
      if (app === this.#current) this.#focusFrame();
    });
    this.#shell.on("unresponsive", ({ app }) => this.#problem(app, "unresponsive"));
    this.#shell.on("mismatch", ({ app }) => this.#problem(app, "mismatched"));
    this.#filter.addEventListener("keydown", this.#onFilterKey);
    this.#filter.addEventListener("input", () => this.#drawList());
    this.#switcher.addEventListener("click", (e) => {
      if (e.target === this.#switcher) this.closeSwitcher();
    });
    this.#barApp.addEventListener("click", () => this.openSwitcher());
    for (const b of this.root.querySelectorAll(".menu-button")) b.addEventListener("click", () => this.#toggleMenu());
    this.#menu.addEventListener("keydown", (e) => {
      if ((e as KeyboardEvent).key === "Escape") {
        this.#closeMenu();
        this.#focusFrame();
      }
    });
    const listen = (target: EventTarget | undefined, type: string, fn: (e: Event) => void) => {
      target?.addEventListener(type, fn);
      this.#unlisten.push(() => target?.removeEventListener(type, fn));
    };
    listen(this.#win, "keydown", (e) => {
      const k = e as KeyboardEvent;
      if (!this.switcherOpen && matchesChord(k, this.#chord)) {
        k.preventDefault();
        k.stopPropagation();
        this.onShortcut();
      }
    });
    listen(this.#win, "popstate", (e) => this.#onPop(e as PopStateEvent));
    listen(this.#doc, "visibilitychange", () => {
      if (this.#doc.visibilityState === "visible") void this.poll();
    });
    listen(this.#win.matchMedia?.("(prefers-color-scheme: dark)"), "change", () => this.#applyTheme());
  }

  #problem(app: string, p: View["problem"]): void {
    const v = this.#views.get(app);
    if (!v) return;
    v.problem = p;
    this.#drawNotice(v);
  }

  /** shortcutLabel is the shortcut for people, such as Ctrl+`. */
  get shortcutLabel(): string {
    return label(this.#shortcut);
  }
}
