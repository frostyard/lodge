# 0013 — The companion opens beside the current app

- **Status:** Accepted (2026-10-21)
- **Date:** 2026-10-19

## Context

[ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md) reserved a
*companion*: an app whose manifest sets `companion: true` may open as a
panel beside the current app, and the shell forwards it the current app's
`context`. It left the layout, the keys and the messages to the roadmap's
Phase 5, the assistant's design.

On 2026-10-19 the user decided the assistant:

- **It is Porter,** app `porter`, user `frostporter`, in its own repository
  ([Porter ADR-0003](https://github.com/frostyard/porter/blob/main/docs/adr/0003-porter-is-a-lodge-app-that-owns-no-data.md)).
- **Its chat panel is its main surface.** The full app shows the same
  thread at full width, with its activity and approvals.
- **"Chat inside an app" is that panel,** opened beside whatever app is
  current, with that app's context. There are no chat panels per app.

Constraints from what lodge already is:

- Frames stay loaded, and moving an `<iframe>` in the page reloads it
  ([shell.md](../specs/shell.md#frames)).
- On a window under 600 CSS pixels wide, the shell draws a bar, not a rail
  ([shell.md](../specs/shell.md#layout)).
- Every key an app might use is ruled out for lodge's own chords, and the
  switcher's is **Ctrl+\`**
  ([shell.md](../specs/shell.md#the-switcher)).
- Apps already report `context`, `{kind, ref, title}`, a pointer to what is
  on screen ([lodge-client.md](../specs/lodge-client.md#context)).

## Decision

- **One app is the companion:** the enabled app whose manifest sets
  `companion: true`. When two manifests set it, neither is the companion;
  both stay enabled, and `lodgectl apps` says why, as for a shared `key`.
- **The companion is a frame of its own,** beside the current app's frame,
  at `/app/<id>/`. It is created the first time it opens and stays loaded
  after, like every frame. It is a second frame of the companion app, apart
  from the frame the app has when it is opened in full.
- **The layout:**
  - **From 600 CSS pixels wide,** a panel on the right, **400 pixels** wide
    and never more than half the window. The current app's frame narrows
    beside it.
  - **Under 600,** a full-height sheet over the current app, with a handle
    at its top. A tap on the handle, or a swipe down on it, dismisses it.
    The app underneath stays loaded, and is inert while the sheet is up.
- **It toggles from any app:**
  - with a button for the companion on the rail and on the narrow bar,
    under the apps and above lodge's own state;
  - with its own chord, by default **Ctrl+Shift+\`**
    (`Ctrl+Shift+Backquote`), the switcher's key with Shift. It collides
    with none of shell.md's ruled-out keys, and inside a frame lodge-client
    catches it as it catches the switcher's.
  - Whether it is open is remembered per browser (`lodge:companion`).
- **The switcher and the companion app's `key` open it in full,** as any
  app. While the companion app is the current app, the panel is hidden,
  since it would show the same thread twice; it returns when another app is
  current.
- **The shell forwards the current app's `context`** to the companion: when
  the panel opens, whenever the current app reports a new one, and when
  another app becomes current, with `kind: null` when that app reported
  none. The context is a pointer. The companion fetches what it points at
  with its own credentials, and so sees only what its scopes allow
  ([ADR-0014](0014-agents-on-lodges-host-use-a-local-agent-socket.md)).
- **A new message, `ask {prompt}`,** lets an app's "Ask about this" open the
  companion with a prompt. The shell takes it only from the frame that is
  shown, at most once a second, opens the panel, and forwards `ask` with the
  current context. What the companion does with the prompt (filling its
  composer, or sending it) is the companion's own choice.
- **What the companion frame may send:** `open`, `shortcut` and `ready`, as
  any app. The shell ignores its `route`, `context` and `ask`: its routes
  are not the page's address, and it is not the app the context is about.
- **`init` grows two fields,** additively in contract 1: `role` (`"app"` or
  `"companion"`) and `companion` (the panel's chord, so lodge-client
  catches it in every frame).
- [specs/shell.md](../specs/shell.md#the-companion) pins the layout and
  keys, and [specs/lodge-client.md](../specs/lodge-client.md#the-companion)
  the messages. The roadmap's
  [Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket)
  builds them.

## Consequences

- One assistant serves every app, with each app's context, and an app adds
  "Ask about this" with one message rather than a chat of its own.
- **Apps must work narrower.** With the panel open, a desktop app's frame
  loses 400 pixels. Notes and mail already work at a phone's width, so this
  costs layout testing rather than new layouts.
- **The companion app runs twice when both are open,** as the panel and in
  full: two frames, two `/api` sessions within the per-node bound
  ([ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md)), one
  thread on the daemon's side.
- **One more chord apps must not bind.** Ctrl+Shift+\` joins the
  switcher's, and both reach lodge-client before an app sees them.
- The panel shares the origin with every app
  ([ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md)), so the
  protocol, not the browser, is what keeps the companion to pointers.
  That rests, as before, on every app being first-party and every policy
  forbidding inline script.
- On a phone the sheet covers the app it is about. The context chip in the
  companion says what that was.

## Alternatives considered

- **Chat panels per app:** every app would build its own chat, its own
  model context and its own tools, and a question spanning mail and notes
  would have no home. Porter's design ruled it out too.
- **The assistant only as a full app:** switching away from the note one is
  asking about loses the side-by-side that "ask about this" needs.
- **Moving the full app's frame into the panel:** moving an `<iframe>`
  reloads it, which loses the thread's scroll and any half-written prompt.
- **A floating or draggable window:** ruled out by the suite decision in
  [ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md).
- **Other chords:** Alt+\` switches an application's windows on GNOME,
  Ctrl+J opens the browser's downloads, and Ctrl+Space and the Obsidian
  keys are notes'. Ctrl+Shift+\` is free, and pairs with the switcher.

## References

- Shapes: [specs/shell.md](../specs/shell.md#the-companion), [specs/lodge-client.md](../specs/lodge-client.md#the-companion), [specs/app-contract.md](../specs/app-contract.md#the-manifest-appjson), [design/overview.md](../design/overview.md#the-shell), [roadmap — Phase 5](../plans/0001-roadmap.md#phase-5--the-assistants-design) and [Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket)
- Builds on: [ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md), [ADR-0008](0008-lodge-installs-as-one-home-screen-app.md) (one home-screen app)
- Related: [ADR-0014](0014-agents-on-lodges-host-use-a-local-agent-socket.md); Porter [ADR-0003](https://github.com/frostyard/porter/blob/main/docs/adr/0003-porter-is-a-lodge-app-that-owns-no-data.md), [ADR-0006](https://github.com/frostyard/porter/blob/main/docs/adr/0006-one-continuous-thread.md) (one continuous thread, in the panel and in full) and [design/overview.md](https://github.com/frostyard/porter/blob/main/docs/design/overview.md); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
