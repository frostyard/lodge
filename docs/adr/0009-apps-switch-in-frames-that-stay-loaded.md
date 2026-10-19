# 0009 — Apps switch in frames that stay loaded

- **Status:** Proposed
- **Date:** 2026-10-18

## Context

lodge hosts several apps behind one address
([ADR-0003](0003-lodge-is-the-only-process-on-the-network.md)): notes and
mail first, then the assistant. Each app's frontend is its own bundle,
built in its own repository
([ADR-0007](0007-lodge-serves-every-apps-web-build-from-disk.md)). Notes
and mail are long-lived pages. They hold open editors, subscriptions and
unsaved text, and are slow to start compared with switching.

On 2026-10-18 the user decided:

- **A suite, not a windowed desktop.** One web address, with a quick
  switcher or a launch rail, and no draggable windows or tabs.
- **Each app runs in a frame that stays loaded.**
- **"Chat inside an app" is the assistant,** opened as a companion panel
  beside the current app. The current app reports what is on screen.
- **Links between apps are permanent.** A note links to a mail thread, and
  Obsidian links into lodge.

## Decision

- **The shell page is lodge's own.** It is served for `/`, `/<id>` and
  `/<id>/<route>` as a top-level page. It draws the rail, the switcher
  and one frame per open app ([specs/shell.md](../specs/shell.md)).
- **An app's frame loads `/app/<id>/<route>` and stays loaded.** The shell
  creates it when the app is first opened, and hides it, without unloading
  it, when another app is shown. Switching back shows the same page in the
  same state. Frames live as long as the shell page.
- **`/<id>/<route>` is the canonical URL** of anything in an app, and it is
  permanent: an app keeps answering its routes across versions. The shell
  keeps the address bar on the canonical form and owns history. An app in
  a frame never calls `history.pushState`. It reports its route, and the
  shell navigates it.
- **The shell and the frames talk only through `@frostyard/lodge-client`,**
  by `postMessage` on the one origin, with the origin and source checked on
  both sides ([specs/lodge-client.md](../specs/lodge-client.md)).
  - On load, the shell sends `init`, with the contract, the content origin,
    the switcher's shortcut and the theme.
  - The app reports `route`, `context`, `open` and `shortcut`.
  - The shell sends `navigate`, `visible` and `theme`.
- **The switcher has one shortcut, and it is lodge's.** The default is
  **Ctrl+\`** (backtick), defined in [specs/shell.md](../specs/shell.md#the-switcher)
  and sent in `init`.
  - Apps must not bind it. Inside a frame, lodge-client catches it and
    posts `shortcut`.
  - The switcher followed by an app's manifest `key` jumps straight to that
    app. There are no separate chords per app.
  - Ruled out:
    - Ctrl+Space, which is notes' completion;
    - Ctrl+;, which adds a property in notes;
    - Obsidian's default hotkeys;
    - the browser's tab keys;
    - Alt+1 to 9, which are Firefox's tab keys on Linux.
- **The rail shows each app's state** from lodge's health polls: up, down,
  or disabled with its reason. A contract-major mismatch disables that one
  app.
- **The companion panel is reserved.** A manifest with `companion: true`
  may open beside the current app, and the shell forwards it the current
  app's `context`. The assistant's design, in the roadmap's Phase 5,
  decides the rest.
- **All apps share one origin,** `https://LODGE`. Content that is not
  trusted never loads there
  ([ADR-0010](0010-untrusted-bytes-come-from-a-second-node.md)).

## Consequences

- Switching is instant, and nothing is lost by switching: editors,
  subscriptions and scroll positions stay as they were.
- **The apps are not isolated from each other in the browser.** Frames on
  one origin can reach each other's documents and the shell's, and share
  local storage. A script that runs in one app can drive every app's
  session. lodge accepts this because every app is first-party, its code is
  root-owned ([ADR-0007](0007-lodge-serves-every-apps-web-build-from-disk.md)),
  every policy forbids inline script, and untrusted bytes come only from
  another origin. Per-app paths in `script-src` and `connect-src` narrow
  mistakes, but are not a boundary.
- Every loaded app keeps its own `/api` session while the page is visible.
  An open shell with three apps holds three sessions per tab, within the
  per-node bound ([ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md)).
- Memory grows with each app opened. A phone may discard a hidden frame,
  and the shell then reloads it when it is shown.
- Apps give up their own history handling inside lodge, and route through
  lodge-client instead.
- On a phone, the shell keeps frames inside the screen's safe area (with
  `viewport-fit=cover`), so apps need no insets of their own.

## Alternatives considered

- **A windowed desktop in the browser:** declined by the user. Windows
  inside a page fight the browser's own tabs, history and shortcuts, and do
  not work on a phone.
- **Tabs per app:** declined by the user. The switcher and the rail do the
  same with less chrome.
- **Full-page navigation between apps:** each switch would reload an app,
  and lose its editors and subscriptions.
- **One origin per app:** a tailnet node, and so a name and certificate,
  for every app. Messages and links between apps would cross origins, and
  sessions and allow-lists would multiply. The isolation it buys is between
  first-party apps.
- **One page that mounts every app's code (module federation, web
  components):** a single JavaScript realm. Shared dependency versions
  would couple the apps' releases, and one app's crash would be every
  app's.

## References

- Shapes: [specs/shell.md](../specs/shell.md), [specs/lodge-client.md](../specs/lodge-client.md), [specs/lodge-http.md](../specs/lodge-http.md#shell-routes), [specs/app-contract.md](../specs/app-contract.md#public-urls), [design/overview.md](../design/overview.md#the-shell), [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app)
- Builds on: [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md), [ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md), [ADR-0007](0007-lodge-serves-every-apps-web-build-from-disk.md)
- Related: [ADR-0008](0008-lodge-installs-as-one-home-screen-app.md), [ADR-0010](0010-untrusted-bytes-come-from-a-second-node.md); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
