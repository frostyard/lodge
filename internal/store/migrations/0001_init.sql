-- lodge's state (docs/specs/lodge-http.md, Tokens, logins and nodes).

-- The allowed logins, compared byte for byte (BINARY collation).
CREATE TABLE logins (
	login    TEXT PRIMARY KEY,
	added_at TEXT NOT NULL
) STRICT;

-- The allowed nodes, by StableNodeID, with the node's name when allowed.
CREATE TABLE nodes (
	node_id  TEXT PRIMARY KEY,
	name     TEXT NOT NULL,
	added_at TEXT NOT NULL
) STRICT;

-- MCP tokens, kept as the SHA-256 of the secret. A revoked token's row stays
-- for the log, and its name may be reused by a live one.
CREATE TABLE tokens (
	id           INTEGER PRIMARY KEY,
	name         TEXT NOT NULL,
	app          TEXT NOT NULL,
	scope        TEXT NOT NULL,
	hash         TEXT NOT NULL UNIQUE,
	created_at   TEXT NOT NULL,
	last_used_at TEXT,
	revoked_at   TEXT
) STRICT;

CREATE UNIQUE INDEX tokens_live_name ON tokens (name) WHERE revoked_at IS NULL;
