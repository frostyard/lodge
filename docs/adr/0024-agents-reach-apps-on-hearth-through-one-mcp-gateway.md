# 0024 — Agents reach apps on hearth through 1 MCP gateway

- **Status:** Proposed
- **Date:** 2026-11-16

## Context

- **Each app has its own MCP endpoint today.** An agent reaches app `<id>`
  at `/mcp/<id>` with a token for that app, and lodge moves the frames to
  the app's own MCP server
  ([ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)).
  An agent with 3 apps holds 3 entries and 3 tokens.
- **lodge now issues agent access itself.** An MCP client signs in with
  OAuth 2.1, an admin approves it on the consent page, and the approval
  binds the client to a principal. The principal holds 1 grant per app, a
  list of items `{cap, sel}`
  ([ADR-0023](0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md)).
- **Apps on frostyard/hearth declare their API.** An app's hearth manifest
  lists its operations: each RPC's full name, its permission, its effect
  (`read`, `write` or `outward`), where it is exposed (`ui`, `agent`) and,
  for an agent RPC, its tool name. hearth generates each agent RPC's MCP
  tool: a description and the JSON Schemas of its request and response.
  The manifest holds no description and no schema.
- **The app enforces the grant, and lodge issues it.** hearth's lodge
  adapter reads the caller's principal and grant from the `Lodge-Principal`
  and `Lodge-Grant` headers, only on the app's lodge socket, after it
  checks that the peer is lodge. hearth's own MCP server has 1 caller for
  all its calls: an agent that lodge routes reaches the app's API, not its
  MCP server.
- **The app's API is Connect.** An app on hearth defines its API in
  Protobuf, and serves it with the Connect protocol over HTTP/1.1.
- **An MCP client learns of new tools from a notification.** When a server
  sends `notifications/tools/list_changed`, pi-pocket's `mcp` module and
  other clients read the tool list again in the same session.
- **An MCP client reads `insufficient_scope` as an OAuth matter.** A `403`
  with `WWW-Authenticate: Bearer error="insufficient_scope"` makes a client
  sign in again for more OAuth scope. A grant is not an OAuth scope: a new
  sign-in does not change it.
- **A browser frame of an app calls its API too.** Today a frame talks to
  its daemon over the `/app/<id>/api` WebSocket, with JSON-RPC lines. A
  Connect client in a frame sends 1 `POST` for each call.

## Decision

- **lodge serves 1 MCP gateway, `https://LODGE/gateway/mcp`,** for every
  principal, on the `lodge` node. The OAuth access token selects the
  principal and its client. Only a token whose consent approved scope
  `mcp`, from a client that still holds `mcp`, gets in. A missing or dead
  token is `401` with `WWW-Authenticate: Bearer resource_metadata="…"`
  (RFC 9728); a live token without `mcp` is `403` with
  `error="insufficient_scope", scope="mcp"`.
- **The gateway is an MCP server in lodge,** on the Go MCP SDK
  (`github.com/modelcontextprotocol/go-sdk`), which only `internal/mcpgw`
  imports. Sessions are stateful (Streamable HTTP), and each is bound to
  the OAuth client that opened it: a request with another client's token
  is refused.
- **The tools of a principal** are, for each enabled app with a hearth
  manifest, each agent operation whose permission the principal's grant
  for that app holds in at least 1 item, named `<id>_<tool>`. Some MCP
  clients refuse a dot in a name. The tool's description and schemas come
  from the app; its hints come from the manifest's effect, as hearth gives
  them. lodge adds 1 tool of its own, `request_access`.
- **An app names its tool catalog in `app.json`.** The member `tools` names
  a file in the app's directory in the shape of an MCP `tools/list` result:
  `{"tools": [{"name", "description", "inputSchema", "outputSchema"}]}`.
  It holds 1 tool for each agent operation of the manifest, and no other.
  lodge reads it with the manifest, from the root-owned app directory, and
  disables the app with the reason when it breaks a rule. The manifest
  stays the authority for the permission, the effect and the exposure.
- **A tool call is a Connect call on the app's lodge socket.** lodge reads
  the principal's grant for the app again, at each call. Without an item of
  the operation's permission, the call is a tool error that starts with
  `insufficient_scope:` and names `request_access`, and the app sees
  nothing. Else lodge sends `POST /connect/<service>/<method>` with the
  Connect unary protocol and its JSON codec, the call's arguments as the
  body, `Lodge-Principal` and `Lodge-Grant` (the grant's JSON, base64url
  with no padding). The app's answer is the tool's result; a Connect error
  is a tool error with the app's text. Selectors and stored objects are the
  app's to check.
- **`request_access(app, items)`** makes an access request for the
  session's client (ADR-0023) and returns its consent link. The agent shows
  it to its owner. Nothing changes until an admin approves.
- **A grant change reaches live sessions.** After an approval adds items,
  or the grants page removes one, lodge computes the principal's tools
  again. When they changed, every live session of the principal stays open
  and gets `notifications/tools/list_changed`. A call checks the current
  grant, so a narrowed grant refuses the next call. Revoking a client or a
  principal ends its requests and streams at once.
- **The grants page removes 1 item of a grant.** This narrows a grant with
  no new sign-in. It is a revocation, under ADR-0023's rules for the
  grants page.
- **A frame calls its app's API at `/app/<id>/connect/<service>/<method>`.**
  It is a browser route that takes `POST`, with `Origin` required, and only
  a Connect unary content type. It reaches only operations that the
  manifest exposes to `ui`. lodge writes nothing on it: it proxies the call
  to the app's `/connect/` path, with the login as the principal and a
  grant of every capability of the app on the root path, which is what a
  signed-in person may do in every app today.
- **An app with a hearth manifest has no `/mcp/<id>`** (404). This
  supersedes [ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)
  for apps on hearth only. frostnotes, frostmail, Porter and the
  PocketJournal keep `/mcp/<id>` and `lgm_` tokens.
- **The ID `gateway` is reserved,** as lodge's own first path.

## Consequences

- **An agent needs 1 entry for every app on hearth.** A new grant adds
  tools to it without a new token, a new entry or a restart.
- **lodge holds an MCP server.** lodge parses the gateway's JSON-RPC, which
  it never did for an app; it still parses none of an app's frames on
  `/mcp/<id>`, `/api` or `/connect/`. The SDK's dependencies join lodge's
  module, under Dependabot and `make vulncheck`.
- **lodge depends on 2 files of each app on hearth,** the manifest and the
  catalog, from the same build. A catalog that does not match the manifest
  disables the app. hearth's generator does not write the catalog yet; an
  app writes it from its generated tools until hearth does.
- **A browser route takes a body.** `Origin`, fetch metadata and a content
  type that a form cannot send keep other sites out; each has a test.
- **The app sees every call twice-checked:** lodge's check of the
  capability, and the app's own check of the path and the stored object.
  The app's check is the one that holds when lodge has a bug.
- **A principal's tool list depends on its grant,** so 2 agents see
  different tools at the same URL.

## Alternatives considered

- **1 MCP endpoint per app, as ADR-0006:** keeps lodge out of MCP, but an
  agent holds 1 entry per app, and a new grant cannot add an app's tools
  to a session.
- **lodge relays to each app's own MCP server:** hearth's MCP server has 1
  caller for all its calls, and a per-call caller is not built.
- **Tool schemas in the hearth manifest:** the manifest is the versioned
  contract with its own diff, and a schema change would need a new format.
  The catalog is a build output beside it.
- **The app serves its catalog on its socket:** the tools would vanish
  while the app is down, and the list would change with no grant change.
- **A `403 insufficient_scope` for a call outside the grant:** a client
  would sign in again, which cannot widen a grant; `request_access` can.
- **Close the sessions at a grant change:** the clients would reconnect
  later, not at once, and lose their state.

## References

- Shapes: [specs/lodge-http.md](../specs/lodge-http.md#the-mcp-gateway), [specs/app-contract.md](../specs/app-contract.md#apps-on-hearth), [specs/oauth.md](../specs/oauth.md#the-grants-page), [design/overview.md](../design/overview.md#agents), [roadmap — Phase 8](../plans/0001-roadmap.md#phase-8--the-mcp-gateway-and-apps-on-hearth)
- Builds on: [ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md) (superseded for apps on hearth), [ADR-0023](0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md), [ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md); frostyard/core [ADR-0085](https://github.com/frostyard/core/blob/main/docs/adr/0085-build-new-first-party-apps-on-hearth-with-lodge-as-their-hub.md)
- Related: [ADR-0022](0022-lodge-embeds-a-nats-broker-for-app-events.md) (events: a read grant is a topic prefix of the manifest's events)
- External: [MCP authorization, revision 2025-06-18](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization), [MCP tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools), [the Connect protocol](https://connectrpc.com/docs/protocol), [hearth's MCP tools](https://github.com/frostyard/hearth/blob/main/docs/specs/mcp-tools.md), [hearth's permissions](https://github.com/frostyard/hearth/blob/main/docs/specs/permissions.md)
