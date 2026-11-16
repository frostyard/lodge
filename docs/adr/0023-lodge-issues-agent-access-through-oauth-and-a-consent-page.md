# 0023 — lodge issues agent access through OAuth and a consent page

- **Status:** Proposed
- **Date:** 2026-11-16

## Context

- **Each connection costs a root command today.** An agent reaches an app
  through `/mcp/<id>` with a token that `lodgectl token issue` prints on
  lodge's host ([ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md)).
  Somebody copies it into the agent's configuration, and does it again for
  each app and each agent.
- **Standard MCP clients sign in with OAuth.** The MCP authorization
  specification, revision 2025-06-18, has a client find the authorization
  server from the resource (RFC 9728) and the server's metadata
  (RFC 8414), register itself (RFC 7591), and get tokens with the
  authorization code grant and PKCE (OAuth 2.1). A client that can do
  this needs no token copied by hand. pi-pocket's `mcp` module is one such
  client: it opens the sign-in link, takes the redirect on a loopback port
  that it picks each time, refreshes, and rotates refresh tokens.
- **New apps declare what an agent may do.** An app on frostyard/hearth
  ships a manifest with its capabilities (`marks:read`) and its selectors
  (`folder`), and checks a grant of items `{cap, sel}` on each call. lodge
  issues the grant, and the app enforces it.
- **A person approves access, and only a person.** An agent asks because
  its owner told it to. The owner sees what it asks for, narrows it,
  approves or declines it. Nothing is granted by default.
- **lodge's rules forbid this today.** [lodge-http.md](../specs/lodge-http.md#rules)
  says that no browser route writes, and that issuing a token and
  allowing a login or a node happen only on the control socket. A consent
  page is a browser route that writes, and an approval issues a
  credential.
- **A browser route that writes is a target.** A page on another site can
  submit a form to it, frame it to trick a click, or replay a request it
  saw. Every peer on an allowed device signs in by WhoIs
  ([ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md)),
  so a login on the allow-list does not prove that its owner may grant
  access.

## Decision

- **lodge is an OAuth 2.1 authorization server** for its own resources,
  on the `lodge` node, built on `github.com/ory/fosite`, which only
  `internal/oauth` imports. It follows the MCP authorization specification,
  revision 2025-06-18:
  - `/.well-known/oauth-protected-resource` (RFC 9728) and
    `/.well-known/oauth-authorization-server` (RFC 8414);
  - `/oauth/register`, dynamic client registration (RFC 7591), for public
    clients only (`token_endpoint_auth_method` `none`);
  - `/oauth/authorize`, the authorization code grant, with PKCE `S256`
    required and `plain` refused;
  - `/oauth/token`, for the code and the refresh token;
  - `/oauth/revoke` (RFC 7009).
- **Tokens are opaque.** An access token lives 1 hour, a refresh token 30
  days. Each refresh rotates the refresh token. A refresh token used a
  second time revokes its whole family: every access and refresh token
  that came from the same approval. lodge keeps only each token's HMAC
  signature, never the token.
- **Redirects** are `https`, or `http` on a loopback address. A loopback
  IP literal matches on any port, as RFC 8252 says, because a native
  client picks a free port at each sign-in.
- **A grant belongs to a principal, and a client acts for it.** A
  principal is a name such as `chani`. It has 1 or more OAuth clients and
  1 grant per app: a list of items `{cap, sel}`. A client's OAuth scope is
  `mcp`, `events` or both. What a client may do is the principal's grant
  limited by the client's scope. Revoking a client ends its tokens;
  revoking a principal ends all its clients and deletes its grants. All
  of it is in lodge's database in `/var/lib/lodge/`.
- **The consent page amends 2 rules of lodge-http.md.** "No browser route
  writes" gains 1 exception, and "issuing a token … happen only on the
  control socket" gains 1 exception: the consent routes, under these
  conditions together. `/oauth/authorize` only records a pending request,
  which grants nothing; the writes that grant or take away access are a
  decision on the consent page and a revocation on the grants page.
  - **Only an admin approves.** `lodgectl allow admin LOGIN` and
    `lodgectl disallow admin LOGIN` keep a list of admin logins, on the
    control socket only. A page that approves, declines or revokes
    admits a peer only when it passes every identity check of
    [lodge-http.md](../specs/lodge-http.md#identity) and its login is an
    admin's.
  - **Each decision is a `POST` from lodge's own page:** `Origin` is
    exactly lodge's, and `Sec-Fetch-Site` is present and `same-origin`.
  - **Each carries a CSRF token.** A decision carries the token that only
    lodge's page of that request holds, and **the hash of the exact
    request**: the client, the principal, the app, the capabilities and
    the selectors that the page showed. lodge refuses a decision whose
    hash is not the request's. A revocation carries the grants page's
    token, which covers the admin's login and a time, and expires after
    1 hour.
  - **A request is decided once,** and a pending request expires after
    24 hours.
  - **The pages run no script,** cannot be framed, and send no referrer
    to another origin.
  - lodge still sets no cookie, and identity still comes from WhoIs
    alone.
- **The consent page shows the request,** with each capability and
  selector from the app's hearth manifest. The admin may approve a part of
  it, narrow a selector's path, or decline it. An approval of a sign-in
  binds the client to a principal and its scope, then redirects to the
  client with a code. An approval of an access request adds the approved
  items to the principal's grant for that app.
- **The grants page** lists principals, their clients and grants, last
  use, and pending requests, and revokes a client or a principal.
- **Today's credentials stay.** `/mcp/<id>` with `lgm_` tokens, the
  agent socket and `lodgectl token issue` work as before
  ([ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md),
  [ADR-0014](0014-agents-on-lodges-host-use-a-local-agent-socket.md)).
  An OAuth token works on no route that takes an `lgm_` token, and an
  `lgm_` token on no OAuth route. The OAuth tokens serve lodge's MCP
  gateway and its broker
  ([ADR-0022](0022-lodge-embeds-a-nats-broker-for-app-events.md)).
- [specs/oauth.md](../specs/oauth.md) pins the server, the consent and
  grants pages and the storage.
  [specs/lodge-http.md](../specs/lodge-http.md#oauth-routes) lists the
  routes, their checks, and the 2 amended rules.

## Consequences

- **An agent connects without a root command.** Its owner opens 1 link,
  reads what it asks, and approves. A standard MCP client works the same
  way as pi-pocket.
- **lodge holds a credential store with a browser door.** The admin list,
  the request hash, the CSRF token, fetch metadata and the single use are
  5 fences, and each has a test that fails it. A compromised browser on an
  admin's device can still approve: as on every browser route, the device
  is trusted ([ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md#consequences)).
- **lodge's dependencies grow.** fosite brings `github.com/ory/x`, and
  through it OpenTelemetry and gRPC modules, which lodge does not call.
  Dependabot and `make vulncheck` watch them, as they watch the rest.
- **An admin can grant access in a browser,** which until now only root
  on lodge's host could do. The admin list is short, and empty until root
  adds a login.
- Nothing changes for frostnotes, frostmail, Porter and the PocketJournal
  until each moves to the gateway in its own change.

## Alternatives considered

- **Issue OAuth tokens only on the control socket:** keeps the rules
  whole, but a standard MCP client cannot sign in, and each connection
  stays a root command.
- **Our own OAuth code:** fewer dependencies, but lodge would own the
  grant, PKCE and rotation rules that fosite already implements and tests.
- **Self-contained (JWT) tokens:** a resource could check them alone, but
  lodge is the only resource, and revocation must take effect at once.
- **Any allowed login approves:** every program on an allowed device signs
  in as its owner, so the approval needs a smaller list.
- **A cookie and a session for the pages:** lodge sets no cookie on any
  browser response; the per-request CSRF token and WhoIs do the same work
  with no new state in the browser.

## References

- Shapes: [specs/oauth.md](../specs/oauth.md), [specs/lodge-http.md](../specs/lodge-http.md#oauth-routes), [specs/app-contract.md](../specs/app-contract.md#the-manifest-appjson), [design/overview.md](../design/overview.md#agents), [roadmap — Phase 7](../plans/0001-roadmap.md#phase-7--oauth-consent-and-grants)
- Builds on: [ADR-0005](0005-browsers-sign-in-with-their-tailnet-identity.md), [ADR-0006](0006-agents-reach-each-app-through-its-own-mcp-endpoint.md), [ADR-0014](0014-agents-on-lodges-host-use-a-local-agent-socket.md)
- Related: [ADR-0022](0022-lodge-embeds-a-nats-broker-for-app-events.md) (the broker, which takes an access token of scope `events`)
- External: [MCP authorization, revision 2025-06-18](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization), [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728), [RFC 8414](https://www.rfc-editor.org/rfc/rfc8414), [RFC 7591](https://www.rfc-editor.org/rfc/rfc7591), [RFC 7636](https://www.rfc-editor.org/rfc/rfc7636), [RFC 7009](https://www.rfc-editor.org/rfc/rfc7009), [RFC 8252](https://www.rfc-editor.org/rfc/rfc8252#section-7.3), [hearth's manifest](https://github.com/frostyard/hearth/blob/main/schema/manifest-v1.json)
