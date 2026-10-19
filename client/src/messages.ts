// The messages between lodge's shell and an app's frame, as TypeScript types
// (docs/specs/lodge-client.md, Messages), and the checks both halves apply
// to what they receive.

/** The contract major this library speaks: the envelope's `lodge`. */
export const CONTRACT = 1;

/** The switcher's default shortcut (docs/specs/shell.md, The switcher). */
export const DEFAULT_SHORTCUT = "Ctrl+Backquote";

/** The companion's default chord: the switcher's with Shift (docs/specs/shell.md, The companion). */
export const DEFAULT_COMPANION = "Ctrl+Shift+Backquote";

/** Limits on message fields, in characters. */
export const MAX_ROUTE = 2048;
export const MAX_TITLE = 200;
export const MAX_KIND = 32;
export const MAX_REF = 1024;
export const MAX_PROMPT = 4000;

export type Theme = "light" | "dark";

/** A frame's role: an app's own frame, or the frame opened as the companion. */
export type Role = "app" | "companion";

interface Envelope<T extends string> {
  lodge: typeof CONTRACT;
  type: T;
}

/** app → shell: the app's lodge-client is listening. */
export interface ReadyMessage extends Envelope<"ready"> {
  contract: number;
}

/** app → shell: the app's route changed. */
export interface RouteMessage extends Envelope<"route"> {
  path: string;
  title: string;
  replace?: boolean;
}

/** What is on screen, for the companion. `kind` null says nothing is open. */
export interface Context {
  kind: string | null;
  ref: string;
  title: string;
}

/** app → shell: what is on screen. */
export interface ContextMessage extends Envelope<"context">, Context {}

/** app → shell: open a URL, possibly in another app. */
export interface OpenMessage extends Envelope<"open"> {
  url: string;
}

/** app → shell: the switcher's shortcut or the companion's chord was pressed, or the app asked for the switcher. */
export interface ShortcutMessage extends Envelope<"shortcut"> {
  key: string;
}

/** app → shell: open the companion with a prompt about what is on screen. */
export interface AskMessage extends Envelope<"ask"> {
  prompt: string;
}

export type AppMessage = ReadyMessage | RouteMessage | ContextMessage | OpenMessage | ShortcutMessage | AskMessage;

/** shell → app: the answer to `ready`. */
export interface InitMessage extends Envelope<"init"> {
  contract: number;
  contentOrigin: string | null;
  shortcut: string;
  theme: Theme;
  /** "companion" in the frame opened as the companion, else "app"; "app" from a shell that sends none. */
  role: Role;
  /** The companion's chord, or null when no app is the companion or the shell sends none. */
  companion: string | null;
}

/** shell → app: go to this route. */
export interface NavigateMessage extends Envelope<"navigate"> {
  route: string;
}

/** shell → app: the frame was shown or hidden. */
export interface VisibleMessage extends Envelope<"visible"> {
  visible: boolean;
}

/** shell → app: the theme changed. */
export interface ThemeMessage extends Envelope<"theme"> {
  scheme: Theme;
}

/** The current app's context, as the companion receives it. */
export interface AppContext extends Context {
  app: string;
}

/** shell → companion: the current app's context. */
export interface CompanionContextMessage extends Envelope<"context">, AppContext {}

/** shell → companion: the current app's `ask`, after its context. */
export interface CompanionAskMessage extends Envelope<"ask"> {
  app: string;
  prompt: string;
}

export type ShellMessage =
  | InitMessage
  | NavigateMessage
  | VisibleMessage
  | ThemeMessage
  | CompanionContextMessage
  | CompanionAskMessage;

const ID = /^[a-z][a-z0-9]{0,15}$/;
const RESERVED = new Set(["app", "device", "lodge", "mcp"]);
const KIND = /^[a-z-]{1,32}$/;

/** validApp reports whether `id` may name an app (docs/specs/app-contract.md, The manifest). */
export function validApp(id: string): boolean {
  return ID.test(id) && !RESERVED.has(id);
}

/** validRoute reports whether `route` is a route: it starts with `/`, and is at most 2,048 characters. */
export function validRoute(route: unknown): route is string {
  return typeof route === "string" && route.startsWith("/") && route.length <= MAX_ROUTE;
}

/** validContext reports whether a context's fields are within their limits. */
export function validContext(c: Context): boolean {
  return (
    (c.kind === null || (typeof c.kind === "string" && KIND.test(c.kind))) &&
    typeof c.ref === "string" &&
    c.ref.length <= MAX_REF &&
    typeof c.title === "string"
  );
}

/** validPrompt reports whether `prompt` is 1 to MAX_PROMPT characters. */
export function validPrompt(prompt: unknown): prompt is string {
  if (typeof prompt !== "string" || prompt.length === 0) return false;
  return prompt.length <= MAX_PROMPT || Array.from(prompt).length <= MAX_PROMPT;
}

/** title cuts a title to MAX_TITLE characters. */
export function title(s: string): string {
  if (s.length <= MAX_TITLE) return s;
  return Array.from(s).slice(0, MAX_TITLE).join("");
}

type Fields = Record<string, unknown>;

function envelope(data: unknown): Fields | null {
  if (typeof data !== "object" || data === null || Array.isArray(data)) return null;
  const m = data as Fields;
  return m.lodge === CONTRACT && typeof m.type === "string" ? m : null;
}

function isTheme(v: unknown): v is Theme {
  return v === "light" || v === "dark";
}

function context(m: Fields): Context | null {
  const c = { kind: m.kind as string | null, ref: m.ref as string, title: m.title as string };
  return validContext(c) ? { ...c, title: title(c.title) } : null;
}

/**
 * parseAppMessage returns the message an app sent, checked and copied, or
 * null for anything to drop: another major, an unknown type, or a field
 * out of shape. Titles over MAX_TITLE are cut.
 */
export function parseAppMessage(data: unknown): AppMessage | null {
  const m = envelope(data);
  if (m === null) return null;
  const lodge = CONTRACT;
  switch (m.type) {
    case "ready":
      return Number.isInteger(m.contract) ? { lodge, type: "ready", contract: m.contract as number } : null;
    case "route": {
      if (!validRoute(m.path) || typeof m.title !== "string") return null;
      if (m.replace !== undefined && typeof m.replace !== "boolean") return null;
      const r: RouteMessage = { lodge, type: "route", path: m.path, title: title(m.title) };
      if (m.replace) r.replace = true;
      return r;
    }
    case "context": {
      const c = context(m);
      return c === null ? null : { lodge, type: "context", ...c };
    }
    case "open":
      return typeof m.url === "string" ? { lodge, type: "open", url: m.url } : null;
    case "shortcut":
      return typeof m.key === "string" ? { lodge, type: "shortcut", key: m.key } : null;
    case "ask":
      return validPrompt(m.prompt) ? { lodge, type: "ask", prompt: m.prompt } : null;
  }
  return null;
}

/** parseShellMessage is parseAppMessage for what the shell sends a frame. */
export function parseShellMessage(data: unknown): ShellMessage | null {
  const m = envelope(data);
  if (m === null) return null;
  const lodge = CONTRACT;
  switch (m.type) {
    case "init":
      if (
        !Number.isInteger(m.contract) ||
        !(m.contentOrigin === null || typeof m.contentOrigin === "string") ||
        typeof m.shortcut !== "string" ||
        !isTheme(m.theme) ||
        !(m.role === undefined || m.role === "app" || m.role === "companion") ||
        !(m.companion === undefined || m.companion === null || typeof m.companion === "string")
      ) {
        return null;
      }
      return {
        lodge,
        type: "init",
        contract: m.contract as number,
        contentOrigin: m.contentOrigin,
        shortcut: m.shortcut,
        theme: m.theme,
        role: m.role ?? "app",
        companion: m.companion ?? null,
      };
    case "navigate":
      return validRoute(m.route) ? { lodge, type: "navigate", route: m.route } : null;
    case "visible":
      return typeof m.visible === "boolean" ? { lodge, type: "visible", visible: m.visible } : null;
    case "theme":
      return isTheme(m.scheme) ? { lodge, type: "theme", scheme: m.scheme } : null;
    case "context": {
      const c = context(m);
      if (c === null || typeof m.app !== "string" || !validApp(m.app)) return null;
      return { lodge, type: "context", app: m.app, ...c };
    }
    case "ask":
      if (typeof m.app !== "string" || !validApp(m.app) || !validPrompt(m.prompt)) return null;
      return { lodge, type: "ask", app: m.app, prompt: m.prompt };
  }
  return null;
}
