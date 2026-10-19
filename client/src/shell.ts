// @frostyard/lodge-client/shell, the shell half: it keeps one port per
// frame, an app's own and the companion's, checks the origin and source of
// every message, answers `ready` with `init`, and queues `navigate` until
// then (docs/specs/lodge-client.md, Start). From the companion's frame it
// takes ready, open and shortcut, and drops the rest (The companion).
//
//   const shell = createShell({ init: (app, role) => ({ contentOrigin, shortcut, companion, theme }) });
//   const port = shell.attach("notes", iframe);
//   const side = shell.attach("porter", panelFrame, "companion");
//   shell.on("route", ({ app, path, title, replace }) => ...);

import {
  type AppContext,
  CONTRACT,
  parseAppMessage,
  type Role,
  type ShellMessage,
  type Theme,
  validApp,
  validPrompt,
  validRoute,
} from "./messages.js";

export { type Chord, type KeyLike, matchesChord, parseChord } from "./chord.js";
export * from "./messages.js";

/** Where a message goes: a frame's contentWindow. */
export interface MessageTarget {
  postMessage(message: unknown, targetOrigin: string): void;
}

/** The parts of the shell's window the shell half uses. */
export interface ShellWindow {
  readonly location: { readonly origin: string };
  addEventListener(type: string, listener: (event: Event) => void): void;
  removeEventListener(type: string, listener: (event: Event) => void): void;
}

/** The parts of an <iframe> the shell half uses. */
export interface FrameLike {
  readonly contentWindow: MessageTarget | null;
  addEventListener(type: string, listener: (event: Event) => void): void;
  removeEventListener(type: string, listener: (event: Event) => void): void;
}

/** What `init` tells an app. */
export interface InitFields {
  /** https://CONTENT for an app whose manifest sets content, else null. */
  contentOrigin: string | null;
  shortcut: string;
  /** The companion's chord, or null when no app is the companion. */
  companion: string | null;
  theme: Theme;
}

export interface ShellOptions {
  /** init returns what `init` tells app's frame of role, when it sends `ready`. */
  init(app: string, role: Role): InitFields;
  /** The shell's window: the page's own by default. */
  window?: ShellWindow;
  /** How long a frame has, after its load event, to send `ready`: 10 s by default. */
  readyTimeout?: number;
}

/**
 * A frame's state. loading: no `ready` yet. ready: `init` has gone.
 * mismatched: its `ready` named a contract this shell does not speak.
 * unresponsive: no `ready` within readyTimeout of its load event.
 */
export type FrameState = "loading" | "ready" | "mismatched" | "unresponsive";

/**
 * The shell half's events. Each names the frame's app and role; route,
 * context and ask come only from an app's own frame.
 */
export interface ShellEvents {
  ready: { app: string; role: Role };
  mismatch: { app: string; role: Role; contract: unknown };
  unresponsive: { app: string; role: Role };
  route: { app: string; path: string; title: string; replace: boolean };
  context: AppContext;
  ask: { app: string; prompt: string };
  open: { app: string; role: Role; url: string };
  shortcut: { app: string; role: Role; key: string };
}

/** One frame's port. */
export interface FramePort {
  readonly app: string;
  readonly role: Role;
  readonly state: FrameState;
  /** navigate sends the app to a route, after `init` if that has not gone. */
  navigate(route: string): void;
  /** visible tells the app whether its frame is shown; the latest value is sent after `init`. */
  visible(visible: boolean): void;
  /** context forwards the current app's context to the companion's frame. */
  context(context: AppContext): void;
  /** ask forwards the current app's ask to the companion's frame, after its context. */
  ask(app: string, prompt: string): void;
  /** detach stops listening to the frame. */
  detach(): void;
}

export interface Shell {
  /** attach starts a port for app's frame of role [app]. Attach before the frame loads. */
  attach(app: string, frame: FrameLike, role?: Role): FramePort;
  /** port returns the port of app's frame of role [app], if attached. */
  port(app: string, role?: Role): FramePort | undefined;
  /** theme sends `theme` to every ready frame. */
  theme(scheme: Theme): void;
  /** on calls `fn` for each event of `type`; it returns a function that stops it. */
  on<T extends keyof ShellEvents>(type: T, fn: (event: ShellEvents[T]) => void): () => void;
  /** close detaches every frame and stops listening. */
  close(): void;
}

type Listeners = { [T in keyof ShellEvents]: Set<(event: ShellEvents[T]) => void> };

/** createShell starts the shell half. */
export function createShell(options: ShellOptions): Shell {
  const win = options.window ?? (globalThis.window as unknown as ShellWindow);
  const timeout = options.readyTimeout ?? 10_000;
  // By role and app: an app's own frame, and the companion's.
  const ports = new Map<string, Port>();
  const portKey = (app: string, role: Role) => `${role}:${app}`;
  const listeners: Listeners = {
    ready: new Set(),
    mismatch: new Set(),
    unresponsive: new Set(),
    route: new Set(),
    context: new Set(),
    ask: new Set(),
    open: new Set(),
    shortcut: new Set(),
  };
  const emit = <T extends keyof ShellEvents>(type: T, event: ShellEvents[T]) => {
    for (const fn of [...(listeners[type] as Set<(event: ShellEvents[T]) => void>)]) fn(event);
  };

  class Port implements FramePort {
    readonly app: string;
    readonly role: Role;
    state: FrameState = "loading";
    #frame: FrameLike;
    #queue: string[] = [];
    #visible: boolean | null = null;
    #timer: ReturnType<typeof setTimeout> | null = null;
    // The frame's document has loaded and not yet sent ready.
    #awaiting = false;
    // A ready came before its document's load event.
    #early = false;

    constructor(app: string, frame: FrameLike, role: Role) {
      this.app = app;
      this.role = role;
      this.#frame = frame;
      frame.addEventListener("load", this.#onLoad);
    }

    get window(): MessageTarget | null {
      return this.#frame.contentWindow;
    }

    navigate(route: string): void {
      if (!validRoute(route)) throw new TypeError(`lodge-client: ${JSON.stringify(route)} is not a route`);
      if (this.state === "ready") this.#send({ lodge: CONTRACT, type: "navigate", route });
      else this.#queue.push(route);
    }

    visible(visible: boolean): void {
      this.#visible = visible;
      if (this.state === "ready") this.#send({ lodge: CONTRACT, type: "visible", visible });
    }

    context(c: AppContext): void {
      if (this.state === "ready") this.#send({ lodge: CONTRACT, type: "context", ...c });
    }

    ask(app: string, prompt: string): void {
      if (!validApp(app) || !validPrompt(prompt)) return;
      if (this.state === "ready") this.#send({ lodge: CONTRACT, type: "ask", app, prompt });
    }

    theme(scheme: Theme): void {
      if (this.state === "ready") this.#send({ lodge: CONTRACT, type: "theme", scheme });
    }

    detach(): void {
      this.#frame.removeEventListener("load", this.#onLoad);
      this.#stopTimer();
      const k = portKey(this.app, this.role);
      if (ports.get(k) === this) ports.delete(k);
    }

    receive(data: unknown): void {
      // A ready of any major says whether the frame speaks this one.
      if (isReady(data)) {
        this.#ready(data.contract);
        return;
      }
      if (this.state !== "ready") return;
      const m = parseAppMessage(data);
      const own = this.role === "app";
      switch (m?.type) {
        case "route":
          if (own) emit("route", { app: this.app, path: m.path, title: m.title, replace: m.replace === true });
          break;
        case "context":
          if (own) emit("context", { app: this.app, kind: m.kind, ref: m.ref, title: m.title });
          break;
        case "ask":
          if (own) emit("ask", { app: this.app, prompt: m.prompt });
          break;
        case "open":
          emit("open", { app: this.app, role: this.role, url: m.url });
          break;
        case "shortcut":
          emit("shortcut", { app: this.app, role: this.role, key: m.key });
          break;
      }
    }

    #ready(contract: unknown): void {
      this.#early = !this.#awaiting;
      this.#awaiting = false;
      this.#stopTimer();
      if (contract !== CONTRACT) {
        this.state = "mismatched";
        this.#queue = [];
        emit("mismatch", { app: this.app, role: this.role, contract });
        return;
      }
      this.state = "ready";
      const f = options.init(this.app, this.role);
      this.#send({
        lodge: CONTRACT,
        type: "init",
        contract: CONTRACT,
        contentOrigin: f.contentOrigin,
        shortcut: f.shortcut,
        theme: f.theme,
        role: this.role,
        companion: f.companion,
      });
      if (this.#visible !== null) this.#send({ lodge: CONTRACT, type: "visible", visible: this.#visible });
      const queue = this.#queue;
      this.#queue = [];
      for (const route of queue) this.#send({ lodge: CONTRACT, type: "navigate", route });
      emit("ready", { app: this.app, role: this.role });
    }

    #onLoad = (): void => {
      if (this.#early) {
        this.#early = false; // this document has sent ready already
        return;
      }
      // A new document: it is loading until it sends ready.
      this.state = "loading";
      this.#awaiting = true;
      this.#stopTimer();
      this.#timer = setTimeout(() => {
        this.#timer = null;
        this.state = "unresponsive";
        emit("unresponsive", { app: this.app, role: this.role });
      }, timeout);
    };

    #stopTimer(): void {
      if (this.#timer !== null) clearTimeout(this.#timer);
      this.#timer = null;
    }

    #send(m: ShellMessage): void {
      this.#frame.contentWindow?.postMessage(m, win.location.origin);
    }
  }

  const onMessage = (event: Event) => {
    const e = event as MessageEvent;
    if (e.origin !== win.location.origin || e.source === null) return;
    for (const port of ports.values()) {
      if (port.window !== null && port.window === e.source) {
        port.receive(e.data);
        return;
      }
    }
  };
  win.addEventListener("message", onMessage);

  return {
    attach(app, frame, role = "app") {
      if (!validApp(app)) throw new TypeError(`lodge-client: ${JSON.stringify(app)} is not an app id`);
      const k = portKey(app, role);
      ports.get(k)?.detach();
      const port = new Port(app, frame, role);
      ports.set(k, port);
      return port;
    },
    port: (app, role = "app") => ports.get(portKey(app, role)),
    theme(scheme) {
      for (const port of ports.values()) port.theme(scheme);
    },
    on(type, fn) {
      const set = listeners[type] as Set<typeof fn>;
      set.add(fn);
      return () => set.delete(fn);
    },
    close() {
      win.removeEventListener("message", onMessage);
      for (const port of [...ports.values()]) port.detach();
    },
  };
}

function isReady(data: unknown): data is { contract: unknown } {
  if (typeof data !== "object" || data === null) return false;
  const m = data as Record<string, unknown>;
  return m.type === "ready" && Number.isInteger(m.lodge);
}

/** Where `open` leads (docs/specs/lodge-client.md, open). */
export type OpenTarget =
  | { kind: "app"; app: string; route: string }
  | { kind: "content"; url: string }
  | { kind: "external"; url: string };

const EXTERNAL = new Set(["https:", "http:", "mailto:"]);

/**
 * resolveOpen classifies an `open` message's URL, or returns null for one
 * to ignore.
 *
 *   - A canonical lodge URL, /<id>/<route> or https://LODGE/<id>/<route>,
 *     opens app <id>. Paths under /app/, /lodge/, /mcp/ and /device/ are
 *     refused, as is any other path on the lodge origin.
 *   - A URL on the content origin opens in a new tab.
 *   - Any other https:, http: or mailto: URL opens in a new browsing context.
 */
export function resolveOpen(url: string, origins: { lodge: string; content: string | null }): OpenTarget | null {
  let u: URL;
  try {
    if (url.startsWith("/") && !url.startsWith("//")) u = new URL(url, origins.lodge);
    else u = new URL(url);
  } catch {
    return null;
  }
  if (u.origin === origins.lodge) {
    const [, app = "", ...rest] = u.pathname.split("/");
    if (!validApp(app)) return null;
    const route = `/${rest.join("/")}${u.search}${u.hash}`;
    return validRoute(route) ? { kind: "app", app, route } : null;
  }
  if (origins.content !== null && u.origin === origins.content) return { kind: "content", url: u.href };
  return EXTERNAL.has(u.protocol) ? { kind: "external", url: u.href } : null;
}
