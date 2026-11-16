# Spec: lodge's OAuth server, consent and grants

lodge's OAuth 2.1 authorization server, the consent page where an admin
approves an agent's access, the grants page, and the state behind them:
principals, clients, grants and tokens. `internal/oauth` implements it on
`github.com/ory/fosite`, and `internal/gateway` routes to it after the
checks of [lodge-http.md](lodge-http.md#oauth-routes). Its clients are MCP
clients such as pi-pocket's `mcp` module, and lodge's MCP gateway and
broker, which take its tokens
([lodge-http.md](lodge-http.md#event-subscriptions) for the broker's). Rationale:
[ADR-0023](../adr/0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md).
Where it fits: [design/overview.md](../design/overview.md#agents).

**Status:** written on 2026-11-16 with the code that implements it.

## Interface

### Names

`LODGE` is the `lodge` node's origin, `https://LODGE`
([lodge-http.md](lodge-http.md#listeners)); in the development build it is
`http://127.0.0.1:PORT`.

| Thing | Form |
| --- | --- |
| issuer | `https://LODGE`, with no trailing `/` |
| resource | `https://LODGE/gateway/mcp` |
| scope | `mcp`, `events`, or both, space-separated |
| client ID | `lgc_` + 22 base64url characters (16 random bytes) |
| request ID | 32 lowercase hex digits (16 random bytes) |
| principal | `^[a-z][a-z0-9-]{0,31}$` |
| access token | `lgo_at_` + fosite's HMAC token |
| refresh token | `lgo_rt_` + fosite's HMAC token |
| authorization code | `lgo_ac_` + fosite's HMAC token |

- **A scope says what a client may use:** `mcp` the MCP gateway, `events`
  the broker. Nothing allows publishing.
- An authorization request without `scope` asks for `mcp`. A scope value
  other than `mcp` and `events` is `invalid_scope`.
- A `resource` parameter (RFC 8707), on the authorization or the token
  request, is either the issuer or starts with the issuer and `/`. Any other
  value is `invalid_request`.

### Metadata

`GET /.well-known/oauth-authorization-server` (RFC 8414):

```json
{
  "issuer": "https://LODGE",
  "authorization_endpoint": "https://LODGE/oauth/authorize",
  "token_endpoint": "https://LODGE/oauth/token",
  "registration_endpoint": "https://LODGE/oauth/register",
  "revocation_endpoint": "https://LODGE/oauth/revoke",
  "scopes_supported": ["mcp", "events"],
  "response_types_supported": ["code"],
  "grant_types_supported": ["authorization_code", "refresh_token"],
  "code_challenge_methods_supported": ["S256"],
  "token_endpoint_auth_methods_supported": ["none"],
  "revocation_endpoint_auth_methods_supported": ["none"]
}
```

`GET /.well-known/oauth-protected-resource` and
`GET /.well-known/oauth-protected-resource/gateway/mcp` (RFC 9728) give
the same document:

```json
{
  "resource": "https://LODGE/gateway/mcp",
  "authorization_servers": ["https://LODGE"],
  "scopes_supported": ["mcp", "events"],
  "bearer_methods_supported": ["header"],
  "resource_name": "lodge"
}
```

Both answer `Content-Type: application/json` and
`Cache-Control: no-store`.

### Registration

`POST /oauth/register` (RFC 7591), with `Content-Type: application/json`
and a body of at most 16 KiB. lodge reads these members and ignores the
others:

| Member | Rule |
| --- | --- |
| `redirect_uris` | required; 1 to 8 strings of at most 512 bytes; each an absolute URI with no fragment and no user name, whose host is a DNS name (labels of `A-Z`, `a-z`, `0-9` and `-`, 1 to 253 characters), an IPv4 literal or a bracketed IPv6 literal, with a port of digits only; and either `https`, or `http` with the host `127.0.0.1`, `[::1]` or `localhost`. The host rule keeps the origin safe in the consent page's `form-action` |
| `client_name` | optional; 1 to 64 characters with no control character; `unnamed client` when absent |
| `grant_types` | optional; a subset of `authorization_code` and `refresh_token` |
| `response_types` | optional; a subset of `code` |
| `token_endpoint_auth_method` | optional; any value is replaced with `none` (RFC 7591, section 3.2.1) |
| `scope` | optional; space-separated values from `mcp` and `events` |

- A good request answers **201** with the client's metadata:
  `client_id`, `client_id_issued_at`, `client_name`, `redirect_uris`,
  `grant_types` (`["authorization_code", "refresh_token"]`),
  `response_types` (`["code"]`), `token_endpoint_auth_method` (`none`) and
  `scope` (`mcp events`).
- A bad redirect URI answers 400 `{"error": "invalid_redirect_uri",
  "error_description": "…"}`; any other bad member answers 400
  `invalid_client_metadata`; a body that is not a JSON object, 400
  `invalid_client_metadata`.
- **Every client is public:** it has no secret, and PKCE protects its code.
- **A client no consent has bound is deleted 24 hours after it
  registered.** lodge holds at most 256 such clients; past that,
  registration answers 429 `{"error": "temporarily_unavailable"}`.

### Authorization

`GET /oauth/authorize`, the authorization code grant (OAuth 2.1), after the
[consent checks](lodge-http.md#oauth-routes):

1. fosite parses the request: `response_type=code`, a registered client, a
   redirect URI that matches one of the client's, a `state` of 8 or more
   characters, `code_challenge_method=S256` and a `code_challenge`.
   `plain` and a missing challenge are refused. A failure goes back to the
   client's redirect URI when that URI matched, else it is an error page.
2. lodge records a **sign-in request** ([below](#requests)) and answers
   **303** to `/oauth/consent/<id>`.

**Redirect URIs match** exactly, or, for an `http` URI whose host is a
loopback IP literal (`127.0.0.1`, `[::1]`), on every part but the port
(RFC 8252, section 7.3). `http://localhost` matches only exactly.

### Tokens

`POST /oauth/token`, with `Content-Type:
application/x-www-form-urlencoded`, a body of at most 16 KiB and the
client's `client_id`:

- `grant_type=authorization_code` with `code`, `redirect_uri` and
  `code_verifier`. A wrong verifier, a used code or another client's code
  is `invalid_grant`. A code is good for 10 minutes and once; using it a
  second time revokes every token issued from it.
- `grant_type=refresh_token` with `refresh_token`. Each refresh **rotates**
  the refresh token: the answer holds a new one, and the old one stops
  working.
- **A refresh token used a second time revokes its family:** every access
  and refresh token issued from the same approval. The answer is
  `invalid_grant`.
- An access token lives **1 hour** (`expires_in` 3600), a refresh token
  **30 days**. Every grant returns both, and `scope`, the scopes the
  consent approved.
- Errors are fosite's JSON (RFC 6749, section 5.2), without debug detail.

`POST /oauth/revoke` (RFC 7009) takes `token`, an optional
`token_type_hint` and `client_id`. Revoking an access token or a refresh
token revokes its family: every token of the approval that issued it
(RFC 7009, section 2.1). It answers 200 for a token it does not know.

### Principals, clients and grants

- **A principal** holds the grant. It is created when an admin approves a
  sign-in for a new name. A revoked principal's name can be approved
  again, and starts with no grant.
- **A client** is bound to 1 principal by its first approved sign-in, and
  keeps it. Its scope is the scope that its last approved sign-in
  approved.
- **A grant** is 1 list of items per principal and app:

  ```json
  [{"cap": "marks:read", "sel": {"folder": "/Reading"}},
   {"cap": "marks:write", "sel": {"folder": "/Reading/Inbox"}}]
  ```

  - `cap` is one of the app's capabilities, `<app>:<name>`.
  - `sel` maps the app's selector names to paths. A path is `/`, or
    `/`-separated segments in Unicode NFC, with no empty, `.` or `..`
    segment and no trailing `/`. An item without `sel`, or without a
    selector, covers that selector's root.
  - **A path G covers a path P** when G is `/`, P equals G, or P starts
    with G and `/`.
  - An approval adds its items to the grant; an exact duplicate is not
    added twice.
  - **Each item holds for 1 line of the app's version**: lodge stores
    with it `major`, the request's [line](#the-apps-manifest), as in
    `{"cap": "marks:read", "sel": {"folder": "/Reading"}, "major": "0.1"}`.
    An item is **in force** only while the app's line is its `major`.
    The gateway, the broker and every other reader of a grant see only
    the items in force; an app that is not installed with a hearth
    manifest has none. An item with no `major`, stored before lodge
    recorded it, is in force for no line. An approval of an item the
    grant holds already sets its `major` to the request's, which puts it
    in force again.
  - An app's new line takes the old items out of force at the reload
    that loads it: the gateway tells the principal's sessions that its
    tools changed, and every event subscription is refiltered at once
    ([ADR-0023](../adr/0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md)).
- **Effective access is the principal's grant limited by the client's
  scope.** The gateway uses the grant only for a token with scope `mcp`,
  the broker only for one with scope `events`, where a grant's read items
  become a consumer's filters
  ([lodge-http.md](lodge-http.md#event-subscriptions)).
- **Last use** is recorded for a client when one of its access tokens is
  checked, at most once a minute. A principal's last use is the latest of
  its clients'.
- **Revoking a client** marks it revoked and deletes its tokens and
  pending requests. **Revoking a principal** revokes each of its clients,
  deletes its grants and pending requests, and marks it revoked. Both take
  effect on the next check of a token.

### The app's manifest

An app on hearth names its manifest in `app.json`'s `hearth` member
([app-contract.md](app-contract.md#the-manifest-appjson)). lodge reads, from
that file in the app's directory:

| Member | Rule |
| --- | --- |
| `hearth` | the format major; lodge reads `1` |
| `app` | the app's ID |
| `version` | the app's version, `MAJOR.MINOR.PATCH`, each a number without a leading zero |
| `capabilities` | strings `<app>:<name>`, `name` matching `^[a-z][a-z0-9_]*$` |
| `selectors` | strings matching `^[a-z][a-z0-9_]*$` |
| `events` | the app's events, each `{type, message, topic, selector, read_permission}`: `topic` of 3 or more tokens matching `^[a-z][a-z0-9_]*$`, the first the app's ID, and ending in `.<selector>` when `selector` is set; `selector` one of `selectors` or absent; `read_permission` one of `capabilities`; no 2 events with the same `type` or `topic` |

lodge ignores the other members
([hearth's schema](https://github.com/frostyard/hearth/blob/main/schema/manifest-v1.json)).
A manifest that cannot be read, or whose format, app or version is not
these, disables the app, with the reason. For an unknown format the reason names
both versions: `hearth manifest format 2 is not one this lodge reads (1)`.

**The app's line** is `MAJOR` of its `version` from `1.0.0`, and
`0.MINOR` before it: `1.4.2` is on line `1`, `0.1.7` on line `0.1`. It is
the range in which hearth allows no breaking change of the manifest
([hearth ADR-0004](https://github.com/frostyard/hearth/blob/main/docs/adr/0004-generate-the-manifest-from-protobuf-options.md)),
so a grant item holds for 1 line ([above](#principals-clients-and-grants)).

### Requests

A request is what the consent page shows and an admin decides.

| Kind | Made by | Holds |
| --- | --- | --- |
| `signin` | `GET /oauth/authorize` | the client, a principal, the scopes asked for, and the authorization request |
| `access` | the gateway's `request_access`, through `RequestAccess` | the client, its principal, 1 app and 1 to 32 items |

- **A sign-in's principal** is the client's when it is bound; else lodge
  proposes the client's name, lower-cased, with every character outside
  `[a-z0-9-]` dropped, cut to 32 characters, when that is a valid
  principal, and else nothing.
- **An access request** needs a bound, live client, and an installed,
  enabled app with a hearth manifest. Each item's `cap` is one of the
  app's capabilities, each selector name one of its selectors, and each
  path valid. The request records the app's line when it is made.
  `RequestAccess` returns the request's ID and its URL,
  `https://LODGE/oauth/consent/<id>`.
- **At most 64 requests are pending** at once. A 65th is refused: the
  authorization answers 429 with one line, and `RequestAccess` returns an
  error.
- **A pending request expires 24 hours after it was made.** A request is
  deleted 24 hours after it expired or was decided.
- **The request hash** is the SHA-256, in lowercase hex, of this JSON, with
  the members in this order, the scopes sorted, the keys of each `sel`
  sorted, the items in the request's order, and no spaces:

  ```json
  {"id":"…","kind":"access","client":"lgc_…","principal":"chani","app":"marks","scopes":[],"items":[{"cap":"marks:read","sel":{"folder":"/Reading"}}]}
  ```

  A sign-in has `"app":""` and `"items":[]`; an access request has
  `"scopes":[]`.

### The consent page

`GET /oauth/consent/<id>` shows a pending request; `POST
/oauth/consent/<id>` decides it, after the
[consent checks](lodge-http.md#oauth-routes).

- **The page shows** the client's name and ID, its redirect URI's origin (a
  sign-in), the principal, the app's name, ID and version (an access
  request), and each scope or item. An access request made on another
  line than the app's now shows, above its items, the line of the
  refusal below. An item shows its capability and each selector's
  name and path, as the app's manifest names them; an item without a
  selector shows `everything`.
- **The form** holds `csrf`, the request's CSRF token, and `hash`, the
  request hash, as hidden fields, and the decision as the button pressed,
  `decision=approve` or `decision=decline`.
- **A sign-in** has a field `principal` (filled with the proposal, and
  read-only for a bound client) and a checkbox `scope` for each scope asked
  for, all checked.
  - Approving needs a valid principal and 1 or more scopes, each one asked
    for. lodge creates or revives the principal, binds the client to it,
    sets the client's scope, and answers with fosite's authorization
    response: a redirect to the client's redirect URI with `code` and
    `state`.
  - Declining redirects to the client with `error=access_denied`.
- **An access request** has, for each item `i` (from 0), a checkbox
  `item` with value `i`, all checked, and a text field `path.<i>.<selector>`
  for each selector of the item, filled with its path.
  - Approving needs 1 or more checked items. Each path is valid and covered
    by the path asked for: an admin may narrow a path, never widen it. The
    app is on the line the request was made on. lodge adds the approved
    items to the principal's grant for the app, for that line, and
    answers 303 to `/oauth/grants`.
  - Declining answers 303 to `/oauth/grants`.
- **A refused decision changes nothing,** answers with one line, and
  leaves the request pending, unless it was decided or expired already. In
  the order lodge checks:

  | Refusal | Status |
  | --- | --- |
  | the form cannot be read | 400 |
  | no request of that ID | 404 |
  | the CSRF token is not the request's | 403 |
  | the hash is not the request's | 409 |
  | the request was decided | 409 |
  | the request expired | 410 |
  | a field breaks a rule above: principal, scopes, items, paths, or a decision that is neither `approve` nor `decline` | 400 |
  | an access request was made on another line than the app's now: `app APP is at version V, and this request was made for LINE: decline it, and ask again` | 409 |
  | the client is bound to another principal than the request's | 409 |
  | the client or the principal was revoked after the request | 410 |
- **A sign-in whose client is bound to another principal than the one the
  request shows** answers 409, on the page and on an approval, with one
  line: `client CLIENT is bound to principal P: decline this request`. The
  request stays pending, and it can be declined.

### The grants page

`GET /oauth/grants`, after the consent checks, lists:

- each live principal: its name, when it was made, its last use, its grant
  per app (the app's version, and each item as on the consent page; an
  item that is not in force says why: `not in force: approved for version
  line LINE, and the app needs a new consent`), and its live clients, each
  with its name, ID, scope, when it registered, and its last use;
- each pending request: its kind, client, principal, app, when it was made
  and when it expires, and a link to its consent page.

Each client and each principal has a form, `POST /oauth/grants/revoke`,
with `csrf`, `ts`, and `client=<ID>` or `principal=<name>`. Each item of
each grant has one too, with `principal=<name>`, `app=<id>` and
`item=<hash>`, and a button **Remove**: it removes that item from the
principal's grant for the app, and deletes the grant when no item is left
([ADR-0024](../adr/0024-agents-reach-apps-on-hearth-through-one-mcp-gateway.md)).
An item's hash is the SHA-256, in lowercase hex, of the item's JSON as the
[request hash](#requests) writes it: `{"cap":"…","sel":{…}}`, the `sel`
keys sorted, no spaces, and no `sel` for an item without a selector.

- A form names exactly 1 target: `client` alone, `principal` alone, or
  `principal`, `app` and `item` together; any other mix is 400.
- A revocation or a removal answers 303 to `/oauth/grants`; an unknown or
  revoked target is 404.
- A removal narrows the grant at once: the gateway reads the grant at each
  call, and tells the principal's sessions that its tools changed.

### CSRF tokens

- A request's CSRF token is the HMAC-SHA256, under lodge's page key, of
  `consent`, a NUL byte and the request ID, in base64url.
- The grants page's is the HMAC-SHA256 of `grants`, NUL, the admin's
  login, NUL and `ts`, the page's time in Unix seconds. It is good from
  `ts` until 1 hour after it, and from 1 minute before `ts`.
- lodge compares a token in constant time.

### State

All of it is in lodge's database in `/var/lib/lodge/`:

| Table | Holds |
| --- | --- |
| `admins` | the admin logins, with when each was added |
| `oauth_keys` | 32 random bytes each: `tokens`, fosite's HMAC secret; `pages`, the CSRF key. Made at the first start |
| `principals` | name, created, revoked |
| `oauth_clients` | ID, name, redirect URIs, principal, scope, registered, last use, revoked |
| `oauth_sessions` | 1 row per code, access token, refresh token and PKCE session: its HMAC signature, kind, request ID (the family), client, the stored request and session, its expiry, and whether it is active |
| `grants` | principal, app, items (each with its line), updated |
| `consent_requests` | the requests, the app's line of an access request, and their decisions |

- No row holds a token, a code or a CSRF token: only signatures.
- Every hour lodge deletes expired sessions, unbound clients older than
  24 hours, and the requests [above](#requests).

### lodge's own callers

`internal/oauth` gives the rest of lodge:

- `Check(ctx, token, scope)`: the principal, client, scopes and expiry of a
  live access token that has `scope`, from a live, bound client of a live
  principal; else an error. A live token whose scopes, or whose client's
  scope, lack `scope` is the error `ErrScope`, which the gateway answers
  with 403 and not 401. It records last use.
- `RemoveGrantItem(ctx, principal, app, hash)`: removes 1 item of a grant
  ([the grants page](#the-grants-page)).
- `Grant(ctx, principal, app)`: the principal's items for the app.
- `Client(ctx, id)`: a live client, with its principal and scope; else
  `ErrNotFound`. The broker's sweep uses it.
- `RequestAccess(ctx, client, app, items)`: an access request
  ([above](#requests)).
- Hooks it calls after a change: a grant changed (principal, app), and a
  client or a principal revoked (principal, client IDs).

## Rules

- Only an admin's login decides a request or revokes access, and only
  through a `POST` that passes every consent check, with the request's CSRF
  token and hash. Each check has a test that fails it.
- A request is decided once. A pending request expires after 24 hours.
- A refresh token works once. Its reuse revokes its family.
- PKCE with `S256` is required; `plain` is refused.
- An OAuth token is refused on `/mcp/<id>` and on the agent socket, and an
  `lgm_` token by `Check`.
- No response, log line or row holds an access token, a refresh token, a
  code or a CSRF token, other than the response that issues it and the
  page that holds it.
- The consent and grants pages run no script and cannot be framed.
- Only `internal/oauth` imports `github.com/ory/fosite`.

## References

- Rationale: [ADR-0023](../adr/0023-lodge-issues-agent-access-through-oauth-and-a-consent-page.md), [ADR-0024](../adr/0024-agents-reach-apps-on-hearth-through-one-mcp-gateway.md) (the removal of a grant's item, the gateway's use)
- Context: [design/overview.md](../design/overview.md#agents)
- Related: [lodge-http.md](lodge-http.md#oauth-routes) (the routes and their checks), [app-contract.md](app-contract.md#the-manifest-appjson) (the `hearth` member)
