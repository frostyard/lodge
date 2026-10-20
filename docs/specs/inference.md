# Spec: the inference layer

lodge's side of the inference socket: how it names the calling app, its
configuration of providers, aliases and allow-lists, the priority queue in
front of each provider, what it forwards and drops, the usage log, provider
credentials and availability, and the `lodgectl` commands for them. Its
consumers are `internal/inference`, `cmd/lodgectl`, the owner who configures
lodge, and every app daemon, whose side of the API is
[app-contract.md](app-contract.md#inference). Rationale:
[ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md),
and for the `codex` provider
[ADR-0015](../adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md).
Where it fits: [design/overview.md](../design/overview.md#inference).

**Status:** written on 2026-10-19 for the
[roadmap's](../plans/0001-roadmap.md#phase-3--the-inference-layer) Phase 3;
the `codex` provider added on 2026-10-20.

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

These are an `openai` provider's rules; a `codex` provider translates
instead ([The codex provider](#the-codex-provider)).

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
| `.baseURL` | string | for `openai` | an `http` or `https` URL whose path ends in `/v1`; for `codex`, optional, `https://api.openai.com/v1` when absent |
| `.slots` | int | for `openai` and `codex` | 1 to 64: requests the provider runs at once |
| `.home` | bool | no | the provider runs on the owner's own hardware; never for `codex` |
| `aliases.<alias>` | object | — | `local`, `fast` or `smart` in contract 1 |
| `.provider` | string | yes | a name in `providers` |
| `.model` | string | yes | 1 to 256 visible ASCII characters, the provider's model |
| `apps.<id>` | array of aliases | no | the aliases app `<id>` may use; an app absent here may use every alias |

- **`local` MUST map to a provider with `"home": true`.**
- **`codex` is the owner's ChatGPT plan** through Sign in with ChatGPT
  ([The codex provider](#the-codex-provider)).
- **`copilot` is not built.** Using a subscription's login from
  third-party software is a grey area of its terms
  ([ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md)).
  lodge accepts the type so a configuration can name it, and shows such a
  provider as unavailable, "not built: its terms are to verify".
- A configuration that breaks a rule is refused whole: lodge logs why,
  keeps serving everything else, has no aliases until a reload succeeds,
  and `lodgectl status` shows the reason.

### Credentials

- A provider's key is set with `sudo lodgectl provider key NAME`, which
  reads it from stdin; an empty input removes it. lodge keeps it in its
  state directory (`/var/lib/lodge/`, 0700), and shows it nowhere: not in
  `lodgectl`, not in a log.
- Configuration never holds a key.
- A `codex` provider has a sign-in instead of a key
  ([The codex provider](#the-codex-provider)). lodge keeps it in the same
  database, and shows only its account and client.

### The codex provider

The owner's ChatGPT plan, through Sign in with ChatGPT's plan usage for
open-source and locally hosted apps
([ADR-0015](../adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md)).

**The sign-in** runs on a computer with a browser:
`lodgectl provider login NAME [--client-id ID] [--email EMAIL] [--port N]
[--out FILE]`. It needs no running lodge.
- **The host ID:** it makes the computer's `ext_agent_host_id` once,
  `urn:uuid:…` in `$XDG_CONFIG_HOME/lodge/host-id`.
- **The callback:** it listens on `http://127.0.0.1:PORT/auth/callback`.
  The port is 1455, or any free port when 1455 is taken and `--port` is
  not given.
- **The browser:** it opens `https://auth.openai.com/api/accounts/authorize`
  with PKCE (S256), fresh `state` and `nonce`, `response_type=code`,
  `scope=openid profile email offline_access resource.invoke
  chatgpt.tokens.use.direct` and `resource=https://api.openai.com/v1`.
  - A first sign-in sends `client_id=dynamic_agent_client` and
    `agent_name_hint=lodge`.
  - A later one sends `--client-id`, the client issued at registration,
    with `--email` as `login_hint`.
- **The callback's checks:** it ignores a callback with another `state`.
  It refuses an `error`, a registration without an issued `client_id`, and
  a callback that names a client other than `--client-id`.
- **The exchange:** it exchanges the code at
  `https://auth.openai.com/api/accounts/oauth/token`, with the same
  `redirect_uri` and `resource`, and no client secret.
- **The ID token:** lodge verifies its RS256 signature against the
  issuer's published keys, found through discovery. It checks `iss`, that
  `aud` holds the client, `exp`, and `nonce`. It requires the granted
  `scope` to hold `chatgpt.tokens.use.direct`.
- **The login file:** it writes the file at `--out` (default
  `lodge-NAME-login.json`), mode 0600. The file holds `type` (`codex`),
  `client_id`, `account` (the ID token's `sub`), `email`, `scope`,
  `access_token`, `refresh_token`, `id_token`, `expires_at` and
  `earliest_refresh_at`.

**The import:** `sudo lodgectl provider import NAME < FILE`
(`lodge.providerImport`).
- lodge replaces the provider's sign-in with the file's.
- It answers `invalidParams` for a provider not of type `codex`, or for a
  file without a type, an issued client, an account, the plan's scope,
  tokens or an expiry. It answers `notFound` for no such provider.
- The owner deletes the file on both computers. The computer that signed
  in never refreshes its copy, because refresh tokens rotate.

**Refreshing:**
- lodge refreshes when the access token is two minutes from its expiry and
  `earliest_refresh_at` has passed, or after a 401. It POSTs
  `grant_type=refresh_token`, `client_id`, `refresh_token` and `resource`
  to the token endpoint.
- Refreshes are serialized, and the new tokens are saved before the access
  token is used.
- A refresh refused with any of these codes signs the provider out:
  `invalid_grant`, `invalid_refresh_token`, `token_expired`,
  `refresh_token_expired`, `refresh_token_invalidated`,
  `refresh_token_reused` or `invalid_client`.
  - A sign-out clears the tokens and keeps the client, account and email.
  - The provider shows unavailable, "signed out (CODE)", with the
    `lodgectl provider login NAME --client-id ID` to run.
- Any other refusal keeps a still-valid access token in use.

**Requests:**
- **The call:** lodge sends `POST <baseURL>/responses` with
  `Authorization: Bearer <access token>`, `Accept: text/event-stream`,
  `store: false` and `stream: true`, and nothing of the app's headers.
- **On 401:** lodge refreshes once and sends the request again.
- **An app's chat completion becomes a Responses request:**
  - `model` is the alias's model.
  - Leading `system` and `developer` messages become `instructions`, and
    later ones become `developer` messages.
  - `user` messages keep their text, and `image_url` parts become
    `input_image`.
  - An `assistant` message's text stays. Its `tool_calls` become
    `function_call` items, and `tool` messages become
    `function_call_output` items.
  - A message with any other role, or content of another type, answers 400
    `unsupported`.
  - Tools go in namespaces: `APP__TOOL` becomes function `TOOL` in
    namespace `APP`, and a name without `__` goes in namespace
    `functions`. A call's `function_call` item carries its namespace back.
  - `tool_choice` keeps `auto`, `none` and `required`; one named function
    becomes `required`.
  - `parallel_tool_calls` passes, and `reasoning_effort` becomes
    `reasoning.effort`.
  - Every other member is dropped, such as `temperature`, `max_tokens` and
    `chat_template_kwargs`.
- **The answer comes back in the shape the app asked for:**
  - A chat completion is answered once `response.completed` (or
    `response.incomplete`) arrives:
    - text becomes `content`;
    - each `function_call` becomes a `tool_calls` entry under the app's
      name;
    - `finish_reason` is `tool_calls`, `length` (for incomplete) or
      `stop`;
    - `usage` comes from the response.
  - A streamed one is answered as chunks:
    - each text delta becomes a `content` chunk;
    - each finished `function_call` becomes a `tool_calls` chunk;
    - then a chunk with `finish_reason`, a usage chunk when
      `stream_options.include_usage` asks, and `data: [DONE]`.
- **An app's own Responses request** is fitted to plan usage:
  - `store: false` and `stream: true`;
  - without `background`, `conversation`, `max_output_tokens`,
    `max_tool_calls`, `metadata`, `moderation`, `multi_agent`, `prompt`,
    `prompt_cache_retention`, `safety_identifier`, `temperature`,
    `top_logprobs`, `top_p`, `truncation`, `user` and
    `previous_response_id`;
  - `system` messages become `developer` messages.

  A streamed request gets the events as they come. Otherwise the answer is
  the response object of `response.completed`.
- **Errors:** plan usage errors answer in OpenAI's error shape, with the
  plan's `code`:

  | Code | Status |
  | --- | --- |
  | `subscription_sharing_usage_limit_exceeded` | 429, naming ChatGPT's Usage settings |
  | `subscription_sharing_usage_unavailable`, `subscription_sharing_user_unavailable` | 503 |
  | `subscription_sharing_user_not_eligible`, `subscription_sharing_route_not_supported` | 403 |
  | `subscription_sharing_unsupported_capability` | 400, naming the refused `param` |
  | `subscription_sharing_invalid_user` | 401 |

  - A stream that ends before `response.completed` answers 502
    `provider_failed`, or a last error chunk when streaming.
  - lodge never retries on another provider and never switches billing.

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

- lodge checks each `openai` and `codex` provider at start, every minute,
  and at once after a request fails to reach it or a sign-in is imported:
  `GET <baseURL>/models` with the key, or the sign-in's access token,
  answered 2xx within 5 s. A `codex` provider without a sign-in, or signed
  out, is unavailable with the command that fixes it.
- An unavailable provider's aliases list `"available": false`, and a
  request to one answers 503 at once, without queueing.
- A provider not yet checked, just after start or a reload that changed
  it, is neither: a request for it waits for its first check, at most 6 s.
- `lodgectl status` shows each provider: its type, whether it is
  available and why not, its slots, and how many requests run and wait;
  and for `codex`, the account and client it is signed in as.

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
| `lodgectl provider login NAME [flags]` | none | on a computer with a browser: signs a `codex` provider in and writes its login file ([The codex provider](#the-codex-provider)) |
| `lodgectl provider import NAME` | `lodge.providerImport` | replaces a `codex` provider's sign-in with the login file on stdin |
| `lodgectl provider models NAME` | `lodge.providerModels` | the provider's models: the plan's model slugs with `visibility` `list` for `codex`, `GET /models` ids for `openai`; `conflict` when the provider cannot answer |

## Rules

- No header names the caller outside `--root`; the peer's uid does.
- An app never sees a provider key, and a provider never sees an app's
  `Authorization` or `Lodge-*` headers.
- No log line, database row or `lodgectl` answer holds a prompt, a
  completion or a key, and no log line or `lodgectl` answer holds a
  sign-in's token.
- A `codex` request is unstored and streamed, and a plan usage error never
  falls back to another provider.
- An unavailable provider's alias fails at once, and never queues.
- Background work never holds the last slot of a provider with more than
  one.
- Every limit here (32 MiB, 256 waiting, `slots`, `slots − 1`) has a test
  at it and past it.

## References

- Rationale: [ADR-0011](../adr/0011-a-shared-inference-layer-routes-apps-to-models.md), [ADR-0015](../adr/0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md) (the `codex` provider), [ADR-0003](../adr/0003-lodge-is-the-only-process-on-the-network.md) (a user per app)
- Context: [design/overview.md](../design/overview.md#inference)
- Related: [app-contract.md](app-contract.md#inference) (the API apps call), [lodge-http.md](lodge-http.md#the-control-socket-and-lodgectl) (`lodgectl`)
- Built in: [roadmap — Phase 3](../plans/0001-roadmap.md#phase-3--the-inference-layer)
