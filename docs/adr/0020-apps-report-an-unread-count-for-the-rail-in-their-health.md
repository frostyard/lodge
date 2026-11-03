# 0020 — Apps report an unread count for the rail in their health

- **Status:** Proposed
- **Date:** 2026-11-02

## Context

The roadmap keeps "badges in the rail, such as unread mail" among its
[later ideas](../plans/0001-roadmap.md#later--ideas). The user wants to see
new mail, or new articles, without opening the app. The UI package already
has badge tokens (`--badge-bg`, `--badge-text`;
[ui-package.md](../specs/ui-package.md#tokens)), and nothing uses them.

Facts and constraints from what lodge already is:

- **The shell makes a frame only for an app the user opened**
  ([shell.md](../specs/shell.md#frames)). An app that was never opened in
  this page has no frame and no lodge-client, so it cannot tell the shell
  anything. The count must reach the shell through lodge's server.
- **lodge already asks every enabled app** for its state: `GET /health` on
  the app's socket, every 15 s, with a 2 s timeout
  ([app-contract.md](../specs/app-contract.md#what-an-app-serves-on-its-socket)).
  The shell already reads that state from `GET /lodge/apps` every 15 s
  while it is visible ([shell.md](../specs/shell.md#app-states)).
- **`/health` admits any caller, or none,** and carries no identity. lodge
  is one owner's shell: every allowed login opens the same apps on the same
  data ([ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md)).
- **Within contract major 1, changes are additive only**
  ([app-contract.md](../specs/app-contract.md#versioning)). An app on an
  older kit, and an older lodge, must keep working.
- **lodge installs as one home-screen app**
  ([ADR-0008](0008-lodge-installs-as-one-home-screen-app.md)), and
  registers no service worker. The Badging API,
  `navigator.setAppBadge(n)`, sets a count on an installed app's icon, where
  the browser and the platform support it.

## Decision

- **An app MAY add one optional member to its `/health` answer,** `badge`:
  a whole number from 0, the count of things the user has not seen, such
  as unread messages. 0, or no member, is no badge.

  ```json
  { "ok": true, "version": "0.9.0", "protocol": 1, "badge": 3 }
  ```

- **The count is the app's, never a caller's.** `/health` carries no
  identity, and lodge shows the one count to every session. An app whose
  data differs by login does not set `badge`.
- **lodge reads it with the state it already polls.** Only an `up` answer's
  badge counts; a degraded, down or disabled app has none, and its state
  mark shows instead. lodge ignores a `badge` that is not a whole number
  from 0, and caps it at 99999. A bad badge never makes an app degraded or
  down.
- **`/lodge/apps` gives each app a `badge`,** 0 when it has none
  ([lodge-http.md](../specs/lodge-http.md#shell-routes)).
- **The shell draws it** ([shell.md](../specs/shell.md#badges)):
  - a count on the app's rail button, and in its row of the switcher, in
    the badge tokens, written `99+` above 99;
  - on the narrow bar, the sum of the counts of the apps that are not the
    current one, since that is what a tap on the bar leads to;
  - each button's accessible name and tooltip carry the count.
- **The home-screen icon shows the total.** The shell calls
  `navigator.setAppBadge(total)` with the sum of every app's count, and
  `navigator.clearAppBadge()` when the sum is 0. It calls them only when
  the sum changes, only where the browser has them, and ignores their
  failures.
- **The kit carries the member.** `lodgeapp.Health` gains `Badge`
  (`badge`, omitted at 0), so an app daemon sets it from its `Health`
  function. lodge reads the same member. The kit's conformance checks
  (`kittest`) refuse a negative badge.
- [specs/app-contract.md](../specs/app-contract.md#what-an-app-serves-on-its-socket)
  pins the member, [specs/lodge-http.md](../specs/lodge-http.md#shell-routes)
  the rail's field, and [specs/shell.md](../specs/shell.md#badges) the
  drawing.

## Consequences

- An app shows its unread count in the rail with one member in an answer it
  already gives. lodge gets no new request, socket, caller kind or poll.
- **The count lags by up to about 30 s:** lodge's 15 s poll, then the
  shell's. After the user reads a message, its app's count falls on a later
  poll, not at once.
- **The home-screen badge updates only while lodge is open.** With no
  service worker, nothing updates it in the background, so a closed lodge
  keeps the last total until it opens again. Browsers without the Badging
  API show no home-screen count; the rail still does.
- **The kit changes,** additively. An app can set `badge` only after Brian
  tags a kit release with it, and the app bumps its pin in a PR of its own.
  lodge itself builds on its own `kit/`, so it reads the member at once.
- An app on an older kit sends no `badge` and shows none. An older lodge
  ignores the member, since lodge and apps ignore what they do not know.
- Each app now computes its count on every `/health`, every 15 s. An app
  keeps that cheap: a count it already holds, or one indexed query, inside
  the poll's 2 s timeout.
- The count is visible to every allowed login. That is true of the app's
  data already, for this one owner's shell, and the rule against a
  per-caller count keeps it so.

## Alternatives considered

- **A new socket path, such as `GET /badge`:** it needs a new caller kind
  for lodge's own requests, a second poll, and a new row in the socket's
  table, for one number that `/health` can carry.
- **A lodge-client message from the app's frame:** a frame exists only
  after the user opens the app, so an app never opened shows no count,
  which is most of the point. It may come later as a faster path for an
  open frame, beside this one.
- **Apps push their counts to lodge:** it needs a socket of lodge's that
  every app dials, and a writer's identity on it, where today lodge only
  dials apps.
- **A dot without a number:** it tells less, and the badge tokens and the
  Badging API both take a count.
- **A service worker for the home-screen badge:** it would keep the icon's
  count fresh while lodge is closed, but
  [ADR-0008](0008-lodge-installs-as-one-home-screen-app.md) registers none,
  and a push from lodge would need a public push service.

## References

- Shapes: [specs/app-contract.md](../specs/app-contract.md#what-an-app-serves-on-its-socket), [specs/lodge-http.md](../specs/lodge-http.md#shell-routes), [specs/shell.md](../specs/shell.md#badges), [design/overview.md](../design/overview.md#the-shell), [roadmap — Later / ideas](../plans/0001-roadmap.md#later--ideas)
- Builds on: [ADR-0008](0008-lodge-installs-as-one-home-screen-app.md) (one home-screen app), [ADR-0009](0009-apps-switch-in-frames-that-stay-loaded.md) (the rail and frames), [ADR-0018](0018-a-ui-package-beside-the-client.md) (the badge tokens)
- Related: [ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md) (one owner, its allowed logins)
