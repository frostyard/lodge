-- A codex provider's sign-in with ChatGPT (docs/specs/inference.md, The
-- codex provider; ADR-0015): the login file lodgectl provider import
-- brings, as JSON, which lodge refreshes. Its tokens are shown nowhere. A
-- sign-out keeps the registration (client_id, account, email) and clears
-- the tokens.
CREATE TABLE provider_logins (
	provider   TEXT PRIMARY KEY,
	login      TEXT NOT NULL,
	updated_at TEXT NOT NULL
) STRICT;
