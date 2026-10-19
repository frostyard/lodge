-- The inference layer's state (docs/specs/inference.md).

-- Provider keys, set with lodgectl provider key; shown nowhere.
CREATE TABLE provider_keys (
	provider TEXT PRIMARY KEY,
	key      TEXT NOT NULL,
	set_at   TEXT NOT NULL
) STRICT;

-- The usage log: one row per request, and never its content. Token counts
-- are NULL when the provider sent none.
CREATE TABLE usage (
	id            INTEGER PRIMARY KEY,
	at            TEXT NOT NULL,
	app           TEXT NOT NULL,
	alias         TEXT NOT NULL,
	provider      TEXT NOT NULL,
	model         TEXT NOT NULL,
	priority      TEXT NOT NULL,
	input_tokens  INTEGER,
	output_tokens INTEGER,
	wait_ms       INTEGER NOT NULL,
	first_byte_ms INTEGER,
	total_ms      INTEGER NOT NULL,
	status        INTEGER NOT NULL
) STRICT;

CREATE INDEX usage_at ON usage (at);
