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

// Access is what a live access token gives: the principal, the client, the
// scopes the consent approved and the token's expiry (docs/specs/oauth.md,
// lodge's own callers).
type Access struct {
	Principal string   `json:"principal"`
	Client    string   `json:"client"`
	Scopes    []string `json:"scopes"`
	// Expires is the access token's expiry.
	Expires time.Time `json:"expires"`
}

// clientRow reads client id. An unknown or revoked client is ErrNotFound.
func (s *storage) clientRow(ctx context.Context, id string) (Client, error) {
	return s.clientRowAt(ctx, nil, id)
}

// clientRowAt reads client id in q: a transaction, fosite's, or the
// database. An unknown or revoked client is ErrNotFound.
func (s *storage) clientRowAt(ctx context.Context, q querier, id string) (Client, error) {
	var c Client
	var uris string
	var principal, scope, lastUsed, revoked sql.NullString
	var registered string
	err := s.at(ctx, q).QueryRowContext(ctx, `SELECT id, name, redirect_uris, principal, scope, registered_at, last_used_at, revoked_at
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
func (s *storage) clients(ctx context.Context, q querier, principal string) ([]Client, error) {
	rows, err := s.at(ctx, q).QueryContext(ctx, `SELECT id FROM oauth_clients WHERE principal = ? AND revoked_at IS NULL ORDER BY name, id`, principal)
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
		c, err := s.clientRowAt(ctx, q, id)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// livePrincipal reports ErrNotFound unless name is a live principal.
func (s *storage) livePrincipal(ctx context.Context, q querier, name string) error {
	var revoked sql.NullString
	err := s.at(ctx, q).QueryRowContext(ctx, `SELECT revoked_at FROM principals WHERE name = ?`, name).Scan(&revoked)
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

// principals returns the live principals.
func (s *storage) principals(ctx context.Context) ([]Principal, error) {
	rows, err := s.query(ctx, `SELECT name, created_at, revoked_at FROM principals WHERE revoked_at IS NULL ORDER BY name`)
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

// Live reports whether name is a live principal: lodge knows it, and it is not
// revoked. The MCP gateway drops a revoked principal's sessions by it
// (ADR-0024).
func (s *Server) Live(ctx context.Context, name string) (bool, error) {
	err := s.store.livePrincipal(ctx, nil, name)
	switch {
	case errors.Is(err, ErrNotFound):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// grantItem is 1 stored item of a grant: the item, and the app's
// compatibility line that its approval was for (apps.HearthMajor). An item
// stored before lodge recorded the line has none, and is in force for no
// line.
type grantItem struct {
	Cap   string            `json:"cap"`
	Sel   map[string]string `json:"sel,omitzero"`
	Major string            `json:"major,omitzero"`
}

// item is the stored item without its line: what a grant gives.
func (g grantItem) item() Item { return Item{Cap: g.Cap, Sel: g.Sel} }

// decodeGrant decodes a grant's stored items.
func decodeGrant(text string) ([]grantItem, error) {
	var items []grantItem
	if err := jsonUnmarshal([]byte(text), &items); err != nil {
		return nil, fmt.Errorf("oauth: decode a grant: %w", err)
	}
	if items == nil {
		items = []grantItem{}
	}
	return items, nil
}

// appMajor is app's current compatibility line, or "" when it is not
// installed with a hearth manifest.
func (s *Server) appMajor(app string) string {
	a, ok := s.apps.Get(app)
	if !ok || a.Hearth == nil {
		return ""
	}
	return a.Hearth.Major
}

// Grant returns the principal's items for app that are in force: the grant
// that limits what a client of the principal may do there. An item is in
// force only for the app's compatibility line that its approval was for: a
// new major version of the app needs a new consent (docs/specs/oauth.md,
// Principals, clients and grants). An app that is not installed with a
// hearth manifest has no item in force.
func (s *Server) Grant(ctx context.Context, principal, app string) ([]Item, error) {
	var text string
	err := s.store.queryRow(ctx, `SELECT items FROM grants WHERE principal = ? AND app = ?`, principal, app).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return []Item{}, nil
	} else if err != nil {
		return nil, fmt.Errorf("oauth: read a grant: %w", err)
	}
	stored, err := decodeGrant(text)
	if err != nil {
		return nil, err
	}
	return inForce(stored, s.appMajor(app)), nil
}

// inForce returns the items approved for the compatibility line major; none
// when major is "".
func inForce(stored []grantItem, major string) []Item {
	items := []Item{}
	if major == "" {
		return items
	}
	for _, it := range stored {
		if it.Major == major {
			items = append(items, it.item())
		}
	}
	return items
}

// Client returns the live client id: an OAuth client of lodge, revoked or
// unknown ones are ErrNotFound (docs/specs/oauth.md, Clients).
func (s *Server) Client(ctx context.Context, id string) (Client, error) {
	return s.store.clientRow(ctx, id)
}

// grants returns every grant of a principal, by app, as stored: the items
// that are in force and the ones that wait for a new consent.
func (s *storage) grants(ctx context.Context, principal string) (map[string][]grantItem, error) {
	rows, err := s.query(ctx, `SELECT app, items FROM grants WHERE principal = ? ORDER BY app`, principal)
	if err != nil {
		return nil, fmt.Errorf("oauth: read the grants: %w", err)
	}
	defer rows.Close()
	out := map[string][]grantItem{}
	for rows.Next() {
		var app, text string
		if err := rows.Scan(&app, &text); err != nil {
			return nil, err
		}
		items, err := decodeGrant(text)
		if err != nil {
			return nil, err
		}
		out[app] = items
	}
	return out, rows.Err()
}

// addGrant adds items, approved for the app's compatibility line major, to
// the principal's grant for app in q, without adding an exact duplicate
// twice: an item that is stored already takes major, so a new consent puts
// it in force again for the app's new major version. An approval calls it in
// the request's own transaction, so the decision and the grant go together.
func (s *Server) addGrant(ctx context.Context, q querier, principal, app string, items []Item, major string) error {
	if major == "" {
		return fmt.Errorf("an approval needs the app's major version: %w", ErrInvalid)
	}
	have := []grantItem{}
	var text string
	err := q.QueryRowContext(ctx, `SELECT items FROM grants WHERE principal = ? AND app = ?`, principal, app).Scan(&text)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return fmt.Errorf("oauth: read a grant: %w", err)
	default:
		if have, err = decodeGrant(text); err != nil {
			return err
		}
	}
	for _, it := range normalize(items) {
		i := slices.IndexFunc(have, func(h grantItem) bool { return sameItem(h.item(), it) })
		if i < 0 {
			have = append(have, grantItem{Cap: it.Cap, Sel: it.Sel, Major: major})
			continue
		}
		have[i].Major = major
	}
	encoded, err := jsonMarshal(have)
	if err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO grants (principal, app, items, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (principal, app) DO UPDATE SET items = excluded.items, updated_at = excluded.updated_at`,
		principal, app, string(encoded), stamp(s.now())); err != nil {
		return fmt.Errorf("oauth: write a grant: %w", err)
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

// RemoveGrantItem removes 1 item of the principal's grant for app, the one
// whose ItemHash is hash, and deletes the grant when no item is left
// (ADR-0024). A live principal is required. It is 1 transaction, and the
// hook runs after the commit.
func (s *Server) RemoveGrantItem(ctx context.Context, principal, app, hash string) error {
	left := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.store.livePrincipal(ctx, tx, principal); err != nil {
			return err
		}
		var text string
		switch err := tx.QueryRowContext(ctx, `SELECT items FROM grants WHERE principal = ? AND app = ?`,
			principal, app).Scan(&text); {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("principal %s has no grant for app %s: %w", principal, app, ErrNotFound)
		case err != nil:
			return fmt.Errorf("oauth: read a grant: %w", err)
		}
		stored, err := decodeGrant(text)
		if err != nil {
			return err
		}
		kept, found := make([]grantItem, 0, len(stored)), false
		for _, it := range stored {
			if ItemHash(it.item()) == hash {
				found = true
				continue
			}
			kept = append(kept, it)
		}
		if !found {
			return fmt.Errorf("item %q is not in the grant of principal %s for app %s: %w", hash, principal, app, ErrNotFound)
		}
		if len(kept) == 0 {
			if _, err := tx.ExecContext(ctx, `DELETE FROM grants WHERE principal = ? AND app = ?`, principal, app); err != nil {
				return fmt.Errorf("oauth: delete a grant: %w", err)
			}
			return nil
		}
		encoded, err := jsonMarshal(kept)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE grants SET items = ?, updated_at = ? WHERE principal = ? AND app = ?`,
			string(encoded), stamp(s.now()), principal, app); err != nil {
			return fmt.Errorf("oauth: write a grant: %w", err)
		}
		left = len(kept)
		return nil
	})
	if err != nil {
		return err
	}
	if s.onGrant != nil {
		s.onGrant(principal, app)
	}
	s.log.Info("grant item removed", "principal", principal, "app", app, "items", left)
	return nil
}

// Check answers what token gives, for a caller that needs scope: a live
// access token that has the scope, from a live, bound client of a live
// principal. A live token that lacks the scope is ErrScope, never
// ErrNotFound: a caller answers that with 403, not with a new sign-in. It
// records the client's last use, at most once a minute.
func (s *Server) Check(ctx context.Context, token, scope string) (Access, error) {
	if token == "" {
		return Access{}, ErrNotFound
	}
	// The introspection checks the token itself; the scopes are compared
	// here, because fosite reports a missing scope as an invalid token.
	use, req, err := s.provider.IntrospectToken(ctx, token, fosite.AccessToken, NewSession())
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
	if err := s.store.livePrincipal(ctx, nil, c.Principal); err != nil {
		return Access{}, err
	}
	if subject := req.GetSession().GetSubject(); subject != c.Principal {
		return Access{}, fmt.Errorf("the token is for %q, not for the client's principal: %w", subject, ErrNotFound)
	}
	// The scopes the consent approved and the client's current scope both
	// limit the token: a revoked scope stops working at once.
	if scope != "" && !has(req.GetGrantedScopes(), scope) {
		return Access{}, fmt.Errorf("the token was not granted scope %q: %w", scope, ErrScope)
	}
	if scope != "" && !has(scopes(c.Scope), scope) {
		return Access{}, fmt.Errorf("client %s has no scope %q: %w", c.ID, scope, ErrScope)
	}
	if err := s.touch(ctx, c.ID); err != nil {
		s.log.Warn("record a client's use", "client", c.ID, "err", err)
	}
	return Access{Principal: c.Principal, Client: c.ID, Scopes: append([]string{}, req.GetGrantedScopes()...),
		Expires: req.GetSession().GetExpiresAt(fosite.AccessToken)}, nil
}

// touch records a client's last use, at most once a minute.
func (s *Server) touch(ctx context.Context, id string) error {
	now := s.now().UTC()
	_, err := s.store.exec(ctx, `UPDATE oauth_clients SET last_used_at = ?
		WHERE id = ? AND (last_used_at IS NULL OR last_used_at <= ?)`,
		stamp(now), id, stamp(now.Add(-lastUseEvery)))
	if err != nil {
		return fmt.Errorf("oauth: record a client's use: %w", err)
	}
	return nil
}

// RevokeClient revokes client id: its tokens stop working at once, and its
// pending requests go. The cascade is 1 transaction, and its hook runs after
// the commit.
func (s *Server) RevokeClient(ctx context.Context, id string) error {
	var c Client
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if c, err = s.store.clientRowAt(ctx, tx, id); err != nil {
			return err
		}
		return s.revokeClient(ctx, tx, c, stamp(s.now()))
	})
	if err != nil {
		return err
	}
	if s.onRevoke != nil {
		s.onRevoke(c.Principal, []string{c.ID})
	}
	s.log.Info("oauth client revoked", "client", c.ID, "principal", c.Principal)
	return nil
}

// revokeClient marks a client revoked in q, and deletes its tokens and
// pending requests.
func (s *Server) revokeClient(ctx context.Context, q querier, c Client, now string) error {
	if _, err := s.store.at(ctx, q).ExecContext(ctx,
		`UPDATE oauth_clients SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, now, c.ID); err != nil {
		return fmt.Errorf("oauth: revoke a client: %w", err)
	}
	if err := s.store.deleteClientSessions(ctx, q, c.ID); err != nil {
		return err
	}
	return s.deleteRequests(ctx, q, c.ID)
}

// RevokePrincipal revokes a principal: each of its clients, its grants and
// its requests go, and its name may be approved again with no grant.
func (s *Server) RevokePrincipal(ctx context.Context, name string) error {
	var ids []string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := s.store.livePrincipal(ctx, tx, name); err != nil {
			return err
		}
		clients, err := s.store.clients(ctx, tx, name)
		if err != nil {
			return err
		}
		now := stamp(s.now())
		for _, c := range clients {
			if err := s.revokeClient(ctx, tx, c, now); err != nil {
				return err
			}
			ids = append(ids, c.ID)
		}
		if s.beforePrincipalRevoked != nil {
			if err := s.beforePrincipalRevoked(); err != nil {
				return err
			}
		}
		// A revoked principal holds no grant: deleting them keeps the next
		// approval of the name empty, as it must be.
		if _, err := tx.ExecContext(ctx, `DELETE FROM grants WHERE principal = ?`, name); err != nil {
			return fmt.Errorf("oauth: delete a principal's grants: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE principals SET revoked_at = ? WHERE name = ?`, now, name); err != nil {
			return fmt.Errorf("oauth: revoke a principal: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
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
