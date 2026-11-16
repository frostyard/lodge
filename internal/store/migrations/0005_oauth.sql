-- lodge's OAuth server, its consent and grants pages, and the state behind
-- them (docs/specs/oauth.md, State; ADR-0023).

-- The admin logins, compared byte for byte: only these decide a request.
CREATE TABLE admins (
	login    TEXT PRIMARY KEY,
	added_at TEXT NOT NULL
) STRICT;

-- lodge's keys, 32 random bytes each, hex: tokens is fosite's HMAC secret,
-- pages the CSRF key. Made at the first start.
CREATE TABLE oauth_keys (
	name       TEXT PRIMARY KEY,
	key        TEXT NOT NULL,
	created_at TEXT NOT NULL
) STRICT;

-- A principal holds the grants: a name such as chani. A revoked row stays,
-- so that the name may be approved again with no grant.
CREATE TABLE principals (
	name       TEXT PRIMARY KEY,
	created_at TEXT NOT NULL,
	revoked_at TEXT
) STRICT;

-- OAuth clients, registered at /oauth/register. An unbound client has no
-- principal and no scope; the first approved sign-in binds it for good.
-- redirect_uris is a JSON array.
CREATE TABLE oauth_clients (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL,
	redirect_uris TEXT NOT NULL,
	principal     TEXT,
	scope         TEXT,
	registered_at TEXT NOT NULL,
	last_used_at  TEXT,
	revoked_at    TEXT
) STRICT;

CREATE INDEX oauth_clients_principal ON oauth_clients (principal);

-- One row per authorization code, access token, refresh token and PKCE
-- session: its HMAC signature, never the token. request_id is the family
-- that one approval issued. form and session are JSON.
CREATE TABLE oauth_sessions (
	signature       TEXT NOT NULL,
	kind            TEXT NOT NULL,
	request_id      TEXT NOT NULL,
	client_id       TEXT NOT NULL,
	principal       TEXT NOT NULL,
	requested_at    TEXT NOT NULL,
	expires_at      TEXT NOT NULL,
	requested_scopes TEXT NOT NULL,
	granted_scopes  TEXT NOT NULL,
	requested_audience TEXT NOT NULL,
	granted_audience TEXT NOT NULL,
	form            TEXT NOT NULL,
	session         TEXT NOT NULL,
	active          INTEGER NOT NULL,
	PRIMARY KEY (kind, signature)
) STRICT;

CREATE INDEX oauth_sessions_request ON oauth_sessions (request_id);
CREATE INDEX oauth_sessions_client ON oauth_sessions (client_id);
CREATE INDEX oauth_sessions_expires ON oauth_sessions (expires_at);

-- A principal's grant for one app: a JSON list of {cap, sel} items.
CREATE TABLE grants (
	principal  TEXT NOT NULL,
	app        TEXT NOT NULL,
	items      TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY (principal, app)
) STRICT;

-- What the consent page shows and an admin decides. query holds a sign-in's
-- authorization request, as its form, so that the approval can rebuild it.
CREATE TABLE consent_requests (
	id         TEXT PRIMARY KEY,
	kind       TEXT NOT NULL,
	client_id  TEXT NOT NULL,
	principal  TEXT NOT NULL,
	app        TEXT NOT NULL,
	scopes     TEXT NOT NULL,
	items      TEXT NOT NULL,
	query      TEXT NOT NULL,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	decided_at TEXT,
	decision   TEXT
) STRICT;

CREATE INDEX consent_requests_pending ON consent_requests (decided_at, expires_at);
