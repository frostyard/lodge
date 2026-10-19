// @frostyard/lodge-client, the app half: what every app's frontend uses to
// run in lodge's shell (docs/specs/lodge-client.md, The library).
//
//   const lodge = await connectLodge({ app: "notes" });
//   lodge.route("/1/Projects/Plan.md", "Plan");
//   lodge.on("navigate", ({ route }) => router.go(route));

import { type Chord, matchesChord, parseChord } from "./chord.js";
import {
  type AppContext,
  type AppMessage,
  CONTRACT,
  type Context,
  type InitMessage,
  parseShellMessage,
  type Role,
  type RouteMessage,
  type Theme,
  validApp,
  validPrompt,
  validRoute,
} from "./messages.js";

export { type Chord, type KeyLike, matchesChord, parseChord } from "./chord.js";
export * from "./messages.js";

/** Where a message goes: a window's postMessage. */
export interface MessageTarget {
  postMessage(message: unknown, targetOrigin: string): void;
}

/** The parts of the page's window lodge-client uses; the page's own by default. */
export interface AppWindow extends MessageTarget {
  readonly parent: MessageTarget;
  readonly location: {
    readonly origin: string;
    readonly protocol: string;
    readonly host: string;
    readonly pathname: string;
    readonly search: string;
    readonly hash: string;
    replace(url: string): void;
  };
  addEventListener(type: string, listener: (event: Event) => void, options?: boolean | AddEventListenerOptions): void;
  removeEventListener(type: string, listener: (event: Event) => void, options?: boolean | EventListenerOptions): void;
}

export interface ConnectOptions {
  /** The app's id, as in its manifest. */
  app: string;
  /** The window to run in, for tests: the page's own by default. */
  window?: AppWindow;
}

/** A route split into its parts, as `history.location` gives it. */
export interface LodgeLocation {
  readonly pathname: string;
  readonly search: string;
  readonly hash: string;
}

export interface HistoryUpdate {
  /** push and replace come from the app; navigate from the shell. */
  action: "push" | "replace" | "navigate";
  location: LodgeLocation;
}

/**
 * A history for client-side routers, driven by `route` and `navigate`
 * instead of the window's. In the companion's frame it is kept in memory:
 * push and replace post nothing, and the shell sends no navigate.
 */
export interface LodgeHistory {
  readonly location: LodgeLocation;
  /** push posts `route` and calls the listeners. */
  push(route: string, title?: string): void;
  /** replace posts `route` with replace set, and calls the listeners. */
  replace(route: string, title?: string): void;
  /** listen calls `fn` on push, replace and navigate; it returns a function that stops it. */
  listen(fn: (update: HistoryUpdate) => void): () => void;
}

export interface LodgeEvents {
  navigate: { route: string };
  visible: { visible: boolean };
  theme: { scheme: Theme };
  /** In the companion's frame: the current app's context. */
  context: AppContext;
  /** In the companion's frame: the current app's "Ask about this", after its context. */
  ask: { app: string; prompt: string };
}

export interface Lodge {
  readonly app: string;
  /** The shell's contract major, from `init`. */
  readonly contract: number;
  readonly theme: Theme;
  /** The switcher's chord, such as "Ctrl+Backquote". The app must not bind it. */
  readonly shortcut: string;
  /** "companion" in the frame opened as the companion, else "app". */
  readonly role: Role;
  /** The companion's chord, such as "Ctrl+Shift+Backquote", or null without a companion. The app must not bind it. */
  readonly companion: string | null;
  /** https://CONTENT, or null for an app without content. */
  readonly contentOrigin: string | null;
  /** Whether this frame is the one shown, or the open companion. */
  readonly visible: boolean;
  readonly history: LodgeHistory;
  /** The app's /api WebSocket: wss://LODGE/app/<id>/api, or ws: on the development build. */
  apiURL(): string;
  /** contentOrigin + "/" + app + "/" + path; path has no leading "/" and is already encoded. */
  contentURL(path: string): string;
  /** Reports the app's route; the shell's address bar and history follow it. */
  route(path: string, title?: string, options?: { replace?: boolean }): void;
  /** Reports what is on screen, for the companion. */
  context(c: Context): void;
  /**
   * Opens the companion with a prompt about what is on screen: an app's
   * "Ask about this". Report the context first, and call it from the
   * handler of the input that asked. Offer it only when `companion` is not null.
   */
  ask(prompt: string): void;
  /** Asks the shell to open a URL: a canonical lodge URL, a content URL, or an outside one. */
  open(url: string): void;
  /** Opens the switcher, as its shortcut does. */
  openSwitcher(): void;
  /** "<id>:" + key: apps share the origin's storage. */
  storageKey(key: string): string;
  /**
   * on calls `fn` for each message of `type` from now on; it returns a
   * function that stops it. Nothing is replayed: history.location and the
   * fields above always hold the latest.
   */
  on<T extends keyof LodgeEvents>(type: T, fn: (event: LodgeEvents[T]) => void): () => void;
  /** close removes lodge-client's listeners. */
  close(): void;
}

/** frameRoute returns the route of a frame URL: its path past /app/<id>, with query and fragment. */
export function frameRoute(app: string, loc: LodgeLocation): string {
  const base = `/app/${app}`;
  let path = loc.pathname;
  if (path === base) path = "/";
  else if (path.startsWith(`${base}/`)) path = path.slice(base.length);
  return path + loc.search + loc.hash;
}

/** splitRoute splits a route into pathname, search and hash. */
export function splitRoute(route: string): LodgeLocation {
  let rest = route;
  let hash = "";
  let search = "";
  const h = rest.indexOf("#");
  if (h >= 0) {
    hash = rest.slice(h);
    rest = rest.slice(0, h);
  }
  const q = rest.indexOf("?");
  if (q >= 0) {
    search = rest.slice(q);
    rest = rest.slice(0, q);
  }
  return { pathname: rest, search, hash };
}

/**
 * connectLodge posts `ready` to the shell and resolves on its `init`.
 * Outside a frame it replaces the page with the app's canonical URL, so the
 * app always runs inside the shell, and never resolves.
 */
export function connectLodge(options: ConnectOptions): Promise<Lodge> {
  const { app } = options;
  if (!validApp(app)) throw new TypeError(`lodge-client: ${JSON.stringify(app)} is not an app id`);
  const win = options.window ?? (globalThis.window as unknown as AppWindow);
  const loc = win.location;
  if (win.parent === win) {
    const route = frameRoute(app, loc);
    loc.replace(`/${app}${route === "/" ? "" : route}`);
    return new Promise(() => {});
  }
  return new Promise((resolve) => {
    const onInit = (event: Event) => {
      const e = event as MessageEvent;
      if (e.origin !== loc.origin || e.source !== win.parent) return;
      const m = parseShellMessage(e.data);
      if (m?.type !== "init") return;
      win.removeEventListener("message", onInit);
      resolve(new Client(app, win, m));
    };
    win.addEventListener("message", onInit);
    post(win, { lodge: CONTRACT, type: "ready", contract: CONTRACT });
  });
}

function post(win: AppWindow, m: AppMessage): void {
  win.parent.postMessage(m, win.location.origin);
}

type Listeners = { [T in keyof LodgeEvents]: Set<(event: LodgeEvents[T]) => void> };

class Client implements Lodge {
  readonly app: string;
  readonly contract: number;
  readonly shortcut: string;
  readonly role: Role;
  readonly companion: string | null;
  readonly contentOrigin: string | null;
  readonly history: LodgeHistory;
  theme: Theme;
  visible = false;

  #win: AppWindow;
  #chord: Chord | null;
  #companionChord: Chord | null;
  #location: LodgeLocation;
  #historyListeners = new Set<(update: HistoryUpdate) => void>();
  #listeners: Listeners = {
    navigate: new Set(),
    visible: new Set(),
    theme: new Set(),
    context: new Set(),
    ask: new Set(),
  };

  constructor(app: string, win: AppWindow, init: InitMessage) {
    this.app = app;
    this.#win = win;
    this.contract = init.contract;
    this.shortcut = init.shortcut;
    this.role = init.role;
    this.companion = init.companion;
    this.contentOrigin = init.contentOrigin;
    this.theme = init.theme;
    this.#chord = parseChord(init.shortcut);
    this.#companionChord = init.companion === null ? null : parseChord(init.companion);
    this.#location = splitRoute(frameRoute(app, win.location));
    const self = this;
    this.history = {
      get location() {
        return self.#location;
      },
      push: (route, t) => this.#go("push", route, t),
      replace: (route, t) => this.#go("replace", route, t),
      listen: (fn) => {
        this.#historyListeners.add(fn);
        return () => this.#historyListeners.delete(fn);
      },
    };
    win.addEventListener("message", this.#onMessage);
    win.addEventListener("keydown", this.#onKey, { capture: true });
  }

  apiURL(): string {
    const scheme = this.#win.location.protocol === "http:" ? "ws:" : "wss:";
    return `${scheme}//${this.#win.location.host}/app/${this.app}/api`;
  }

  contentURL(path: string): string {
    if (this.contentOrigin === null) throw new Error(`lodge-client: app ${this.app} has no content origin`);
    if (path.startsWith("/")) throw new TypeError("lodge-client: a content path has no leading /");
    return `${this.contentOrigin}/${this.app}/${path}`;
  }

  route(path: string, t = "", options: { replace?: boolean } = {}): void {
    if (!validRoute(path)) throw new TypeError(`lodge-client: ${JSON.stringify(path)} is not a route`);
    this.#location = splitRoute(path);
    // The companion's routes are not the page's address: they stay here.
    if (this.role === "companion") return;
    const m: RouteMessage = { lodge: CONTRACT, type: "route", path, title: t };
    if (options.replace) m.replace = true;
    post(this.#win, m);
  }

  context(c: Context): void {
    post(this.#win, { lodge: CONTRACT, type: "context", kind: c.kind, ref: c.ref, title: c.title });
  }

  ask(prompt: string): void {
    if (!validPrompt(prompt)) throw new TypeError("lodge-client: a prompt is 1 to 4,000 characters");
    post(this.#win, { lodge: CONTRACT, type: "ask", prompt });
  }

  open(url: string): void {
    post(this.#win, { lodge: CONTRACT, type: "open", url });
  }

  openSwitcher(): void {
    post(this.#win, { lodge: CONTRACT, type: "shortcut", key: this.shortcut });
  }

  storageKey(key: string): string {
    return `${this.app}:${key}`;
  }

  on<T extends keyof LodgeEvents>(type: T, fn: (event: LodgeEvents[T]) => void): () => void {
    const set = this.#listeners[type] as Set<(event: LodgeEvents[T]) => void>;
    set.add(fn);
    return () => set.delete(fn);
  }

  close(): void {
    this.#win.removeEventListener("message", this.#onMessage);
    this.#win.removeEventListener("keydown", this.#onKey, { capture: true });
  }

  #go(action: "push" | "replace", route: string, t = ""): void {
    this.route(route, t, { replace: action === "replace" });
    this.#notify(action);
  }

  #notify(action: HistoryUpdate["action"]): void {
    const update = { action, location: this.#location };
    for (const fn of [...this.#historyListeners]) fn(update);
  }

  #emit<T extends keyof LodgeEvents>(type: T, event: LodgeEvents[T]): void {
    for (const fn of [...(this.#listeners[type] as Set<(event: LodgeEvents[T]) => void>)]) fn(event);
  }

  #onMessage = (event: Event): void => {
    const e = event as MessageEvent;
    if (e.origin !== this.#win.location.origin || e.source !== this.#win.parent) return;
    const m = parseShellMessage(e.data);
    switch (m?.type) {
      case "navigate":
        this.#location = splitRoute(m.route);
        this.#notify("navigate");
        this.#emit("navigate", { route: m.route });
        break;
      case "visible":
        this.visible = m.visible;
        this.#emit("visible", { visible: m.visible });
        break;
      case "theme":
        this.theme = m.scheme;
        this.#emit("theme", { scheme: m.scheme });
        break;
      case "context":
        this.#emit("context", { app: m.app, kind: m.kind, ref: m.ref, title: m.title });
        break;
      case "ask":
        this.#emit("ask", { app: m.app, prompt: m.prompt });
        break;
    }
  };

  #onKey = (event: Event): void => {
    const e = event as KeyboardEvent;
    let key: string;
    if (this.#chord !== null && matchesChord(e, this.#chord)) key = this.shortcut;
    else if (this.#companionChord !== null && this.companion !== null && matchesChord(e, this.#companionChord))
      key = this.companion;
    else return;
    e.preventDefault();
    e.stopImmediatePropagation();
    post(this.#win, { lodge: CONTRACT, type: "shortcut", key });
  };
}
