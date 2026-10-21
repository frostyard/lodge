# 0016 — Copilot uses the owner's Copilot plan through GitHub's device sign-in

- **Status:** Accepted (Brian, 2026-10-21)
- **Date:** 2026-10-21

## Context

[ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md) gave
lodge a `copilot` provider type for the owner's GitHub Copilot
subscription, and left it unbuilt until its terms were checked.
[ADR-0015](0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md)
built `codex` and kept Copilot unbuilt for the same reason. On 2026-10-21
the owner read Copilot's terms, found this use acceptable, and asked for
Copilot in the suite.

GitHub offers Copilot to software outside its own products in two ways:
- **The Copilot SDK** (generally available since 2026-06-02). It drives
  the Copilot CLI, a separate runtime, over JSON-RPC. The CLI runs its own
  agent loop with its own tools, and it accepts an OAuth user token, a
  GitHub App user token or a fine-grained personal access token.
- **The Copilot API that the editors use.** An editor signs the user in
  with GitHub's device flow under Copilot's editor OAuth client. It then
  trades the GitHub token for a short-lived Copilot token at
  `https://api.github.com/copilot_internal/v2/token`. That token reaches
  an OpenAI-compatible API (`/models`, `/chat/completions`,
  `/responses`) at the endpoint the answer names. Open-source tools
  (aider, LiteLLM, the Pi coding agent on the owner's machines) use this
  route. GitHub does not document it for third parties.

lodge's apps ask for chat completions with their own tools and their own
loop ([ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md)).
Porter is the main user.

## Decision

- **The `copilot` provider uses the Copilot API, the editors' route.** It
  sends the app's chat completion, or its Responses request, unchanged
  but for the model, to the API, with a Copilot token. No translation is
  needed: the API is OpenAI-compatible.
- **The credential is a GitHub token of the owner's account, kept as the
  provider's key.** `sudo lodgectl provider key NAME` stores it, as for
  any provider, and lodge shows it nowhere.
  - `lodgectl provider login NAME --copilot` gets one with GitHub's device
    flow, on any computer: it prints a URL and a code, and the owner
    enters the code in any browser. It writes the token to a file (0600)
    for `provider key`. It needs no running lodge and no browser on
    lodge's host.
  - The device flow uses Copilot's editor OAuth client, with scope
    `read:user`, the only client whose tokens the token exchange takes.
- **lodge trades the GitHub token for a Copilot token, and keeps that in
  memory only.** It renews it when the answer's `refresh_in` has passed
  or two minutes before `expires_at`, and once after a 401 from the API.
  Renewals are serialized per provider.
- **lodge identifies itself as an editor integration,** with the headers
  the editors send (`Copilot-Integration-Id`, `Editor-Version`,
  `Editor-Plugin-Version`, `User-Agent`, `Openai-Intent`). They live in
  one place in the code and in the spec.
- **`X-Initiator` is honest.** A request whose last message is the
  user's is `user`; one that follows a tool result is `agent`. This
  decides which requests count as premium requests, so lodge never claims
  `agent` for a user's turn.
- **A refusal makes the provider unavailable with its reason.** A GitHub
  token refused at the exchange says to sign in again; an account with no
  Copilot says so. lodge never falls back to another provider.
- **A `copilot` provider is never `home`,** so `local` cannot map to it.

[specs/inference.md](../specs/inference.md#the-copilot-provider) pins the
exchange, the headers and the requests.

## Consequences

- The owner's Copilot plan answers whichever alias maps to the provider.
  Prompts sent to it leave the owner's network for GitHub and the model's
  vendor, as ADR-0015 says for `codex`. An app that must keep data at home
  asks for `local`.
- Requests count against the Copilot plan's premium requests, as the
  owner's own use in an editor does. Background routines spend the same
  allowance.
- **The route is not documented for third parties.** GitHub can change
  the exchange, the headers or the endpoints, or refuse the editor client
  for lodge's use. lodge then shows the provider as unavailable, with the
  status it got, and the aliases fail fast. `TestCopilotLive` checks the
  route against the owner's account when it is run.
- Tokens from the device flow do not expire until revoked. Revoking the
  OAuth authorization in GitHub's settings signs lodge out at the next
  exchange.
- No new control-socket method: the key path already exists.

## Alternatives considered

- **The Copilot SDK and CLI:** the documented route, but the CLI runs its
  own agent loop and tools, and it is a second runtime on lodge's host.
  lodge's apps run their own loops through an OpenAI-compatible API. This
  is the reason ADR-0015 gave against Codex's app-server.
- **A fine-grained personal access token straight to the API:** the SDK
  takes such tokens through the CLI, but nothing documents the API taking
  them directly. If GitHub documents it, a later ADR can drop the device
  flow and keep the rest.
- **An OAuth app of lodge's own:** the token exchange takes only the
  editors' client.
- **GitHub Models (`models.github.ai`):** a separate product with its own
  limits and models, not the Copilot plan. It needs no new code: it is an
  OpenAI-compatible server that an `openai` provider can name.

## References

- Shapes: [specs/inference.md](../specs/inference.md#the-copilot-provider), [design/overview.md](../design/overview.md#inference), [roadmap — Phase 3](../plans/0001-roadmap.md#phase-3--the-inference-layer)
- Builds on: [ADR-0011](0011-a-shared-inference-layer-routes-apps-to-models.md), [ADR-0015](0015-codex-uses-the-owners-chatgpt-plan-through-sign-in-with-chatgpt.md)
- GitHub: [Copilot SDK authentication](https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/authenticate), [the SDK's agent loop](https://docs.github.com/en/copilot/how-tos/copilot-sdk/features/agent-loop), [device flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps#device-flow)
