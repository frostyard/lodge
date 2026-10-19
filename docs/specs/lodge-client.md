# Spec: lodge-client, the shell's protocol with app frames

The messages that pass by `postMessage` between lodge's shell page and each
app's frame, and the API of `@frostyard/lodge-client`, the library every
app bundle includes. Its consumers:

- the shell (`shell/`, [shell.md](shell.md));
- every app's frontend;
- `client/`, which implements both halves.

Rationale: [ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md)
(frames, canonical URLs, the shortcut),
[ADR-0010](../adr/0010-untrusted-bytes-come-from-a-second-node.md) (the
content origin), [ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md)
(storage on one origin),
[ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md) (the
companion). Where it fits:
[design/overview.md](../design/overview.md#the-shell).

**Status:** contract major **1**, written on 2026-10-18. It binds when the
library lands in the [roadmap's](../plans/0001-roadmap.md) Phase 1, and
the shell's half in Phase 2. [The companion's](#the-companion) additions
(`ask`, the shell's `context` to the companion, and `init`'s `role` and
`companion`) were made on 2026-10-19, additively in major 1, and built in
the roadmap's
[Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket).

## Interface

### Transport

- **One origin.** The shell is at `https://LODGE/`, and each frame at
  `https://LODGE/app/<id>/…`
  ([lodge-http.md](lodge-http.md#routes)).
  - An app sends with `window.parent.postMessage(message, location.origin)`.
  - The shell sends with `frame.contentWindow.postMessage(message,
    location.origin)`.
- **Both sides check every message.** A receiver drops a message, silently,
  unless:
  - `event.origin` equals its own `location.origin`;
  - `event.source` is the expected window: `window.parent` for an app, and
    for the shell the `contentWindow` of a frame it created. The shell
    knows a message's app by its frame, never by anything in the message.
- **The envelope** is a plain object: `{"lodge": 1, "type": "<type>",
  …fields}`. `lodge` is the contract major. A message with another major or
  an unknown `type` is dropped, so additions within a major are safe.
- **Values** are JSON values only: strings, numbers, booleans, null,
  arrays and objects. Every string is text. No side ever evaluates a
  message, or inserts one as HTML.

### Messages

| Message | Direction | Fields | Meaning |
| --- | --- | --- | --- |
| `ready` | app → shell | `contract` (int) | the app's lodge-client is listening |
| `init` | shell → app | `contract` (int), `contentOrigin` (string or null), `shortcut` (string), `theme` (`"light"` or `"dark"`); `role` (`"app"` or `"companion"`), `companion` (string or null) | the answer to `ready`, on load |
| `route` | app → shell | `path` (string), `title` (string), `replace` (bool, optional) | the app's route changed; the address bar and history follow it |
| `context` | app → shell | `kind` (string or null), `ref` (string), `title` (string) | what is on screen, for the companion: `{kind: "note", ref: "notes/1/Projects/Plan.md", title: "Plan"}` |
| `open` | app → shell | `url` (string) | open a URL, possibly in another app |
| `shortcut` | app → shell | `key` (string) | the switcher's shortcut, or the companion's, was pressed inside the frame, or the app asked for the switcher |
| `ask` | app → shell | `prompt` (string) | open the companion with a prompt about what is on screen: an app's "Ask about this" |
| `navigate` | shell → app | `route` (string) | go to this route: a deep link, or history, into an app already loaded |
| `visible` | shell → app | `visible` (bool) | the frame was shown or hidden |
| `theme` | shell → app | `scheme` (`"light"` or `"dark"`) | the theme changed |
| `context` | shell → companion | `app` (string), `kind`, `ref`, `title` | the current app's context, for the frame opened as the companion |
| `ask` | shell → companion | `app` (string), `prompt` (string) | the current app's `ask`, after its `context` |

### Start

1. The shell creates the frame at `/app/<id>/<route>`
   ([shell.md](shell.md#frames)).
2. The app calls `connectLodge()`, and lodge-client posts `ready
   {contract: 1}`.
3. The shell answers with `init`, then `visible`.
   - `init.contract` is the shell's major.
   - `contentOrigin` is `https://CONTENT` for an app whose manifest sets
     `content`, else `null`.
   - `shortcut` is the switcher's chord, by default `Ctrl+Backquote`
     ([shell.md](shell.md#the-switcher)).
   - `theme` is the current scheme.
   - `role` is `"companion"` in the frame opened as the
     companion, else `"app"`. `companion` is the companion's chord, by
     default `Ctrl+Shift+Backquote`, or `null` when no app is the companion
     ([the companion](#the-companion)).

   `connectLodge()` resolves on `init`.
4. If `ready.contract` is not a major the shell speaks, the shell sends no
   `init`. It marks the app as mismatched, ignores the frame's messages,
   and shows why.
5. The shell queues `navigate` messages until `init` has gone. It waits up
   to 10 s after the frame's `load` event for `ready`, and then shows the
   app as not responding.

### Routes

- A route (`route.path`, `navigate.route`) starts with `/`, is relative to
  the app's root, may carry `?query` and `#fragment`, and is at most 2,048
  characters.
  - The app's canonical URL is `/<id>` followed by the route, with `/` alone
    giving `/<id>`.
  - The frame's URL is `/app/<id>` followed by the route.
  - Notes' routes, for example, are `/<vault id>/<path>`, so
    `/notes/1/Projects/Plan.md`.
- On `route`, the shell shows the canonical URL in the address bar,
  pushing a history entry, or replacing the current one when `replace` is
  true. It titles the page `<title> — <app name>`, or the app's name when
  `title` is empty. Titles are at most 200 characters. A `route` equal to
  the app's current entry changes nothing, so an app may report the route it
  was navigated to.
- **An app in a frame never makes history entries or moves itself.** It
  MUST NOT call `history.pushState` or `replaceState`, or assign
  `location`, to navigate. It reports `route` and obeys `navigate`.
  `lodge.history` gives client-side routers a history driven that way.
- **An app MAY call `history.back()`** to undo an entry it pushed itself
  with `route`, such as a Back button in its own UI (mail's narrow layout).
  The browser walks the shell's history, and the shell answers with
  `navigate`. An app MUST NOT call it from a route it was opened at, where
  the entry before may not be lodge's.

### `open`

`url` is one of:

- **a canonical lodge URL,** `/<id>/<route>` or `https://LODGE/<id>/<route>`.
  The shell opens app `<id>` at `<route>`, loading it if needed, and pushes
  a history entry. Paths under `/app/`, `/lodge/`, `/mcp/` and `/device/`
  are refused.
- **a content URL** on `https://CONTENT`. The shell opens it in a new tab.
- **any other `https:`, `http:` or `mailto:` URL.** The shell opens it in a
  new browsing context with `noopener` and `noreferrer`.

Anything else is ignored. A new tab opens only within the user activation
of the input that caused it, so an app sends `open` from that input's
handler.

### `context`

- `kind` is at most 32 characters of `[a-z-]`, or `null` to say nothing is
  open. `ref` is at most 1,024 characters, in a form each app defines, and
  `title` is at most 200.
- The shell keeps the latest per app. It forwards the current app's
  context to the frame opened as the companion
  ([the companion](#the-companion)).

### The shortcut

- `init.shortcut` is a chord: zero or more modifiers from `Ctrl`, `Alt`,
  `Shift` and `Meta`, joined by `+`, then a `KeyboardEvent.code` value. The
  default is `Ctrl+Backquote`, the key left of `1` on a US layout, with
  Ctrl held.
- lodge-client listens to `keydown` on the frame's window, in the capture
  phase. When an event's `code` and modifiers match the chord exactly, and
  it is not part of a composition (`isComposing`), lodge-client calls
  `preventDefault()` and `stopPropagation()`, and posts `shortcut {key:
  chord}`. The app never sees the event.
- An app MUST NOT bind the chord. `lodge.openSwitcher()` posts the same
  message, for an app's own menu or touch button.
- lodge-client catches `init.companion`'s chord the same way,
  and posts `shortcut {key: chord}` with it. The shell tells the two apart
  by `key`. An app MUST NOT bind it either.

### The companion

The companion is the one app whose
manifest sets `companion: true`, opened in a frame of its own beside the
current app ([shell.md](shell.md#the-companion)).

- **Roles.** `init.role` is `"companion"` in that frame, and `"app"` in
  every other. One app can run in both at once, the companion and the app
  opened in full: two documents, each with its own `/api` session.
- **`ask`, app → shell.** `prompt` is 1 to 4,000 characters, or the message
  is dropped. An app sends it from the handler of the input that asked,
  after reporting the `context` it is about. The shell takes it only from
  the current app's frame, at most once a second, and only when there is a
  companion ([shell.md](shell.md#the-companions-messages)). An app SHOULD
  offer "Ask about this" only when `init.companion` is not `null`.
- **`context`, shell → companion.** `app` is the current app's ID, and
  `kind`, `ref` and `title` are its latest `context`. When it reported none,
  `kind` is `null`, and `ref` and `title` are empty. The shell sends it
  while the companion is open: when it opens, when the current app reports
  a new context, and when another app becomes current.
- **`ask`, shell → companion.** `app` and `prompt`, sent after the `context`
  they are about. What the companion does with the prompt, such as filling
  its composer or sending it, is its own choice.
- **In the companion's frame:**
  - the shell takes `ready`, `open` and `shortcut`, and ignores `route`,
    `context` and `ask`;
  - the shell sends no `navigate`, so `lodge.history` keeps the frame's
    routes in memory: `push` and `replace` move it, and post nothing;
  - `visible` says whether the companion is open.
- **The context is a pointer,** never content. The companion reads what
  `ref` points at with its own credentials, through that app's
  `/mcp/<id>` and its own token
  ([app-contract.md](app-contract.md#the-agent-socket)), so it sees only
  what its scopes allow. No frame is sent another frame's content.

### Visibility and theme

- `visible` says whether this frame is the one shown, or the open
  companion. A current app's frame stays visible while the companion is
  open beside it or over it. A hidden frame stays loaded and keeps its sessions. An app
  MAY pause work while hidden.
- **Keepalive follows the page, not the frame.** An app's socket traffic
  that keeps its session alive follows `document.visibilityState`
  ([app-contract.md](app-contract.md#sessions)).
- An app SHOULD follow `theme` rather than its own
  `prefers-color-scheme` query.

### The library

`@frostyard/lodge-client`, in `client/`: ESM, written in TypeScript, with
no runtime dependencies.

```ts
import { connectLodge } from "@frostyard/lodge-client";

const lodge = await connectLodge({ app: "notes" }); // posts ready, resolves on init
lodge.contract;                       // 1
lodge.theme;                          // "light" | "dark"
lodge.shortcut;                       // "Ctrl+Backquote"
lodge.apiURL();                       // "wss://LODGE/app/notes/api"
lodge.contentURL("blobs/<mac>/<hash>.png"); // "https://CONTENT/notes/blobs/<mac>/<hash>.png"
lodge.route("/1/Projects/Plan.md", "Plan");
lodge.context({ kind: "note", ref: "notes/1/Projects/Plan.md", title: "Plan" });
lodge.open("/mail/message/abc%40example.com"); // a mail by its Message-ID
lodge.openSwitcher();
lodge.storageKey("workspace");        // "notes:workspace"
lodge.on("navigate", ({ route }) => {});
lodge.on("visible", ({ visible }) => {});
lodge.on("theme", ({ scheme }) => {});
lodge.history;                        // location, push, replace, listen

// The companion
lodge.role;                           // "app" | "companion"
lodge.companion;                      // "Ctrl+Shift+Backquote", or null without a companion
lodge.ask("What is due here?");       // opens the companion with a prompt
lodge.on("context", ({ app, kind, ref, title }) => {}); // in the companion
lodge.on("ask", ({ app, prompt }) => {});               // in the companion
```

- **Outside a frame,** where `window.parent === window`, `connectLodge()`
  replaces the location with the canonical URL, so an app always runs inside
  the shell.
- `apiURL()` is the app's `/api` WebSocket on the page's own host: `wss:`,
  or `ws:` on the development build's `http:` origin.
- `contentURL(path)` is `contentOrigin + "/" + app + "/" + path`. `path`
  has no leading `/`, and its segments are already encoded. It throws when
  `contentOrigin` is `null`.
- `storageKey(key)` is `<id>:` followed by `key`. Every app shares the
  origin's storage, so apps prefix every key this way.
- `history.push` and `history.replace` post `route`, and `navigate` moves
  `history.location` and calls its listeners.
- The shell's half, `@frostyard/lodge-client/shell`, keeps the frames'
  ports, one for each app's own frame and one for the companion's
  (`attach(app, frame, "companion")`): it does the origin and source
  checks, answers `ready` with the frame's role, and drops the companion
  frame's `route`, `context` and `ask`.

## Rules

- Both sides check the origin and the source of every message, and drop
  the rest.
- No message is evaluated, or inserted as HTML.
- An app in a frame never changes the top window's history or location.
- The switcher's shortcut never reaches an app, nor the companion's
  chord.
- A frame sends a context as a pointer, never another frame's content.
- A contract-major mismatch stops one frame's messages, not the shell.
- Unknown messages are dropped, so a major grows by addition only.

## Derived artifacts

| Artifact | Derivation |
| --- | --- |
| `client/src/messages.ts` | the [messages](#messages) table, as TypeScript types |
| the shell's frame handling (`shell/`) | [Start](#start), [routes](#routes), [`open`](#open), [the shortcut](#the-shortcut) and [the companion](#the-companion) |

## References

- Rationale: [ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md), [ADR-0010](../adr/0010-untrusted-bytes-come-from-a-second-node.md), [ADR-0008](../adr/0008-lodge-installs-as-one-home-screen-app.md), [ADR-0005](../adr/0005-browsers-sign-in-with-their-tailnet-identity.md) (keepalive), [ADR-0013](../adr/0013-the-companion-opens-beside-the-current-app.md) (the companion)
- Context: [design/overview.md](../design/overview.md#the-shell)
- Related: [shell.md](shell.md), [app-contract.md](app-contract.md#the-frontend-frostyardlodge-client), [lodge-http.md](lodge-http.md#headers)
- Built in: [roadmap — Phase 1](../plans/0001-roadmap.md#phase-1--the-kit) (the library), [Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app) (the shell's half), [Phase 6](../plans/0001-roadmap.md#phase-6--the-companion-and-the-agent-socket) (the companion)
