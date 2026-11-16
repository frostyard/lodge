package oauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/ory/fosite"
)

// Client is one OAuth client, as lodge keeps it.
type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
	// Principal is the principal the client is bound to; "" until a sign-in
	// is approved.
	Principal string
	// Scope is the scope its last approved sign-in approved.
	Scope      string
	Registered time.Time
	LastUsed   time.Time
	Revoked    time.Time
}

// Bound reports whether a sign-in bound the client to a principal.
func (c Client) Bound() bool { return c.Principal != "" && c.Revoked.IsZero() }

// Principal is one principal, the holder of the grants.
type Principal struct {
	Name    string
	Created time.Time
	Revoked time.Time
}

// Live reports whether the principal may hold access.
func (p Principal) Live() bool { return p.Revoked.IsZero() }

// Access is what a live access token gives: the principal, the client and
// the scopes the consent approved (docs/specs/oauth.md, lodge's own
// callers).
type Access struct {
	Principal string   `json:"principal"`
	Client    string   `json:"client"`
	Scopes    []string `json:"scopes"`
}

// client reads client id. An unknown or revoked client is ErrNotFound.
func (s *storage) clientRow(ctx context.Context, id string) (Client, error) {
	var c Client
	var uris string
	var principal, scope, lastUsed, revoked sql.NullString
	var registered string
	err := s.db.QueryRowContext(ctx, `SELECT id, name, redirect_uris, principal, scope, registered_at, last_used_at, revoked_at
		FROM oauth_clients WHERE id = ? AND revoked_at IS NULL`, id).Scan(&c.ID, &c.Name, &uris,
		&principal, &scope, &registered, &lastUsed, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrNotFound
	} else if err != nil {
		return Client{}, fmt.Errorf("oauth: read a client: %w", err)
	}
	c.Principal, c.Scope = principal.String, scope.String
	c.Registered, c.LastUsed, c.Revoked = parseTime(registered), parseTime(lastUsed.String), parseTime(revoked.String)
	if err := jsonUnmarshal([]byte(uris), &c.RedirectURIs); err != nil {
		return Client{}, fmt.Errorf("oauth: decode a client's redirect URIs: %w", err)
	}
	return c, nil
}

// clients returns the live clients of a principal, sorted by name.
func (s *storage) clients(ctx context.Context, principal string) ([]Client, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM oauth_clients WHERE principal = ? AND revoked_at IS NULL ORDER BY name, id`, principal)
	if err != nil {
		return nil, fmt.Errorf("oauth: read the clients: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []Client{}
	for _, id := range ids {
		c, err := s.clientRow(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// livePrincipal reports ErrNotFound unless name is a live principal.
func (s *storage) livePrincipal(ctx context.Context, name string) error {
	var revoked sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT revoked_at FROM principals WHERE name = ?`, name).Scan(&revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("oauth: read a principal: %w", err)
	case revoked.Valid:
		return ErrNotFound
	}
	return nil
}

// principals returns the live principals, with their clients.
func (s *storage) principals(ctx context.Context) ([]Principal, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, created_at, revoked_at FROM principals WHERE revoked_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("oauth: read the principals: %w", err)
	}
	defer rows.Close()
	out := []Principal{}
	for rows.Next() {
		var p Principal
		var created string
		var revoked sql.NullString
		if err := rows.Scan(&p.Name, &created, &revoked); err != nil {
			return nil, err
		}
		p.Created, p.Revoked = parseTime(created), parseTime(revoked.String)
		out = append(out, p)
	}
	return out, rows.Err()
}

// Grant returns the principal's items for app: the grant that limits what a
// client of the principal may do there.
func (s *Server) Grant(ctx context.Context, principal, app string) ([]Item, error) {
	var text string
	err := s.db.QueryRowContext(ctx, `SELECT items FROM grants WHERE principal = ? AND app = ?`, principal, app).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return []Item{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("oauth: read a grant: %w", err)
	}
	var items []Item
	if err := jsonUnmarshal([]byte(text), &items); err != nil {
		return nil, fmt.Errorf("oauth: decode a grant: %w", err)
	}
	if items == nil {
		items = []Item{}
	}
	return items, nil
}

// grants returns every grant of a principal, by app.
func (s *storage) grants(ctx context.Context, principal string) (map[string][]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT app, items FROM grants WHERE principal = ? ORDER BY app`, principal)
	if err != nil {
		return nil, fmt.Errorf("oauth: read the grants: %w", err)
	}
	defer rows.Close()
	out := map[string][]Item{}
	for rows.Next() {
		var app, text string
		if err := rows.Scan(&app, &text); err != nil {
			return nil, err
		}
		var items []Item
		if err := jsonUnmarshal([]byte(text), &items); err != nil {
			return nil, fmt.Errorf("oauth: decode a grant: %w", err)
		}
		out[app] = items
	}
	return out, rows.Err()
}

// addGrant adds items to the principal's grant for app, without adding an
// exact duplicate twice. It holds the database while it reads and writes, so
// that two decisions at once keep both sets of items.
func (s *Server) addGrant(ctx context.Context, principal, app string, items []Item) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("oauth: begin a grant: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	have := []Item{}
	var text string
	err = tx.QueryRowContext(ctx, `SELECT items FROM grants WHERE principal = ? AND app = ?`, principal, app).Scan(&text)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("oauth: read a grant: %w", err)
	default:
		if err := jsonUnmarshal([]byte(text), &have); err != nil {
			return fmt.Errorf("oauth: decode a grant: %w", err)
		}
	}
	for _, it := range normalize(items) {
		if !slices.ContainsFunc(have, func(h Item) bool { return sameItem(h, it) }) {
			have = append(have, it)
		}
	}
	encoded, err := jsonMarshal(have)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO grants (principal, app, items, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (principal, app) DO UPDATE SET items = excluded.items, updated_at = excluded.updated_at`,
		principal, app, string(encoded), stamp(s.now())); err != nil {
		return fmt.Errorf("oauth: write a grant: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("oauth: commit a grant: %w", err)
	}
	if s.onGrant != nil {
		s.onGrant(principal, app)
	}
	return nil
}

// sameItem reports whether two items are the same: the same capability, and
// the same path for every selector.
func sameItem(a, b Item) bool {
	if a.Cap != b.Cap || len(a.Sel) != len(b.Sel) {
		return false
	}
	for k, v := range a.Sel {
		if b.Sel[k] != v {
			return false
		}
	}
	return true
}

// Check answers what token gives, for a caller that needs scope: a live
// access token that has the scope, from a live, bound client of a live
// principal. It records the client's last use, at most once a minute.
func (s *Server) Check(ctx context.Context, token, scope string) (Access, error) {
	if token == "" {
		return Access{}, ErrNotFound
	}
	var needed []string
	if scope != "" {
		needed = []string{scope}
	}
	use, req, err := s.provider.IntrospectToken(ctx, token, fosite.AccessToken, NewSession(), needed...)
	if err != nil {
		return Access{}, fmt.Errorf("oauth: check a token: %w", err)
	}
	if use != fosite.AccessToken {
		return Access{}, fmt.Errorf("this is not an access token: %w", ErrNotFound)
	}
	c, err := s.store.clientRow(ctx, req.GetClient().GetID())
	if err != nil {
		return Access{}, err
	}
	if !c.Bound() {
		return Access{}, fmt.Errorf("client %s is bound to no principal: %w", c.ID, ErrNotFound)
	}
	if err := s.store.livePrincipal(ctx, c.Principal); err != nil {
		return Access{}, err
	}
	if subject := req.GetSession().GetSubject(); subject != c.Principal {
		return Access{}, fmt.Errorf("the token is for %q, not for the client's principal: %w", subject, ErrNotFound)
	}
	// The client's current scope limits the token, too: a revoked scope
	// stops working at once.
	if scope != "" && !has(scopes(c.Scope), scope) {
		return Access{}, fmt.Errorf("client %s has no scope %q: %w", c.ID, scope, ErrInvalid)
	}
	if err := s.touch(ctx, c.ID); err != nil {
		s.log.Warn("record a client's use", "client", c.ID, "err", err)
	}
	return Access{Principal: c.Principal, Client: c.ID, Scopes: append([]string{}, req.GetGrantedScopes()...)}, nil
}

// touch records a client's last use, at most once a minute.
func (s *Server) touch(ctx context.Context, id string) error {
	now := s.now().UTC()
	_, err := s.db.ExecContext(ctx, `UPDATE oauth_clients SET last_used_at = ?
		WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
		stamp(now), id, stamp(now.Add(-lastUseEvery)))
	if err != nil {
		return fmt.Errorf("oauth: record a client's use: %w", err)
	}
	return nil
}

// RevokeClient revokes client id: its tokens stop working at once, and its
// pending requests go.
func (s *Server) RevokeClient(ctx context.Context, id string) error {
	c, err := s.store.clientRow(ctx, id)
	if err != nil {
		return err
	}
	if err := s.revokeClient(ctx, c, stamp(s.now())); err != nil {
		return err
	}
	if s.onRevoke != nil {
		s.onRevoke(c.Principal, []string{c.ID})
	}
	s.log.Info("oauth client revoked", "client", c.ID, "principal", c.Principal)
	return nil
}

// revokeClient marks a client revoked and deletes its tokens and pending
// requests.
func (s *Server) revokeClient(ctx context.Context, c Client, now string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE oauth_clients SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, c.ID); err != nil {
		return fmt.Errorf("oauth: revoke a client: %w", err)
	}
	if err := s.store.deleteClientSessions(ctx, c.ID); err != nil {
		return err
	}
	return s.deleteRequests(ctx, c.ID)
}

// RevokePrincipal revokes a principal: each of its clients, its grants and
// its requests go, and its name may be approved again with no grant.
func (s *Server) RevokePrincipal(ctx context.Context, name string) error {
	if err := s.store.livePrincipal(ctx, name); err != nil {
		return err
	}
	clients, err := s.store.clients(ctx, name)
	if err != nil {
		return err
	}
	now := stamp(s.now())
	ids := make([]string, 0, len(clients))
	for _, c := range clients {
		if err := s.revokeClient(ctx, c, now); err != nil {
			return err
		}
		ids = append(ids, c.ID)
	}
	// A client of a revoked principal holds no grant: deleting them keeps
	// the next approval of the name empty, as it must be.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM grants WHERE principal = ?`, name); err != nil {
		return fmt.Errorf("oauth: delete a principal's grants: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE principals SET revoked_at = ? WHERE name = ?`, now, name); err != nil {
		return fmt.Errorf("oauth: revoke a principal: %w", err)
	}
	if s.onRevoke != nil {
		s.onRevoke(name, ids)
	}
	s.log.Info("oauth principal revoked", "principal", name, "clients", len(ids))
	return nil
}

// principalLastUse is the latest use of a principal's clients.
func principalLastUse(clients []Client) time.Time {
	var latest time.Time
	for _, c := range clients {
		if c.LastUsed.After(latest) {
			latest = c.LastUsed
		}
	}
	return latest
}
