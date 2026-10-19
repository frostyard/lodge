# 0011 — A shared inference layer routes apps to models

- **Status:** Proposed
- **Date:** 2026-10-18

## Context

Several apps want language models. Mail wants summaries and triage, notes
wants search and writing help, and the assistant is built on them. The
owner runs a local model: an OpenAI-compatible server on their LAN (its
address is local configuration, written `MODEL_HOST`). It offers
`/v1/chat/completions` and `/v1/responses`, a 262k-token context, thinking
control and no vision. It has **4 slots**, so at most four requests run at
once, across every app.

The owner also has Copilot and Codex subscriptions. Using a subscription's
login from third-party software is a grey area of their terms of service.
stomp's README says so for Claude. Each provider has to be verified before
lodge uses it.

On 2026-10-18 the user decided:

- **A shared inference layer in lodge.** It is OpenAI-compatible, on a Unix
  socket, for app daemons only. The calling app is identified by the
  socket's peer uid. Interactive requests go before background ones.
- **Providers:** the local model first, then Copilot and Codex, whose
  adapters are still to be verified.
- **Data policy:** any app may use any provider. Every request is logged
  with its app, alias, provider, model, token counts and latency, and never
  its content. Per-app allow-lists exist, and default to open.

Each app runs as its own system user
([ADR-0003](0003-lodge-is-the-only-process-on-the-network.md)).

## Decision

- **lodge serves an OpenAI-compatible API on `/run/lodge/inference.sock`,**
  owner `lodge`, group `lodge-apps`, mode 0660. Every app's user is in
  `lodge-apps`. The API is `GET /v1/models`, `POST /v1/chat/completions` and
  `POST /v1/responses`, streaming or not.
- **The caller is the socket peer's uid,** mapped to an app through the
  manifests' `user` fields. No header can claim another app, and a uid that
  is no app's is refused.
- **Apps ask for aliases, not models.** A request's `model` names an alias
  (`local`, `fast`, `smart`), and lodge's configuration maps each alias to
  a provider and a model. `GET /v1/models` lists the aliases the calling
  app may use. `local` maps only to a provider on the owner's own hardware,
  so an app that must keep data at home can ask for that.
- **The queue puts interactive work first.** `Lodge-Priority: interactive |
  background` sets a request's class, and background is the default.
  - lodge runs at most a provider's `slots` requests at once (4 for the
    local model).
  - When a slot frees, the oldest interactive request goes first.
  - Background requests together use at most `slots − 1`, so one slot is
    always left for interactive work.
- **Providers' credentials live only in lodge,** in its state directory.
  lodge drops any `Authorization` an app sends and adds the provider's.
  Apps never hold a provider key.
- **Every request is logged without its content.** The log records the app,
  alias, provider, model, priority, input and output tokens, time to first
  token, total time and status. It never records a prompt or a completion.
  `lodgectl` sums the log per app.
- **Per-app allow-lists exist and start open.** An app may use every alias
  unless lodge's configuration narrows it. A refused alias answers as an
  unknown model.
- **Browsers never reach inference.** A frontend asks its own daemon, and
  the daemon decides what to send.
- **The local model comes first.** The Copilot and Codex adapters are built
  only after each provider's terms are checked for this use
  ([roadmap — Phase 3](../plans/0001-roadmap.md#phase-3--the-inference-layer)).
- **A provider must be reachable from lodge's host.** A local model on
  another machine needs a network path to it: a firewall rule, a listener
  or the tailnet. lodge checks each provider at start and periodically.
  `GET /v1/models` and `lodgectl status` show an unreachable provider as
  unavailable, and its aliases fail fast rather than queue.

[specs/app-contract.md](../specs/app-contract.md#inference) pins the API.

## Consequences

- No app holds a provider key, and the owner configures models once.
  Switching a model or a provider is a lodge configuration change, and the
  apps' aliases stay the same.
- The local model's 4 slots are shared fairly across apps. A user waiting
  on an interactive request never queues behind four long background jobs.
- **Provider credentials sit in the network-facing process.** A compromise
  of lodge exposes them, as it already exposes every app's sessions. The
  inference layer is a package behind its own socket, so it can move into a
  process of its own without changing the contract.
- The usage log shows which app spends what, but never what was said. That
  is not enough to debug a bad answer, which stays the app's job.
- lodge parses request and response bodies on this socket, to read the
  model, the stream flag and the token counts. That is not app JSON-RPC,
  which lodge never parses.
- The lodge host's network decides which providers work. A local model
  unreachable from that host shows as unavailable until a path exists.

## Alternatives considered

- **Each app calling providers itself:** keys in every app, and no shared
  queue in front of the local model's 4 slots. Each app would also log in
  its own way.
- **A separate inference daemon under its own user:** it would keep
  provider credentials out of the network-facing process. The user chose
  lodge to hold them, and the socket contract lets the layer move out
  later.
- **LiteLLM or a similar proxy:** another runtime and its own key model.
  The peer-uid identity and the priority queue would still be needed.
- **Browsers calling inference directly:** the browser would need a
  credential, and nothing could stop a page from sending anything to any
  model.

## References

- Shapes: [specs/app-contract.md](../specs/app-contract.md#inference), [design/overview.md](../design/overview.md#inference), [roadmap — Phase 3](../plans/0001-roadmap.md#phase-3--the-inference-layer)
- Builds on: [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md); frostyard/core [ADR-0057](https://github.com/frostyard/core/blob/main/docs/adr/0057-run-first-party-apps-behind-lodge.md)
- Related: [ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md) (the assistant's reach to other apps)
