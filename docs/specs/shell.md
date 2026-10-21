# Spec: the shell

What lodge's shell page draws and does: the rail, the switcher and its
shortcut, the frames, the address bar and history, app states, the theme,
the safe area and the companion. Its consumers:

- `shell/`, which implements it;
- every app, which learns from it what the shell around its frame does.

The messages it exchanges with frames are
[lodge-client.md](lodge-client.md), and its routes and headers are
[lodge-http.md](lodge-http.md#shell-routes). Rationale:
[ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md) (frames
and the switcher),
[ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md) (the home
screen),
[ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md) (the
companion). Where it fits:
[design/overview.md](../design/overview.md#the-shell).

**Status:** written on 2026-10-18 for the
[roadmap's](../plans/0001-roadmap.md) Phase 2. The narrow layout's bar is
provisional until it is tried on the user's phone.
[The companion](#the-companion) was added on 2026-10-19
([ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md)),
and built in the roadmap's
[Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket).

## Interface

### Layout

| Width | Chrome | The app's frame |
| --- | --- | --- |
| 600 CSS pixels and wider | a rail at the left edge, 48 pixels wide: one button per installed app (icon, its state, its name as a tooltip), then lodge's own state | fills the rest |
| narrower than 600 | a bar at the top, 36 pixels high: the current app's icon and name, which opens the switcher on a tap, and lodge's state | fills the rest |

- Apps in the rail and the switcher are ordered by `name`.
- A disabled app is shown greyed, and opens a panel with its reason
  instead of a frame.
- The page sets `<meta name="viewport" content="width=device-width,
  initial-scale=1, viewport-fit=cover">`. The shell pads its layout by
  `env(safe-area-inset-*)`, so every frame lies inside the safe area, and
  apps need no insets of their own.

### Colours and type

The shell draws in the look its apps draw in, `@frostyard/lodge-ui`
([lodge ADR-0018](../adr/0018-a-ui-package-beside-the-client.md),
[ui-package.md](ui-package.md)). It imports the package's `tokens.css` and
`base.css`, and Inter at 15px (the apps use 13px; the shell keeps the
larger size it had).

The shell's own names map onto the tokens in one block, and the tokens
resolve the light or dark value from the `data-theme` the shell sets
([Theme](#theme)):

| The shell's name | The token | Where it draws |
| --- | --- | --- |
| `--bg` | `--bg-window` | frames, the current app's button, the covering notice |
| `--chrome` | `--bg-sidebar` | the rail, the bar, the page's background |
| `--fg` | `--text-primary` | text |
| `--muted` | `--text-secondary` | names, secondary text |
| `--line` | `--separator` | hairlines, hover |
| `--accent` | `--accent` | the current app's bar, focus rings, lodge's state |
| `--amber` | `--flag-2` | a degraded app's mark |
| `--red` | `--flag-1` | a stopped app's mark |
| `--grey` | `--text-tertiary` | a disabled app's mark |
| `--panel` | `--bg-window` | the switcher, the menu, notices |
| `--shadow` | `--popover-shadow` | the switcher and the menu |

- **The manifest names the same colours**, as ADR-0008 requires:
  `background_color` is the window's background (`#ffffff`), and
  `theme_color` the chrome (`#f2f2f4`).
- **The home-screen icons** are drawn from the accent (`#007aff`) and the
  window's background (`#ffffff`), by `scripts/icons.mjs`.

### The switcher

- **It opens on its shortcut,** by default **Ctrl+\`**: Ctrl with the key
  left of `1`, written `Ctrl+Backquote` (`KeyboardEvent.code`).
  - The shortcut works in the shell itself and inside a frame, where
    lodge-client catches it and posts `shortcut`
    ([lodge-client.md](lodge-client.md#the-shortcut)).
  - A tap on the narrow bar opens it too, and so does an app's
    `openSwitcher()`.
  - The shortcut is a shell setting, kept in local storage
    (`lodge:shortcut`), and is sent to every frame in `init`.
- **Ruled out as defaults:**
  - Ctrl+Space, which is notes' completion (CodeMirror);
  - Ctrl+;, which adds a property in notes;
  - Obsidian's default hotkeys (frostnotes' `ui.md`, its keyboard map);
  - the browser's tab keys;
  - Alt+1 to 9, which Firefox on Linux uses for tabs.
- **What it shows:** a list of the installed apps (icon, name, the
  manifest's `key`, state), with the current app marked and a filter
  field.
- **Keys while it is open:**

  | Key | Does |
  | --- | --- |
  | an app's `key`, as the first key pressed | opens that app at once |
  | any other printable key | starts the filter, which matches names |
  | Up, Down | moves the selection |
  | Enter | opens the selected app |
  | the shortcut again | opens the app shown before the current one |
  | Escape | closes the switcher, and focus returns to the frame |

  So the shortcut followed by `n` opens notes. There is no separate chord
  per app.

### Frames

- **One frame per opened app.** The shell creates an `<iframe>` with `src`
  `/app/<id>/<route>`, `title` the app's name, and
  `referrerpolicy="no-referrer"`. It has no `sandbox`, since the apps are
  first-party and share the origin.
- **Frames stay loaded.** A frame that is not shown gets `hidden` and
  `inert`, and keeps its document. If the browser discards it, the shell
  recreates it at the app's last route when it is next shown: a frame that
  sends `ready` again is sent `navigate` to its last route.
- **On a switch,** the shell shows the frame, sends `visible` to the frames
  that changed, and moves focus into the shown frame.
- **Unresponsive or down.** A frame that sends no `ready` within 10 s of
  `load` is shown as not responding, with a Reload button. An app whose
  state is `down` keeps its frame under a notice, which offers to reload
  the frame once the app is up again.
- **Content frames (a known limitation).** A frame an app opens on the
  content origin ([lodge-http.md](lodge-http.md#content-routes)) is
  cross-origin to the app, and sandboxed without scripts. The app cannot
  size it by its content, and keys pressed inside it, the switcher's
  shortcut included, never reach lodge-client. The user clicks outside the
  content frame first.

### Addresses and history

- **On load,** the shell reads `location`:
  - `/` opens the app last shown (`lodge:last` in local storage), or the
    first app;
  - `/<id>` and `/<id>/<route>` open app `<id>` at that route, with its
    query and fragment.
- **The address bar always shows the canonical URL,** `/<id>/<route>`.
- **History:**
  - `route` messages push entries, or replace the current one when
    `replace` is set;
  - a switch through the rail or the switcher pushes the new app's current
    route;
  - Back and Forward show the entry's app and send it `navigate`.
- **The title** is `<route title> — <app name>`.

### App states

- The shell reads `GET /lodge/apps` at load, every 15 s while the page is
  visible, and when it becomes visible
  ([lodge-http.md](lodge-http.md#shell-routes)).
- States show as a mark on the app's button: none for `up`, amber for
  `degraded`, red for `down`, grey for `disabled`.
- When `/lodge/apps` fails, lodge itself is out of reach. The shell says so
  in the rail or the bar and keeps polling. Frames stay as they are.

### Theme

- The theme follows `prefers-color-scheme`. The shell's menu can fix it to
  light or dark (`lodge:theme` in local storage).
- It is sent in `init`, and in `theme` whenever it changes.

### The companion

One app at most is the companion: the enabled app whose manifest sets
`companion: true` ([app-contract.md](app-contract.md#the-manifest-appjson)),
which `/lodge/apps` marks with `companion`. It opens beside whatever app is
current, and is told what that app shows.

- **Its frame.** The companion has a frame of its own, apart from the one
  it has when it is opened in full: a second `<iframe>` of the companion
  app at `/app/<id>/`, with the attributes of [any frame](#frames) and the
  `title` `<name> (companion)`.
  - It is created the first time the companion opens, and stays loaded
    after, hidden and inert while closed. A discarded one is recreated at
    `/app/<id>/`. When a reload makes another app the companion, or none,
    the old one's frame is removed.
  - Its `init` says `role: "companion"`
    ([lodge-client.md](lodge-client.md#the-companion)). Every other frame's
    says `"app"`.
- **Layout:**

  | Width | The companion | The current app's frame |
  | --- | --- | --- |
  | 600 CSS pixels and wider | a panel at the right edge, the full height, 400 pixels wide or half the window, whichever is less, with a 1-pixel border on its left | fills the rest, between the rail and the panel |
  | narrower than 600 | a sheet over the frame and the bar, the full height and width inside the safe area, with a handle 24 pixels high at its top | stays loaded beneath it, `inert` while the sheet is up |

  - **The sheet closes** on a tap on its handle, or a drag of the handle
    down by more than 80 pixels. A shorter drag springs back.
  - With the panel open, the current app's frame is still the one shown,
    and keeps `visible: true`.
- **Opening and closing:**
  - **A button for the companion,** with its icon and its name as a
    tooltip, sits on the rail under the apps and above lodge's state, and
    on the narrow bar before lodge's state. It shows whether the companion
    is open, and toggles it.
  - **Its chord,** by default **Ctrl+Shift+\`** (`Ctrl+Shift+Backquote`),
    toggles it too: in the shell, in the switcher, and inside a frame,
    where lodge-client catches it and posts `shortcut` with this chord. It
    is a shell setting, set in the shell's menu beside the switcher's and
    kept in local storage (`lodge:companionShortcut`), and is sent to every
    frame in `init.companion`. Neither chord may be the other.
  - An app's `ask` opens it ([below](#the-companions-messages)).
  - Opening moves focus into the companion's frame, which is sent `visible
    {visible: true}`. Closing returns focus to the current app's frame, and
    sends the companion's `visible {visible: false}`.
  - **Whether it is open** is kept in local storage (`lodge:companion`). On
    load, the shell opens it again from 600 pixels wide. A sheet always
    starts closed, so a link opened on a phone shows its app.
  - Opening and closing make no history entry, and the address bar keeps
    the current app's canonical URL.
- **The companion app in full.** The switcher and the companion app's
  `key` open it as any app, in its own frame. While it is the current app,
  the panel and the sheet are hidden, since they would show the same thread
  twice, and its button and chord open the app shown before it, with the
  companion open beside it. When another app becomes current, the
  companion returns if it was open.

### The companion's messages

The messages are [lodge-client.md's](lodge-client.md#the-companion).

- **Context.** The shell keeps each app's latest `context`. While the
  companion is open beside another app, it sends the companion's frame
  `context` with the current app's ID:
  - when the companion opens;
  - when the current app reports a new context;
  - when another app becomes current, with that app's latest, or with
    `kind: null` and empty `ref` and `title` when it reported none.

  While the companion is closed, nothing is sent, and opening sends the
  latest.
- **`ask`.** The shell takes `ask` only from the current app's frame, and
  only when there is a companion and the companion app is not the current
  app. It takes at most one a second, and drops the rest. It opens the
  companion, which opening sends the current `context`, and focuses it,
  then sends it `ask` with the current app's ID and the prompt. A frame
  created for the ask is sent it once it is ready.
- **What the shell takes from the companion's frame:** `ready`, `open` and
  `shortcut`, as from any frame. It ignores the companion frame's `route`,
  `context` and `ask`: its routes are not the page's address, and it is not
  the app any context is about. An `open` of a canonical URL makes that app
  current, beside the open companion.

## Rules

- The shell has no session of its own and changes nothing on lodge. It
  reads `/lodge/apps` and serves frames.
- The shell's own local storage keys start with `lodge:`.
- A frame is never unloaded on purpose while the page lives.
- The switcher's default shortcut collides with none of the ruled-out keys
  above. A test lists them.
- The companion's default chord collides with none of the ruled-out keys,
  nor with the switcher's. The same test lists it.
- The companion frame never changes the address bar or history, and its
  messages never move the current app.

## References

- Rationale: [ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md), [ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md), [ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md) (the companion)
- Context: [design/overview.md](../design/overview.md#the-shell)
- Related: [lodge-client.md](lodge-client.md), [lodge-http.md](lodge-http.md#shell-routes), [app-contract.md](app-contract.md#public-urls)
- Built in: [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app); the companion in [Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket)
