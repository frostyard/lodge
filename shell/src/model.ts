// The shell's pure logic: the rail's data, addresses and the switcher's
// filter (docs/specs/shell.md). Nothing here touches the DOM.

/** An app's state on the rail (docs/specs/lodge-http.md, Shell routes). */
export type AppState = "up" | "degraded" | "down" | "disabled";

/** One app in GET /lodge/apps. */
export interface RailApp {
  id: string;
  name: string;
  icon: string;
  key: string;
  content: boolean;
  companion: boolean;
  state: AppState;
  reason: string;
  /** The count of things the user has not seen: 0 for none, 99999 at most. */
  badge: number;
}

/** GET /lodge/apps. */
export interface Rail {
  contract: number;
  contentOrigin: string;
  apps: RailApp[];
}

const STATES = new Set<string>(["up", "degraded", "down", "disabled"]);
const ID = /^[a-z][a-z0-9]{0,15}$/;
const RESERVED = new Set(["app", "device", "lodge", "mcp"]);
/** MAX_BADGE is the largest count lodge sends (ADR-0020). */
const MAX_BADGE = 99999;

/** validApp reports whether id may name an app. */
export function validApp(id: string): boolean {
  return ID.test(id) && !RESERVED.has(id);
}

const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** parseRail checks /lodge/apps's answer, dropping apps it cannot use. */
export function parseRail(data: unknown): Rail | null {
  if (typeof data !== "object" || data === null) return null;
  const d = data as Record<string, unknown>;
  if (typeof d.contract !== "number" || !Array.isArray(d.apps)) return null;
  const apps: RailApp[] = [];
  for (const a of d.apps as unknown[]) {
    if (typeof a !== "object" || a === null) continue;
    const r = a as Record<string, unknown>;
    const id = str(r.id);
    if (!validApp(id) || apps.some((x) => x.id === id)) continue;
    const icon = str(r.icon);
    const state = STATES.has(str(r.state)) ? (str(r.state) as AppState) : "down";
    const n = r.badge;
    const badge = typeof n === "number" && Number.isSafeInteger(n) && n > 0 ? Math.min(n, MAX_BADGE) : 0;
    apps.push({
      id,
      name: str(r.name) || id,
      // Only an app's own files may be its icon.
      icon: icon.startsWith(`/app/${id}/`) ? icon : "",
      key: /^[a-z]$/.test(str(r.key)) ? str(r.key) : "",
      content: r.content === true,
      companion: r.companion === true,
      state,
      reason: str(r.reason),
      // Only an app that is up shows a count: a degraded, down or disabled
      // app shows its state mark instead (shell.md, Badges).
      badge: state === "up" ? badge : 0,
    });
  }
  return { contract: d.contract, contentOrigin: str(d.contentOrigin), apps };
}

/** badgeText is a count as a badge shows it: "" for none, 99+ above 99. */
export function badgeText(n: number): string {
  if (n <= 0) return "";
  return n > 99 ? "99+" : String(n);
}

/** badgeTotal is the apps' counts added up, without the app whose id is except. */
export function badgeTotal(apps: readonly RailApp[], except: string | null = null): number {
  return apps.reduce((sum, a) => (a.id === except ? sum : sum + a.badge), 0);
}

/** canonical is an app's canonical URL: /<id>, then the route unless it is "/". */
export function canonical(app: string, route: string): string {
  return route === "/" ? `/${app}` : `/${app}${route}`;
}

/** frameURL is where an app's frame loads a route. */
export function frameURL(app: string, route: string): string {
  return `/app/${app}${route}`;
}

/** A location in the shell: an app and its route, or none for "/". */
export interface Place {
  app: string;
  route: string;
}

/**
 * parsePlace reads the address bar: /<id> and /<id>/<route>, with the
 * query and fragment, open app <id> at that route. "/" is null.
 */
export function parsePlace(pathname: string, search = "", hash = ""): Place | null {
  const m = /^\/([^/]+)(\/.*)?$/.exec(pathname);
  if (!m?.[1] || !validApp(m[1])) return null;
  let route = (m[2] ?? "/") + search + hash;
  if (route.startsWith("?") || route.startsWith("#")) route = `/${route}`;
  return { app: m[1], route };
}

/** titleFor is the page's title for an app's route title. */
export function titleFor(routeTitle: string, appName: string): string {
  return routeTitle ? `${routeTitle} — ${appName}` : appName;
}

/**
 * filterApps keeps the apps whose name matches query, case-insensitively:
 * names that start with it first, then names that contain it, each in
 * rail order.
 */
export function filterApps(apps: readonly RailApp[], query: string): RailApp[] {
  const q = query.trim().toLowerCase();
  if (!q) return [...apps];
  const starts = apps.filter((a) => a.name.toLowerCase().startsWith(q));
  const contains = apps.filter((a) => !a.name.toLowerCase().startsWith(q) && a.name.toLowerCase().includes(q));
  return [...starts, ...contains];
}

/** firstApp is the app "/" opens: the last one shown, if it is installed, else the first enabled one. */
export function firstApp(apps: readonly RailApp[], last: string | null): RailApp | undefined {
  return (
    apps.find((a) => a.id === last && a.state !== "disabled") ?? apps.find((a) => a.state !== "disabled") ?? apps[0]
  );
}
