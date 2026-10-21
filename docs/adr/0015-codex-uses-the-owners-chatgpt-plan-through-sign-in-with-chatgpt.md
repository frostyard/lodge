# 0015 — Codex uses the owner's ChatGPT plan through Sign in with ChatGPT

- **Status:** Accepted (2026-10-21)
- **Date:** 2026-10-20

## Context

[ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md) gave
lodge a `codex` provider type for the owner's ChatGPT subscription, but
left it unbuilt. Using a subscription's login from third-party software was
a grey area of the provider's terms, so each provider had to be checked
first. Until 2026, tools reached ChatGPT's plan by reusing the Codex CLI's
OAuth client against ChatGPT's private backend, which OpenAI never offered
to third parties.

In 2026 OpenAI made this an official route: Sign in with ChatGPT, with
ChatGPT plan usage for open-source and locally hosted apps
([overview](https://developers.openai.com/siwc/token-sharing-open-source)):
- **Registration:** an app registers itself at the first sign-in
  (`client_id=dynamic_agent_client`, OAuth with PKCE and a loopback
  callback). OpenAI issues it a client bound to the owner and their
  workspace.
- **Requests:** they go to the public Responses API at
  `https://api.openai.com/v1`, with `store: false` and `stream: true`.
- **Limits:** the owner sets usage limits per app in ChatGPT's settings.
- **Self-hosted VMs:** a self-hosted VM signs in on the owner's laptop and
  copies the credential file to the VM over a secure channel. The VM then
  refreshes the tokens itself.
- **Eligibility:** paid or remotely hosted apps need OpenAI's interest
  form; open-source and locally hosted ones do not.

lodge is open source and self-hosted, and the owner asked on 2026-10-20 to
use their plan for Porter. Its plan usage is still a preview:
- it takes only the Responses API;
- it refuses several fields: `temperature`, `max_output_tokens`,
  `background`, and others;
- its docs say functions go grouped in namespaces, but a live check found
  plain functions accepted and app-named namespaces such as `notes`
  refused as reserved.

## Decision

- **The `codex` provider uses Sign in with ChatGPT's plan usage, and
  nothing else.** It sends `POST /responses` to the public API with the
  owner's access token. It never uses the Codex CLI's client or ChatGPT's
  backend.
- **The sign-in runs on the owner's computer, and lodge's host imports
  it**, as OpenAI's self-hosted VM procedure says:
  - `lodgectl provider login NAME` registers lodge (`agent_name_hint`
    "lodge") and writes a login file, 0600;
  - the owner copies the file to lodge's host and runs `sudo lodgectl
    provider import NAME`;
  - lodge keeps the login in its database, beside the provider keys.
  - A later sign-in reuses the issued client (`--client-id`), so the owner
    keeps one registration with one set of limits.
- **lodge alone refreshes the login.** Refreshes are serialized, because
  refresh tokens rotate, and the new tokens are saved before the access
  token is used. A refused refresh signs the provider out: lodge keeps the
  client and clears the tokens. The provider shows unavailable, with the
  command to sign in again. The computer that signed in never refreshes
  the copy it made.
- **Apps keep the chat completions API.** lodge translates each request
  into a Responses request: system messages become `instructions`, and
  tools go as plain functions under the app's names. It translates the
  event stream back into a chat completion or its chunks. An app that sends `/v1/responses` gets its
  request fitted to plan usage. The exact rules are in
  [specs/inference.md](../specs/inference.md#the-codex-provider).
- **Plan usage errors stop the request; they never fall back.** A usage
  limit answers 429 and names ChatGPT's settings. lodge never retries on
  another provider, and never switches to API billing.
- **Copilot stays unbuilt** until its terms are checked.

## Consequences

- The owner's plan answers whichever alias maps to the provider, such as
  `smart`. `local` cannot map to it, since it is not on the owner's
  hardware.
- Prompts sent to a `codex` alias leave the owner's network and go to
  OpenAI:
  - Porter's context goes there: mail, notes and the owner's own words.
  - This is the data policy of ADR-0011 (any app may use any provider). An
    app that must keep data at home asks for `local`.
- The sign-in needs a browser, so it never happens on lodge's host. After
  a sign-out (a revoked app, 30 days without a refresh), the owner runs
  the login again on their computer and imports it.
- Plan usage is a preview, and its docs and behavior already differ on
  tools. Its limits may change. They live in one translation file and its
  tests, and `TestCodexLive` checks them against the owner's plan. A
  refused field answers 400 with the field's name.
- Background work, such as Porter's routines, counts against the same
  plan limits as the owner's own use of ChatGPT and Codex.

## Alternatives considered

- **The Codex CLI's OAuth client and ChatGPT's backend:** this is what
  third-party tools did before 2026. It is not offered to third parties,
  and OpenAI's docs say not to point at the backend.
- **Codex app-server as a child process:** it is the official client, but
  it runs its own agent loop and tools. Porter needs its own tools and its
  own loop through an OpenAI-compatible API.
- **The sign-in on lodge's host, through a tunnel to its loopback:** this
  needs a port forward from the owner's browser for every sign-in, and
  OpenAI documents the laptop-and-copy route for VMs.
- **An OpenAI API key:** it bills per token apart from the owner's plan,
  which is what the owner wanted to avoid.

## References

- Shapes: [specs/inference.md](../specs/inference.md#the-codex-provider), [design/overview.md](../design/overview.md#inference), [roadmap — Phase 3](../plans/0001-roadmap.md#phase-3--the-inference-layer)
- Builds on: [ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md)
- OpenAI: [Sign in with ChatGPT for open-source apps](https://developers.openai.com/siwc/token-sharing-open-source), [registration and sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in), [self-hosted VMs](https://developers.openai.com/siwc/token-sharing-open-source/self-hosted-vms), [preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations), [errors and recovery](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery)
