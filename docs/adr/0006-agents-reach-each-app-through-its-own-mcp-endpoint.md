# 0006 — Agents reach each app through its own MCP endpoint

- **Status:** Proposed
- **Date:** 2026-10-18

## Context

frostnotes already serves MCP over stdio
([frostnotes ADR-0012](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0012-agents-reach-notes-through-mcp.md)),
and frostmail will. frostnotes' M4 drafted remote MCP for notesd's hub
(its draft ADR-0028, local commit `0586e2b`): Streamable HTTP on the
tailnet, one bearer token per agent, scoped to a vault and to read or
write. lodge now owns the network
([ADR-0003](0003-lodge-is-the-only-process-on-the-network.md)), so it owns
remote MCP for every app.

On 2026-10-18 the user decided:

- Remote MCP is for Claude Code and local agents on the tailnet, each with a
  scoped bearer token. claude.ai and the Claude apps, which need a public
  endpoint and OAuth 2.1, are later work.
- **Agents never run as the account that owns the data.** An agent that
  runs commands has its own identities: a GitHub machine account, a scoped
  `/mcp` token and a tagged node. stomp stays outside lodge, as an MCP
  client.
- The personal assistant reaches other apps only through their MCP, with
  tokens, like any agent.

Claude Code and other local MCP hosts connect to a Streamable HTTP server
directly, and can send a fixed `Authorization` header.

M4's review found that:

- an agent's scope rested on tool code alone (finding 9). A tool that
  forgot a check would reach the whole vault;
- at the session limit, new sessions should evict the least recently used
  one (finding 11). Whether Claude Code starts a new session after a 404
  is to be verified;
- `/mcp` needs a `Host` check;
- whether Claude Code sends an `Origin` header is to be checked in a live
  run (L10);
- lodge's docs should never suggest `claude mcp add --scope project`,
  which writes the token into a repository's `.mcp.json`.

## Decision

- **Each app that serves MCP has its own endpoint,** `https://LODGE/mcp/<id>`.
  lodge proxies it to the app's `/mcp` (Streamable HTTP: `POST`, `GET` and
  `DELETE`) as caller `agent`. An app opts in with `"mcp": true` in its
  manifest. One aggregated `/mcp` for clients that want a single connector
  is later work.
- **lodge issues and checks the tokens.**
  - A token is `lgm_` followed by 43 base64url characters: 32 bytes from
    `crypto/rand`.
  - lodge keeps its SHA-256, never the token.
  - It has a name, unique among live tokens, which is the user's label for
    the agent (`claude-code@laptop`). It also has one app and one scope
    string.
  - It does not expire.
  - It is issued only on lodge's control socket:
    `lodgectl token issue NAME --app ID --scope SCOPE` prints it once.
  - `lodgectl tokens` lists tokens with their last use, and `lodgectl
    token revoke NAME` revokes one.
- **A token works for one app only.** lodge checks, in order:
  - `Host` is lodge's name;
  - the request carries no `Origin` header, because agents send none and
    no browser page may reach `/mcp`;
  - WhoIs names the peer, from one of its own addresses;
  - `Authorization: Bearer` names a live token issued for this app.

  A missing, unknown or revoked token, or one for another app, is 401 with
  `WWW-Authenticate: Bearer` and no detail. lodge then removes
  `Authorization`, so the app never sees the secret. It passes
  `Lodge-Token` (the name) and `Lodge-Scope`.
- **The scope is the app's to define and to enforce, in its engine.**
  - lodge passes the scope through unread. Notes', for example, is
    `vault=<id>[;write]`.
  - The app binds the scope to the connection its MCP tools use. Its engine
    answers `notFound` or `methodNotFound` outside the scope, as for a
    caller that may not call a method. A tool that forgets a check
    therefore still cannot reach past the scope (review finding 9).
- **MCP sessions belong to the app.** `Mcp-Session-Id` and the session's
  state live in the app's handler, so the app owns the session limit.
  - An app binds each session, at `initialize`, to the token name and
    scope that opened it. A request for that session with another token,
    or under a changed scope, is 404 (MCP's answer for an unknown session).
  - At its limit, an app evicts an idle session first (no request or
    stream in flight), else the least recently used one (review finding
    11). Notes caps sessions at 8 per token name. A connected client that
    holds its stream open is therefore evicted only when every other
    session of its token is busy too.
  - `lodge/kit` provides the helper, so no app writes it twice.
- **lodge owns revocation.** It refuses a revoked token's next request, and
  at once closes every request and stream it is proxying for that token.
  Apps keep no list of tokens.
- **The tailnet is the fence, and the token is the key.** `/mcp/<id>` does
  not ask that the peer be an allowed login or node. An agent on a tagged
  node (a container, a build machine, stomp's) can use a token. lodge logs
  each request's node and token name. Agents off lodge's host stay on this
  route. An agent on lodge's own host, such as the assistant, uses lodge's
  local agent socket instead, with the same tokens' rules
  ([ADR-0014](0014-agents-on-lodges-host-use-a-local-agent-socket.md)).
- **Agents' writes are attributed to the token.** An app records the
  token's name (`Lodge-Token`) as an agent write's author. The client's own
  name from `initialize` is self-chosen, so it goes to the log only.
- **`lodgectl token issue` prints the token alone on stdout,** and on
  stderr how to add it to Claude Code:
  `claude mcp add --transport http <id> https://LODGE/mcp/<id> --header "Authorization: Bearer …"`.
  It never suggests `--scope project`.

[specs/lodge-http.md](../specs/lodge-http.md#mcpid) pins the checks, and
[specs/app-contract.md](../specs/app-contract.md#mcp-tokens-and-scope) pins
what an app must do.

## Consequences

- Claude Code on any of the user's machines reaches each app without a
  local daemon. One token reaches one app, inside one scope.
- A token sits in an agent's configuration file (`~/.claude.json`), in
  plain text. Whoever reads it can use the token from the tailnet until it
  is revoked. Its scope bounds the damage, and every write carries the
  token's name.
- The assistant gets one token per app it uses, so its reach is the sum of
  its scopes and nothing more.
- Agents that insist on OAuth discovery cannot connect. claude.ai and the
  phone's Claude app cannot reach lodge yet.
- Every app with MCP implements scope in its engine and binds sessions to
  tokens, through the kit's helper.
- Two facts are checked in the roadmap's Phase 2 live run: whether Claude
  Code sends an `Origin`, and whether it opens a new session after a 404.
  If it sends an `Origin`, this ADR changes before Phase 2 ends. **Both held
  on 2026-10-19** with Claude Code 2.1.300 against `/mcp/notes` and
  `/mcp/mail`:
  - It sends no `Origin`, so the no-`Origin` check stands.
  - After a 404 for its evicted session, it initializes a new session and
    retries the tool.

## Alternatives considered

- **OAuth 2.1 now:** an authorization server, client registration, PKCE
  and a consent page are a large security surface. They serve clients that
  cannot reach a tailnet-only lodge anyway.
- **The tailnet identity for agents too:** an agent on the user's laptop
  would get the user's whole access, with no scope, no read-only mode and
  no attribution per agent. It also breaks "agents never run as the account
  that owns the data".
- **One token for every app:** a leak would reach everything. Tokens per
  app keep each grant visible.
- **lodge interpreting scopes:** lodge would need each app's model of its
  data. A scope string the app's engine enforces keeps rights with the code
  that knows them.
- **lodge tracking MCP sessions:** it would duplicate state the app's MCP
  handler already keeps, and its limit could disagree with the app's.

## References

- Shapes: [specs/lodge-http.md](../specs/lodge-http.md#mcpid), [specs/app-contract.md](../specs/app-contract.md#mcp-tokens-and-scope), [design/overview.md](../design/overview.md#agents), [roadmap — Phase 1](../plans/0001-roadmap.md#phase-1--the-kit) (the session helper), [Phase 2](../plans/0001-roadmap.md#phase-2--lodge-v1-with-notes-as-its-only-app)
- Builds on: [ADR-0003](0003-lodge-is-the-only-process-on-the-network.md), [ADR-0004](0004-lodge-stays-on-the-tailnet-and-joins-it-itself.md); frostnotes' M4 draft ADR-0028 (local commit `0586e2b`), with review findings 9 and 11; frostnotes [ADR-0012](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0012-agents-reach-notes-through-mcp.md)
- Related: [ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md), [ADR-0014](0014-agents-on-lodges-host-use-a-local-agent-socket.md) (agents on lodge's host); frostnotes [ADR-0026](https://github.com/frostyard/frostnotes/blob/main/docs/adr/0026-notesd-runs-as-a-lodge-app.md) (notes' scope)
