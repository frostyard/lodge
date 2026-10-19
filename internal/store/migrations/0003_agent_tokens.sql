-- Tokens bound to their route (docs/specs/lodge-http.md, Tokens, logins and
-- nodes; ADR-0014). agent is the system user a token was issued for, which
-- may use it only on the agent socket; NULL is a token for the tailnet.
ALTER TABLE tokens ADD COLUMN agent TEXT;
