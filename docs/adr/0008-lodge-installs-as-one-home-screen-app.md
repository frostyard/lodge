# 0008 — lodge installs as one home-screen app

- **Status:** Proposed
- **Date:** 2026-10-18

## Context

The user will open lodge on their phone, over the tailnet
([ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md)). In a
phone's browser, a page shares the screen with the address bar and the
browser's tabs. A page with a web app manifest can be added to the home
screen, and opens there without browser chrome.

frostnotes' M4 drafted this for notes alone (its draft ADR-0030, local
commit `0586e2b`): a manifest, no service worker, and a page that needs the
hub. lodge now hosts every app in one shell
([ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md)), on one
origin.

A progressive web app may also register a service worker. A service worker
intercepts the page's requests, can answer them from a cache with no
network, and stays installed between visits until it is replaced. Mail and
the assistant will want notifications while lodge is closed. On the web,
those need Web Push, and Web Push needs a service worker.

## Decision

- **lodge has one web app manifest,** `/lodge/manifest.webmanifest`,
  linked from the shell page:
  - the name Lodge and lodge's icon, at 192 and 512 pixels, plus a maskable
    one;
  - `display: standalone`, with `start_url` and `scope` both `/`;
  - theme and background colors from the shell's tokens.

  One home-screen icon opens the shell, and the switcher reaches every app.
- **Apps have no manifests of their own.** An app's `app.json` is lodge's
  install manifest, not a web manifest. lodge serves no other web manifest,
  and an app frame's policy sets `manifest-src 'none'`.
- **No service worker is registered.** Every policy sets `worker-src
  'none'`. lodge works while it is reachable, and when it is not, the shell
  is a page like any other.
- **When lodge is unreachable, the shell says so.** The rail shows lodge as
  out of reach. Open apps keep what they hold, as each app's own design
  says. For example, notes' editors keep unsaved text and save it with its
  base when lodge answers again. Nothing new loads until then.
- **Per-browser state is shared by origin,** so each app prefixes its
  local storage keys with `<id>:`. lodge-client provides the prefix
  ([specs/lodge-client.md](../specs/lodge-client.md#the-library)).
- **Web Push stays open** ([roadmap](../plans/0001-roadmap.md#open-questions)).
  A worker scoped to push alone would reverse `worker-src 'none'`. That needs
  its own ADR before mail or the assistant depends on it.

## Consequences

- On the phone, lodge opens from the home screen without the browser's
  chrome, with every app behind one icon.
- With no tailnet, the phone shows no app. Offline use waits for each
  app's own native clients. For notes, that is a mobile app with its own
  copy of the vault.
- No script stays installed between visits. An injected script cannot
  persist through a worker, and a reload always runs the bundles lodge
  serves.
- Notifications while lodge is closed are not possible until Web Push is
  decided.
- Browsers differ in what Add to Home Screen makes of a page without a
  service worker. The roadmap's Phase 2 checks on the user's phone that
  lodge opens standalone.

## Alternatives considered

- **A manifest per app:** several home-screen icons on one origin and one
  scope, fighting over `start_url`. The switcher already moves between
  apps.
- **A service worker that caches the shell and the bundles:** lodge would
  start offline with nothing to show. A cached bundle could also lag behind
  its daemon's protocol, and a worker is a script that outlives the page.
- **No manifest:** lodge works in a tab, but on a phone the address bar and
  the tab strip take space from every app.

## References

- Shapes: [specs/lodge-http.md](../specs/lodge-http.md#shell-routes), [specs/shell.md](../specs/shell.md), [design/overview.md](../design/overview.md#the-shell), [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app)
- Builds on: [ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md), [ADR-0007](0007-lodge-serves-every-apps-web-build-from-disk.md); frostnotes' M4 draft ADR-0030 (local commit `0586e2b`)
- Related: [ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md)
