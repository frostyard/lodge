# Spec: the lodge app contract

What an app installs and serves, and what lodge provides to it. Its
consumers:

- lodge, which installs nothing of an app's but proxies to it;
- `lodge/kit`, the Go side every app daemon builds on;
- `@frostyard/lodge-client`, the frontend side;
- each app: `notesd`, `maild`, and later the assistant.

The browser-facing checks are [lodge-http.md](lodge-http.md), and the
messages between the shell and a frame are
[lodge-client.md](lodge-client.md). The key words MUST, MUST NOT, SHOULD
and MAY are used as in RFC 2119. Rationale:
[ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md) (the
gateway), [ADR-0006](../adr/0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)
(MCP), [ADR-0007](../adr/0007-lodge-serves-every-apps-web-build-from-disk.md)
(bundles), [ADR-0010](../adr/0010-untrusted-bytes-come-from-a-second-node.md)
(content), [ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md)
(inference). Where it fits: [design/overview.md](../design/overview.md).

**Status:** contract major **1**, written on 2026-10-18. The app side binds
when the kit lands (the [roadmap's](../plans/0001-roadmap.md) Phase 1), and
lodge's side when lodge v1 does (Phase 2).

## Interface

```
 browser · phone ──https──▶ lodge  (tsnet node "lodge", :443)
                             ├─ sign-in: WhoIs + allowed logins and nodes
                             ├─ /, /<id>/<route>   the shell page: rail, switcher, app frames
                             ├─ /app/<id>/…        app bundles (from disk), /api and /x/ (proxied)
                             ├─ /mcp/<id>          agents, with scoped tokens
                             ├─ /device/<id>/…     routes the app authenticates itself
                             └─ inference.sock     local only: OpenAI-compatible, for app daemons
 tsnet node "lodge-content" ─▶ /<id>/…            untrusted bytes, sandboxed
                             │ HTTP/1.1 over Unix sockets; the peer must be lodge
       maild (user frostmail) · notesd (user frostnotes) · the assistant (user frostassist)
```

`LODGE` is the `lodge` node's DNS name (`lodge.<tailnet>.ts.net`), and
`CONTENT` the `lodge-content` node's.

### Install layout

| What | Where | Owner and mode |
| --- | --- | --- |
| manifest | `/usr/share/lodge/apps/<id>/app.json` | root, 0644 |
| web bundle | `/usr/share/lodge/apps/<id>/web/`; its scripts and stylesheets under `web/assets/` | root; directories 0755, files 0644 |
| app socket | `/run/lodge/apps/<id>.sock` | the app's user, group `lodge`, 0660 |
| inference socket | `/run/lodge/inference.sock` | `lodge`, group `lodge-apps`, 0660 |
| control socket | `/run/lodge/control.sock` | `lodge`, 0600 |
| the shell's bundle | `/usr/share/lodge/shell/` | root; directories 0755, files 0644 |
| lodge's configuration | `/etc/lodge/config.json` | root, 0644; no secrets |
| lodge's state | `/var/lib/lodge/` | `lodge`, 0700: tsnet state, allow-lists, tokens, provider credentials |
| the app's state | the app's own, such as `/var/lib/<user>/` | the app's user, 0700 |

- **System users.** Every daemon runs as its own system user, declared in
  `sysusers.d`. lodge runs as `lodge`, in group `lodge`. Each app's user
  MUST be in group `lodge-apps` (for inference), and MUST NOT be in group
  `lodge`.
- **Sockets.** An app's socket SHOULD be created by a systemd socket unit
  (`SocketUser=<user>`, `SocketGroup=lodge`, `SocketMode=0660`) and passed
  to the daemon. The kit accepts a passed socket or creates one itself. A
  daemon that creates its own MUST give it these owners and mode before it
  accepts.
- **One root directory overrides every path above** for development and
  tests: `--root DIR` for lodge, `LODGE_ROOT` for the kit, so that
  `/run/lodge/apps/notes.sock` becomes `DIR/run/lodge/apps/notes.sock`.
  There, lodge runs as the developer. The kit's peer check then accepts
  the uid in `LODGE_UID`, and its own uid when that is unset, in place of
  user `lodge`'s.
- **One host per lodge.** lodge and every app it hosts share one host,
  because they meet on Unix sockets.

### The manifest, `app.json`

| Field | Type | Required | Constraints |
| --- | --- | --- | --- |
| `contract` | int | yes | the contract major this app speaks: `1` |
| `id` | string | yes | `^[a-z][a-z0-9]{0,15}$`; equal to the directory's name; not `app`, `device`, `lodge` or `mcp`; the URL segment |
| `name` | string | yes | 1 to 32 characters; shown in the rail and the switcher |
| `icon` | string | yes | a relative path to an SVG file inside `web/` |
| `version` | string | yes | the app's version, 1 to 64 visible ASCII characters |
| `user` | string | yes | the system user the daemon runs as, `^[a-z_][a-z0-9_-]{0,31}$`; neither `root` nor `lodge`; unique across manifests |
| `key` | string | no | one letter, `^[a-z]$`, unique across manifests: the switcher followed by it opens the app |
| `content` | bool | no | the app serves untrusted bytes through `lodge-content` |
| `contentLinks` | bool | no | the app's content may open links in a new window: lodge's content policy becomes `sandbox allow-popups allow-popups-to-escape-sandbox` ([lodge-http.md](lodge-http.md#content-routes)), and the app's sanitizer drops `form`, `base` and `target` and sets `<base target="_blank">` |
| `device` | bool | no | the app has routes it authenticates itself |
| `mcp` | bool | no | the app serves MCP |
| `remoteImages` | bool | no | the app's frame may load any `https:` image (notes: yes; the assistant: never) |
| `companion` | bool | no | the app can open as the side panel beside another app |

A missing bool is `false`. Notes' manifest:

```json
{ "contract": 1, "id": "notes", "name": "Notes", "icon": "icon.svg",
  "version": "0.5.0", "user": "frostnotes", "key": "n",
  "content": true, "device": true, "mcp": true, "remoteImages": true }
```

- lodge reads manifests at start and on reload (`lodgectl reload`,
  `SIGHUP`). Fields it does not know are ignored, so a minor addition does
  not break an older lodge.
- An app is **disabled** when its manifest does not parse or breaks a
  constraint, its `contract` is not a major lodge speaks, or `web/index.html`
  or its icon is missing. lodge logs why, the rail shows it, and every route
  of that app answers 503. The other apps are unaffected.
- When two manifests claim one `user`, both are disabled. When two claim one
  `key`, both lose the key and stay enabled.

### Public URLs

On `https://LODGE` unless the row says otherwise:

| URL | Serves | Kind |
| --- | --- | --- |
| `/`, `/<id>`, `/<id>/<route>`, as a top-level page | the shell page, opening app `<id>` at `<route>` | browser |
| `/app/<id>/<path>` | the app's `web/` from disk; a path that is no file gets `web/index.html` | browser |
| `/app/<id>/api` (WebSocket) | the app's `GET /api` | browser (`session`) |
| `/app/<id>/x/<path>` | the app's `GET /x/<path>` | browser (`session`) |
| `/mcp/<id>` | the app's `/mcp` | agent |
| `/device/<id>/<path>` | the app's `/device/<path>` | device |
| `https://CONTENT/<id>/<path>` | the app's `GET /content/<path>`, under the sandbox policy | content |

- **`/<id>/<route>` is the canonical URL** of anything in an app, and it
  is permanent (Rule 6). The app's frame lives at `/app/<id>/<route>`, and
  the shell keeps the address bar on the canonical form. Notes' routes, for
  example, are `/notes/<vault id>/<path>`.
- **`api` and `x` are reserved** as the first segment of an app's routes.
- lodge passes a request's query to the app unchanged, and reads nothing
  from it.

### What an app serves on its socket

The socket speaks HTTP/1.1.

| Request | Required | `Lodge-Caller` | Purpose |
| --- | --- | --- | --- |
| `GET /health` | yes | any, or none | the app's state, for the rail |
| `GET /api`, upgraded to a WebSocket | yes | `session` | JSON-RPC 2.0, one message per text frame |
| `GET`, `HEAD` `/x/<path>` | no | `session` | the app's own reads for its trusted UI |
| `GET`, `HEAD` `/content/<path>` | if `content` | `content` | untrusted bytes |
| `POST`, `GET`, `DELETE` `/mcp` | if `mcp` | `agent` | MCP Streamable HTTP |
| any method `/device/<path>` | if `device` | `device` | routes the app authenticates itself, such as notes' sync and pairing |

- **The peer check.** An app MUST close a connection whose peer uid
  (`SO_PEERCRED`) is not lodge's, before it reads a request.
- **The caller.** Every request but `GET /health` MUST carry
  `Lodge-Caller`. An app MUST refuse a request without it, or with a kind
  the path does not admit, with 403. Each path admits exactly the one kind
  above, and a path outside this table admits none, so it is 403 too. A
  path of the table that the app does not serve (its manifest leaves the
  flag off, as notes does `/x/`) is 404 to the kind it admits. The kit's
  `lodgeapp.Server` answers both.
- **`/health`** answers 200 with `{"ok": true, "version": "…",
  "protocol": N}`. `version` is the app's, and `protocol` is the major of
  the app's own API (notes' is its RPC major). An app that is starting or
  degraded answers `{"ok": false, "reason": "…"}`. lodge polls it every
  15 s, with a 2 s timeout, and shows the app as up, degraded or down.
- **`/api`.** The upgrade carries the session's headers. An app MUST NOT
  check `Origin` here, because lodge has. Each frame is one JSON-RPC
  message of at most 16 MiB, and MAY end with one newline, which is not
  part of the message. The kit ends each frame it sends with one, as the
  apps' frontends read them, and hands the app one message per line.
  Frames are text, uncompressed, moved one to one, and lodge never
  parses them. When the browser leaves, lodge closes the app's side, and
  the app ends that connection's state.
- **`/content/`** responses MUST name what they serve by an unguessable
  key, such as a MAC of its hash under a secret the app keeps; a bare hash
  of the content is not unguessable
  ([lodge-http.md](lodge-http.md#content-routes)). They MAY carry `Content-Disposition: attachment` for
  a download (notes' `?download=1`). lodge adds the sandbox policy
  ([lodge-http.md](lodge-http.md#content-routes)).
- **`/mcp`** follows [MCP tokens and scope](#mcp-tokens-and-scope) below.
- **`/device/`** requests come with every header the device sent, `Origin`
  and `Authorization` included, except `Lodge-*`. The app authenticates
  them itself.
- **Errors** are `text/plain; charset=utf-8`, one line saying why.
- **Response headers.** On browser and content kinds, lodge replaces any
  security header the app sends, except `Content-Security-Policy`, which it
  keeps beside its own ([lodge-http.md](lodge-http.md#headers)). An app MUST
  NOT rely on `Set-Cookie` or CORS headers, which lodge drops.

### Caller headers

Before proxying any request, lodge deletes every `Lodge-*` header the
client sent, then sets these:

| Header | Value | Set for |
| --- | --- | --- |
| `Lodge-Caller` | `session`, `agent`, `device` or `content` | every request but lodge's `/health` polls |
| `Lodge-Login` | the node owner's tailnet login (`alice@example.com`, `alice@github`) | `session`, `content`; `device` when the node is untagged |
| `Lodge-Display-Name` | the owner's display name, when Tailscale has one | `session`, `content` |
| `Lodge-Node-ID` | the node's Tailscale StableNodeID | `session`, `content`; `device` when WhoIs names the peer |
| `Lodge-Node-Name` | the node's short host name: the first label of its DNS name, `[a-z0-9-]` | `session`, `content`; `device` when WhoIs names the peer |
| `Lodge-Session` | the session's ID, 16 lowercase hex digits | `session`, on the `/api` upgrade |
| `Lodge-Token` | the MCP token's name, `[A-Za-z0-9@._-]{1,64}` | `agent` |
| `Lodge-Scope` | the token's scope string, which lodge does not interpret | `agent` |

- **Encoding.** `Lodge-Login` and `Lodge-Display-Name` are UTF-8,
  percent-encoded (RFC 3986) for every byte outside `A–Z a–z 0–9 - . _ ~ @
  +`. The kit decodes them. The other values are ASCII as given.
- **`session` and `content` carry all four identity headers,**
  `Lodge-Display-Name` only when it is not empty. `/x/` reads are caller
  `session` without `Lodge-Session`.
- **Device requests carry the peer for display only.** An app MUST NOT
  authorize a device route on these headers, but MAY show them, for example
  beside a pending pairing.
- **Attribution.** An app SHOULD attribute writes to the connection's
  caller, never to a request's parameters: a session's to `Lodge-Node-Name`,
  and an agent's to `Lodge-Token`.
- **Trust.** An app MUST read `Lodge-*` headers only on its lodge socket,
  and only after the peer check. An app's private socket (its CLI and
  desktop bridge) is separate, keeps caller `local`, and ignores these
  headers.

### Sessions

- A session is one `/app/<id>/api` WebSocket, and it lasts while the
  socket does.
- **Liveness.** lodge closes a session after 90 s without a frame from the
  browser. An app's frontend MUST send a message on its socket at least
  every 30 s while the page is visible (`document.visibilityState`). Notes
  calls `rpc.ping`. A frontend whose socket closes reconnects when it is
  visible again.
- lodge bounds sessions per node and in all, and re-checks each one's
  identity every 60 s ([lodge-http.md](lodge-http.md#sessions)). An app
  learns a session's end only by its socket closing.

### MCP tokens and scope

- **Issuing.** lodge issues and checks tokens:
  `lodgectl token issue NAME --app ID --scope SCOPE`. A token is `lgm_`
  followed by 43 base64url characters, kept as its SHA-256. It works for one
  app only ([lodge-http.md](lodge-http.md#mcpid)).
- **What reaches the app:** `Lodge-Caller: agent`, `Lodge-Token` and
  `Lodge-Scope`. lodge removes `Authorization`, so the app never sees the
  token.
- **The scope** is 1 to 256 visible ASCII characters (`!` to `~`), whose
  grammar is the app's own. Notes' is `vault=<id>[;write]`. An app MUST
  answer 403 to a scope it cannot parse.
- **Enforcement in the engine.** An app MUST bind the scope to the
  connection its MCP tools use, and its engine MUST refuse anything outside
  it, as it refuses a method a caller may not call (`notFound`,
  `methodNotFound`). A tool's own checks are not enough.
- **Sessions are the app's.**
  - An app MUST bind each MCP session at `initialize` to `Lodge-Token` and
    `Lodge-Scope`.
  - It MUST answer 404 to a request for that session under another token
    name or scope.
  - It MUST bound live sessions per token name, evicting the least recently
    used. Notes keeps 8.
  - It SHOULD end a session idle for an hour.
  - The kit provides the helper.
- **Revocation is lodge's.** lodge refuses a revoked token, and closes
  every request and stream it is proxying for it. Apps keep no list of
  tokens.

### Inference

lodge serves an OpenAI-compatible API on `/run/lodge/inference.sock`, to
app daemons only.

| Request | Answers |
| --- | --- |
| `GET /v1/models` | `{"object": "list", "data": [{"id": "<alias>", "object": "model", "owned_by": "lodge", "available": bool}]}`: the aliases this app may use |
| `POST /v1/chat/completions` | as OpenAI's, streaming with `"stream": true` as server-sent events |
| `POST /v1/responses` | as OpenAI's, streaming likewise |

- **The caller** is the socket peer's uid. lodge maps it to a user name and
  that user to the manifest whose `user` it is. A uid that matches no
  enabled app is refused (403). No header can name another app.
- **Under the development root** (`--root`, `LODGE_ROOT`), every app runs
  as the developer, so an app names itself in `Lodge-App: <id>`. lodge
  reads the header there only, from a peer whose uid is its own, and a
  release lodge without `--root` ignores it.
- **The kit's client,** `kit/inference`, calls the socket under
  `LODGE_ROOT`. It sends `Lodge-Priority`, and `Lodge-App` under the root
  only, and no `Authorization`.
- **Aliases.** A request's `model` MUST name an alias: `local`, `fast` or
  `smart` in contract 1. lodge replaces it with the provider's model. An
  alias that is unknown, or not allowed for this app, answers 404 with
  `{"error": {"message": "…", "type": "invalid_request_error", "code":
  "model_not_found"}}`. `local` MUST map only to a provider on the owner's
  own hardware.
- **Priority.** `Lodge-Priority: interactive` or `background`, and
  background when the header is absent or anything else.
  - lodge runs at most the provider's `slots` requests at once.
  - When a slot frees, the oldest interactive request goes first.
  - Background requests together hold at most `slots − 1` slots.
- **Credentials.** lodge removes any `Authorization` the app sends and adds
  the provider's. Apps MUST NOT hold provider keys (Rule 7).
- **Bodies.** lodge reads only the `model`, the `stream` flag and the
  response's usage. It passes every other field through, including a
  provider's own parameters (the local model's thinking control). A request
  body is at most 32 MiB.
- **Availability.** lodge checks each provider at start and every minute.
  An alias whose provider is unreachable lists `"available": false`, and a
  request to it answers 503 at once.
- **Logging.** lodge logs every request: the app, alias, provider, model,
  priority, input and output tokens, time to first token, total time and
  status. It MUST NOT log prompts or completions.
- **Policy.** Any app may use any alias, unless lodge's configuration
  narrows that app's list.
- **Browsers never reach inference.** A frontend asks its own daemon.
- lodge's side (its configuration, the queue, credentials, availability
  and the usage log) is [inference.md](inference.md).

### The frontend: `@frostyard/lodge-client`

Every app bundle includes `@frostyard/lodge-client`, built for the base
`/app/<id>/`.

- The shell sends `init {contract, contentOrigin, shortcut, theme}` when
  the frame loads, and lodge-client's `contentURL(path)` builds content
  URLs.
- The app reports its route, and never calls `history.pushState` in a
  frame.
- The app MUST NOT bind the switcher's shortcut.
- The app prefixes its local-storage keys with `<id>:`.

[lodge-client.md](lodge-client.md) pins the messages. For an app with
`content: true`, its frame's policy allows `CONTENT` in `img-src`,
`media-src` and `frame-src`. With `remoteImages`, `img-src` also allows
`https:` ([lodge-http.md](lodge-http.md#headers)).

### Versioning

- `contract` is a major. Within a major, changes are additive only: new
  optional manifest fields, new headers and new messages. Apps and lodge
  ignore what they do not know.
- A change that breaks either side is a new major. lodge MAY speak several
  majors at once, and disables an app whose major it does not speak.
- Apps pin `lodge/kit` and `@frostyard/lodge-client` to a published lodge
  commit or release, never to a local path.

## Rules

1. Only lodge listens on the network. App daemons listen only on Unix
   sockets.
2. An app socket accepts connections only from lodge's uid
   (`SO_PEERCRED`), and refuses a request without `Lodge-Caller`, except
   `GET /health`. That is why the `Lodge-*` headers can be trusted. lodge
   deletes every `Lodge-*` header a client sends.
3. Every app runs as its own system user, and no app can read another's
   state. The assistant reaches other apps only through `/mcp/<id>`, with a
   token.
4. lodge serves every app's web files itself, from root-owned directories.
   No daemon serves code to the lodge origin.
5. Untrusted bytes come only from `lodge-content`, under a `sandbox`
   content policy.
6. A canonical URL `/<id>/<route>` keeps working across app versions.
7. Provider credentials live only in lodge. Apps never hold provider keys.
8. A contract-major mismatch disables that one app. The others keep
   running.
9. lodge never parses an app's JSON-RPC. It moves frames.
10. Each path on an app socket admits one caller kind.
11. An app enforces an agent's scope in its engine, and binds each MCP
    session to the token that opened it.
12. No app authorizes a device route on lodge's identity headers.

## Derived artifacts

| Artifact | Derivation |
| --- | --- |
| `kit/`'s manifest type, caller parsing and socket server | this spec's [manifest](#the-manifest-appjson), [caller headers](#caller-headers) and [socket](#what-an-app-serves-on-its-socket) sections (the roadmap's Phase 1) |
| lodge's manifest loader (`internal/apps`) | the same manifest type, from `kit/` |
| `client/`'s message types | [lodge-client.md](lodge-client.md) |
| each app's `app.json` and socket unit | this spec, in the app's repository (notes: frostnotes' [lodge-app.md](https://github.com/frostyard/frostnotes/blob/main/docs/specs/lodge-app.md)) |

## References

- Rationale: [ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md), [ADR-0005](../adr/0005-browsers-sign-in-with-their-tailnet-identity.md), [ADR-0006](../adr/0006-agents-reach-each-app-through-its-own-mcp-endpoint.md), [ADR-0007](../adr/0007-lodge-serves-every-apps-web-build-from-disk.md), [ADR-0009](../adr/0009-apps-switch-in-frames-that-stay-loaded.md), [ADR-0010](../adr/0010-untrusted-bytes-come-from-a-second-node.md), [ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
- Context: [design/overview.md](../design/overview.md)
- Related: [lodge-http.md](lodge-http.md), [lodge-client.md](lodge-client.md), [shell.md](shell.md)
- Built in: [roadmap — Phase 1](../plans/0001-roadmap.md#phase-1--the-kit) (the app side), [Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app) (lodge's side), [Phase 3](../plans/0001-roadmap.md#phase-3--the-inference-layer) (inference)
