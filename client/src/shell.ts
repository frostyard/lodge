// @frostyard/lodge-client/shell, the shell half: it keeps one port per app
// frame, checks the origin and source of every message, answers `ready`
// with `init`, and queues `navigate` until then
// (docs/specs/lodge-client.md, Start).
//
//   const shell = createShell({ init: (app) => ({ contentOrigin, shortcut, theme }) });
//   const port = shell.attach("notes", iframe);
//   shell.on("route", ({ app, path, title, replace }) => ...);

import {
  type AppContext,
  CONTRACT,
  parseAppMessage,
  type ShellMessage,
  type Theme,
  validApp,
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
  theme: Theme;
}

export interface ShellOptions {
  /** init returns what `init` tells app, when its frame sends `ready`. */
  init(app: string): InitFields;
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

export interface ShellEvents {
  ready: { app: string };
  mismatch: { app: string; contract: unknown };
  unresponsive: { app: string };
  route: { app: string; path: string; title: string; replace: boolean };
  context: AppContext;
  open: { app: string; url: string };
  shortcut: { app: string; key: string };
}

/** One app frame's port. */
export interface FramePort {
  readonly app: string;
  readonly state: FrameState;
  /** navigate sends the app to a route, after `init` if that has not gone. */
  navigate(route: string): void;
  /** visible tells the app whether its frame is shown; the latest value is sent after `init`. */
  visible(visible: boolean): void;
  /** context forwards the current app's context to a frame opened as the companion. */
  context(context: AppContext): void;
  /** detach stops listening to the frame. */
  detach(): void;
}

export interface Shell {
  /** attach starts a port for app's frame. Attach before the frame loads. */
  attach(app: string, frame: FrameLike): FramePort;
  /** port returns app's port, if attached. */
  port(app: string): FramePort | undefined;
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
  const ports = new Map<string, Port>();
  const listeners: Listeners = {
    ready: new Set(),
    mismatch: new Set(),
    unresponsive: new Set(),
    route: new Set(),
    context: new Set(),
    open: new Set(),
    shortcut: new Set(),
  };
  const emit = <T extends keyof ShellEvents>(type: T, event: ShellEvents[T]) => {
    for (const fn of [...(listeners[type] as Set<(event: ShellEvents[T]) => void>)]) fn(event);
  };

  class Port implements FramePort {
    readonly app: string;
    state: FrameState = "loading";
    #frame: FrameLike;
    #queue: string[] = [];
    #visible: boolean | null = null;
    #timer: ReturnType<typeof setTimeout> | null = null;
    // The frame's document has loaded and not yet sent ready.
    #awaiting = false;
    // A ready came before its document's load event.
    #early = false;

    constructor(app: string, frame: FrameLike) {
      this.app = app;
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

    theme(scheme: Theme): void {
      if (this.state === "ready") this.#send({ lodge: CONTRACT, type: "theme", scheme });
    }

    detach(): void {
      this.#frame.removeEventListener("load", this.#onLoad);
      this.#stopTimer();
      if (ports.get(this.app) === this) ports.delete(this.app);
    }

    receive(data: unknown): void {
      // A ready of any major says whether the frame speaks this one.
      if (isReady(data)) {
        this.#ready(data.contract);
        return;
      }
      if (this.state !== "ready") return;
      const m = parseAppMessage(data);
      switch (m?.type) {
        case "route":
          emit("route", { app: this.app, path: m.path, title: m.title, replace: m.replace === true });
          break;
        case "context":
          emit("context", { app: this.app, kind: m.kind, ref: m.ref, title: m.title });
          break;
        case "open":
          emit("open", { app: this.app, url: m.url });
          break;
        case "shortcut":
          emit("shortcut", { app: this.app, key: m.key });
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
        emit("mismatch", { app: this.app, contract });
        return;
      }
      this.state = "ready";
      const f = options.init(this.app);
      this.#send({
        lodge: CONTRACT,
        type: "init",
        contract: CONTRACT,
        contentOrigin: f.contentOrigin,
        shortcut: f.shortcut,
        theme: f.theme,
      });
      if (this.#visible !== null) this.#send({ lodge: CONTRACT, type: "visible", visible: this.#visible });
      const queue = this.#queue;
      this.#queue = [];
      for (const route of queue) this.#send({ lodge: CONTRACT, type: "navigate", route });
      emit("ready", { app: this.app });
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
        emit("unresponsive", { app: this.app });
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
    attach(app, frame) {
      if (!validApp(app)) throw new TypeError(`lodge-client: ${JSON.stringify(app)} is not an app id`);
      ports.get(app)?.detach();
      const port = new Port(app, frame);
      ports.set(app, port);
      return port;
    },
    port: (app) => ports.get(app),
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
