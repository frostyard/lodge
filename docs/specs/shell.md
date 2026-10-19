# Spec: the shell

What lodge's shell page draws and does: the rail, the switcher and its
shortcut, the frames, the address bar and history, app states, the theme
and the safe area. Its consumers:

- `shell/`, which implements it;
- every app, which learns from it what the shell around its frame does.

The messages it exchanges with frames are
[lodge-client.md](lodge-client.md), and its routes and headers are
[lodge-http.md](lodge-http.md#shell-routes). Rationale:
[ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md) (frames
and the switcher),
[ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md) (the home
screen). Where it fits:
[design/overview.md](../design/overview.md#the-shell).

**Status:** written on 2026-10-18 for the
[roadmap's](../plans/0001-roadmap.md) Phase 2. The narrow layout's bar is
provisional until it is tried on the user's phone.

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
  recreates it at the app's last route when it is next shown.
- **On a switch,** the shell shows the frame, sends `visible` to the frames
  that changed, and moves focus into the shown frame.
- **Unresponsive or down.** A frame that sends no `ready` within 10 s of
  `load` is shown as not responding, with a Reload button. An app whose
  state is `down` keeps its frame under a notice, which offers to reload
  the frame once the app is up again.

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

### The companion (reserved)

An app whose manifest sets `companion: true` may later open as a panel
beside the current app, in a frame of its own that receives the current
app's `context`. Its layout and keys are for the roadmap's Phase 5.

## Rules

- The shell has no session of its own and changes nothing on lodge. It
  reads `/lodge/apps` and serves frames.
- The shell's own local storage keys start with `lodge:`.
- A frame is never unloaded on purpose while the page lives.
- The switcher's default shortcut collides with none of the ruled-out keys
  above. A test lists them.

## References

- Rationale: [ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md), [ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md)
- Context: [design/overview.md](../design/overview.md#the-shell)
- Related: [lodge-client.md](lodge-client.md), [lodge-http.md](lodge-http.md#shell-routes), [app-contract.md](app-contract.md#public-urls)
- Built in: [roadmap — Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app)
