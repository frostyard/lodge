// Fake windows for the tests: a shell window and an app frame on one
// origin, whose postMessage delivers asynchronously with the origin and
// source a browser would set.

import type { AppWindow, MessageTarget } from "../src/index.js";
import type { FrameLike, ShellWindow } from "../src/shell.js";

export const ORIGIN = "https://lodge.test";
export const CONTENT = "https://lodge-content.test";

type Posted = { data: unknown; targetOrigin: string };

// Messages arrive as tasks, as in a browser, on the real clock even under
// fake timers.
const later = globalThis.setTimeout;

class FakeWindow extends EventTarget {
  readonly origin: string;
  constructor(origin: string) {
    super();
    this.origin = origin;
  }
  /** deliver dispatches a message event as a browser would, later. */
  deliver(data: unknown, targetOrigin: string, origin: string, source: unknown): void {
    if (targetOrigin !== "*" && targetOrigin !== this.origin) return;
    const copy = structuredClone(data);
    later(() => {
      this.dispatchEvent(Object.assign(new Event("message"), { data: copy, origin, source }));
    }, 0);
  }
}

export class FakeShellWindow extends FakeWindow implements ShellWindow {
  readonly location: { origin: string };
  constructor(origin = ORIGIN) {
    super(origin);
    this.location = { origin };
  }
}

export interface Location {
  origin: string;
  protocol: string;
  host: string;
  pathname: string;
  search: string;
  hash: string;
  replaced: string[];
  replace(url: string): void;
}

export class FakeAppWindow extends FakeWindow implements AppWindow {
  parent: MessageTarget;
  readonly location: Location;
  constructor(url: string, parent?: MessageTarget) {
    const u = new URL(url);
    super(u.origin);
    const replaced: string[] = [];
    this.location = {
      origin: u.origin,
      protocol: u.protocol,
      host: u.host,
      pathname: u.pathname,
      search: u.search,
      hash: u.hash,
      replaced,
      replace: (to) => {
        replaced.push(to);
      },
    };
    this.parent = parent ?? this;
  }
  postMessage(message: unknown, targetOrigin: string): void {
    this.deliver(message, targetOrigin, this.origin, this);
  }
}

/** FakeFrame is an <iframe> whose contentWindow posts into an app window. */
export class FakeFrame extends EventTarget implements FrameLike {
  contentWindow: MessageTarget | null;
  constructor(contentWindow: MessageTarget) {
    super();
    this.contentWindow = contentWindow;
  }
  load(): void {
    this.dispatchEvent(new Event("load"));
  }
}

/**
 * pair wires a shell window and an app frame at url. The app sees its
 * parent as `parent`; the shell sees the frame's window as `frame.contentWindow`.
 */
export function pair(url: string, shell = new FakeShellWindow(new URL(url).origin)) {
  const appOrigin = new URL(url).origin;
  // What the app calls window.parent, and what the shell's messages come from.
  const parent: MessageTarget = {
    postMessage: (data, targetOrigin) => {
      sent.toShell.push({ data, targetOrigin });
      shell.deliver(data, targetOrigin, appOrigin, frameWindow);
    },
  };
  // What the shell calls frame.contentWindow.
  const frameWindow: MessageTarget = {
    postMessage: (data, targetOrigin) => {
      sent.toApp.push({ data, targetOrigin });
      app.deliver(data, targetOrigin, shell.origin, parent);
    },
  };
  const sent = { toShell: [] as Posted[], toApp: [] as Posted[] };
  const app = new FakeAppWindow(url, parent);
  const frame = new FakeFrame(frameWindow);
  return { shell, app, frame, parent, frameWindow, sent };
}

/** settle lets queued deliveries run. */
export async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await new Promise((resolve) => later(resolve, 0));
}

export function key(
  code: string,
  mods: Partial<Record<"ctrlKey" | "altKey" | "shiftKey" | "metaKey" | "isComposing", boolean>> = {},
) {
  return Object.assign(new Event("keydown", { cancelable: true }), {
    code,
    ctrlKey: false,
    altKey: false,
    shiftKey: false,
    metaKey: false,
    isComposing: false,
    ...mods,
  });
}
