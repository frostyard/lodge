# Spec: the inference layer

lodge's side of the inference socket: how it names the calling app, its
configuration of providers, aliases and allow-lists, the priority queue in
front of each provider, what it forwards and drops, the usage log, provider
credentials and availability, and the `lodgectl` commands for them. Its
consumers are `internal/inference`, `cmd/lodgectl`, the owner who configures
lodge, and every app daemon, whose side of the API is
[app-contract.md](app-contract.md#inference). Rationale:
[ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md).
Where it fits: [design/overview.md](../design/overview.md#inference).

**Status:** written on 2026-10-19 for the
[roadmap's](../plans/0001-roadmap.md#phase-3--the-inference-layer) Phase 3.

## Interface

### The socket

- `/run/lodge/inference.sock`, owner `lodge`, group `lodge-apps`, mode
  0660 (under `--root`, `DIR/run/lodge/inference.sock`). lodge makes it at
  start, so user `lodge` is a member of `lodge-apps`
  ([packaging](../../packaging/README.md)).
- HTTP/1.1. lodge serves it from its start, before its tailnet nodes join.
- A request body is at most **32 MiB** (413 past it).

### The caller

- **The caller is the connection's peer uid** (`SO_PEERCRED`). lodge maps
  it to the enabled app whose manifest's `user` has that uid. A uid that is
  no enabled app's is refused with 403, and no header changes the caller.
- **Under `--root`** (development and tests), every app runs as the
  developer. There, and only there, a peer whose uid is lodge's own names
  its app in the `Lodge-App` header. A release lodge without `--root`
  ignores the header.

### Requests

| Request | Answers |
| --- | --- |
| `GET /v1/models` | the aliases the caller may use, as [app-contract.md](app-contract.md#inference) says |
| `POST /v1/chat/completions` | the provider's answer, streamed when the body sets `"stream": true` |
| `POST /v1/responses` | likewise |

Any other path is 404, and another method 405. Errors have OpenAI's shape,
`{"error": {"message": "…", "type": "…", "code": "…"}}`:

| Status | `type` | `code` | When |
| --- | --- | --- | --- |
| 400 | `invalid_request_error` | `invalid_body` | the body is not a JSON object, or `model` is no string |
| 403 | `permission_error` | `unknown_caller` | the peer is no enabled app |
| 404 | `invalid_request_error` | `model_not_found` | the alias is unknown, or not allowed for the caller |
| 413 | `invalid_request_error` | `body_too_large` | the body is over 32 MiB |
| 429 | `rate_limit_error` | `queue_full` | 256 requests already wait for the provider |
| 502 | `server_error` | `provider_failed` | the provider could not be reached during the request |
| 503 | `server_error` | `provider_unavailable` | the alias's provider is unavailable; answered at once |

### What lodge forwards

- It reads the body's `model` and `stream` and replaces `model` with the
  alias's provider model. Every other member passes unchanged, a
  provider's own parameters included (the local model's thinking control).
- It sends `POST <baseURL>/chat/completions` or `<baseURL>/responses`, with
  `Content-Type: application/json`, the app's `Accept`, and the provider's
  `Authorization: Bearer <key>` when it has a key. No other header of the
  app's is sent: not its `Authorization`, and no `Lodge-*`.
- The provider's status, `Content-Type` and `Cache-Control` pass back, and
  its body streams back as it arrives.
- lodge reads the answer's `usage` only: `prompt_tokens` and
  `completion_tokens` (chat completions), or `input_tokens` and
  `output_tokens` (responses), from the body or from the stream's last
  chunk or `response.completed` event. A provider that sends none leaves
  the counts unknown. lodge never asks for usage it was not sent.

### Configuration

The `inference` member of lodge's configuration, `/etc/lodge/config.json`
(root, 0644, and no secrets), read at start and on reload (`lodgectl
reload`, `SIGHUP`). Without it, lodge has no aliases.

```json
{
  "inference": {
    "providers": {
      "home": { "type": "openai", "baseURL": "http://MODEL_HOST/v1", "slots": 4, "home": true }
    },
    "aliases": {
      "local": { "provider": "home", "model": "MODEL" },
      "fast":  { "provider": "home", "model": "MODEL" }
    },
    "apps": { "mail": ["local", "fast"] }
  }
}
```

| Field | Type | Required | Constraints |
| --- | --- | --- | --- |
| `providers.<name>` | object | — | a name of `[a-z][a-z0-9-]{0,31}` |
| `.type` | string | yes | `openai` (any OpenAI-compatible server), `copilot` or `codex` |
| `.baseURL` | string | for `openai` | an `http` or `https` URL whose path ends in `/v1` |
| `.slots` | int | for `openai` | 1 to 64: requests the provider runs at once |
| `.home` | bool | no | the provider runs on the owner's own hardware |
| `aliases.<alias>` | object | — | `local`, `fast` or `smart` in contract 1 |
| `.provider` | string | yes | a name in `providers` |
| `.model` | string | yes | 1 to 256 visible ASCII characters, the provider's model |
| `apps.<id>` | array of aliases | no | the aliases app `<id>` may use; an app absent here may use every alias |

- **`local` MUST map to a provider with `"home": true`.**
- **`copilot` and `codex` are not built.** Using a subscription's login
  from third-party software is a grey area of their terms
  ([ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md)).
  lodge accepts the types so a configuration can name them, and shows such
  a provider as unavailable, "not built: its terms are to verify". It has
  no login flow for either.
- A configuration that breaks a rule is refused whole: lodge logs why,
  keeps serving everything else, has no aliases until a reload succeeds,
  and `lodgectl status` shows the reason.

### Credentials

- A provider's key is set with `sudo lodgectl provider key NAME`, which
  reads it from stdin; an empty input removes it. lodge keeps it in its
  state directory (`/var/lib/lodge/`, 0700), and shows it nowhere: not in
  `lodgectl`, not in a log.
- Configuration never holds a key.

### The queue

Each provider has its own queue, of two classes: `Lodge-Priority:
interactive`, and `background` when the header is absent or anything else.

- At most `slots` requests run at once.
- When a slot frees, the oldest waiting interactive request takes it; only
  when none waits, the oldest background request may.
- Background requests together hold at most `slots − 1` slots, so one is
  always left for interactive work. With one slot, background requests may
  hold that slot, and wait behind every interactive request.
- At most 256 requests wait per provider (429 past it). A request whose
  app goes away while it waits leaves the queue.
- A slot is held until the provider's answer ends, streams included.

### Availability

- lodge checks each `openai` provider at start, every minute, and at once
  after a request fails to reach it: `GET <baseURL>/models` with the key,
  answered 2xx within 5 s.
- An unavailable provider's aliases list `"available": false`, and a
  request to one answers 503 at once, without queueing.
- `lodgectl status` shows each provider: its type, whether it is
  available and why not, its slots, and how many requests run and wait.

### The usage log

- Every request is logged once, to the journal and to lodge's database:
  the time, app, alias, provider, model, priority, input and output
  tokens (or unknown), time spent waiting, time to the first byte of the
  answer, total time, and status.
- **No prompt, completion or other content is logged or stored.**
- `lodgectl usage [--since YYYY-MM-DD]` sums the database per app and
  alias: requests, failures, and input and output tokens.

### `lodgectl`

| Command | Method | Does |
| --- | --- | --- |
| `lodgectl status` | `lodge.status` | adds a row per provider |
| `lodgectl usage [--since DATE]` | `lodge.usage` | the sums above, since a day (UTC) or for all time |
| `lodgectl provider key NAME` | `lodge.providerKey` | sets or removes a provider's key, from stdin; `notFound` for no such provider |

## Rules

- No header names the caller outside `--root`; the peer's uid does.
- An app never sees a provider key, and a provider never sees an app's
  `Authorization` or `Lodge-*` headers.
- No log line, database row or `lodgectl` answer holds a prompt, a
  completion or a key.
- An unavailable provider's alias fails at once, and never queues.
- Background work never holds the last slot of a provider with more than
  one.
- Every limit here (32 MiB, 256 waiting, `slots`, `slots − 1`) has a test
  at it and past it.

## References

- Rationale: [ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md), [ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md) (a user per app)
- Context: [design/overview.md](../design/overview.md#inference)
- Related: [app-contract.md](app-contract.md#inference) (the API apps call), [lodge-http.md](lodge-http.md#the-control-socket-and-lodgectl) (`lodgectl`)
- Built in: [roadmap — Phase 3](../plans/0001-roadmap.md#phase-3--the-inference-layer)
