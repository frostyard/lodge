package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/frostyard/lodge/internal/apps"
)

// Kind is what a consent request asks for.
type Kind string

const (
	// KindSignin is a sign-in: a client, a principal and scopes.
	KindSignin Kind = "signin"
	// KindAccess is an access request: a client, its principal, an app and
	// the items it asks for.
	KindAccess Kind = "access"
)

// The decisions a request takes.
const (
	// DecisionApprove grants what the page showed.
	DecisionApprove = "approve"
	// DecisionDecline refuses it.
	DecisionDecline = "decline"
)

// Item is one entry of a grant: a capability of an app, and a path for each
// of the app's selectors it names (docs/specs/oauth.md, Principals, clients
// and grants).
type Item struct {
	Cap string            `json:"cap"`
	Sel map[string]string `json:"sel,omitzero"`
}

// Request is what the consent page shows and an admin decides.
type Request struct {
	ID        string    `json:"id"`
	Kind      Kind      `json:"kind"`
	Client    string    `json:"client"`
	Principal string    `json:"principal"`
	App       string    `json:"app"`
	Scopes    []string  `json:"scopes"`
	Items     []Item    `json:"items"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	Decided   time.Time `json:"decided,omitzero"`
	Decision  string    `json:"decision,omitzero"`
	// URL is where the request is decided: ISSUER/oauth/consent/<id>.
	URL string `json:"url,omitzero"`
	// Query is a sign-in's authorization request, as it is stored.
	Query url.Values `json:"-"`
}

// canonical is the JSON the request hash is taken over: the members in this
// order, the scopes sorted, the items in the request's order, and no
// spaces.
func (r Request) canonical() ([]byte, error) {
	scopes := append([]string{}, r.Scopes...)
	slices.Sort(scopes)
	items := r.Items
	if items == nil {
		items = []Item{}
	}
	return jsonMarshal(struct {
		ID        string   `json:"id"`
		Kind      Kind     `json:"kind"`
		Client    string   `json:"client"`
		Principal string   `json:"principal"`
		App       string   `json:"app"`
		Scopes    []string `json:"scopes"`
		Items     []Item   `json:"items"`
	}{r.ID, r.Kind, r.Client, r.Principal, r.App, scopes, items})
}

// Hash is the request's SHA-256, in lowercase hex: what the consent page
// holds and lodge refuses a decision without.
func (r Request) Hash() string {
	b, err := r.canonical()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// principalPattern is the form of a principal's name.
var principalPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidPrincipal reports whether name may name a principal.
func ValidPrincipal(name string) bool { return principalPattern.MatchString(name) }

// ProposePrincipal is the principal lodge suggests for a client name: its
// lower case, with every character that is not a to z, 0 to 9 or a dash
// dropped, cut to 32 characters, when that is a valid principal, and
// nothing otherwise.
func ProposePrincipal(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	p := b.String()
	if len(p) > 32 {
		p = p[:32]
	}
	if !ValidPrincipal(p) {
		return ""
	}
	return p
}

// newID is 32 lowercase hex digits: 16 random bytes.
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth: make an ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// validItem checks an item against the app's hearth manifest: its capability
// is one of the app's, each selector name one of the app's, and each path
// valid.
func validItem(a apps.App, it Item) error {
	if a.Hearth == nil {
		return fmt.Errorf("app %s has no hearth manifest: %w", a.ID, ErrInvalid)
	}
	if !slices.Contains(a.Hearth.Capabilities, it.Cap) {
		return fmt.Errorf("capability %q is not one of app %s's: %w", it.Cap, a.ID, ErrInvalid)
	}
	for name, path := range it.Sel {
		if !slices.Contains(a.Hearth.Selectors, name) {
			return fmt.Errorf("selector %q is not one of app %s's: %w", name, a.ID, ErrInvalid)
		}
		if !ValidPath(path) {
			return fmt.Errorf("path %q of selector %q is not a path: %w", path, name, ErrInvalid)
		}
	}
	return nil
}

// request is a stored consent_requests row.
func (s *Server) createRequest(ctx context.Context, req Request) error {
	scopesJSON, err := jsonMarshal(req.Scopes)
	if err != nil {
		return err
	}
	itemsJSON, err := jsonMarshal(req.Items)
	if err != nil {
		return err
	}
	query := ""
	if req.Query != nil {
		b, err := jsonMarshal(req.Query)
		if err != nil {
			return err
		}
		query = string(b)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO consent_requests
		(id, kind, client_id, principal, app, scopes, items, query, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.ID, string(req.Kind), req.Client, req.Principal, req.App, string(scopesJSON), string(itemsJSON),
		query, stamp(req.Created), stamp(req.Expires))
	if err != nil {
		return fmt.Errorf("oauth: store a request: %w", err)
	}
	return nil
}

// request reads request id.
func (s *Server) request(ctx context.Context, id string) (Request, error) {
	var req Request
	var kind, scopesJSON, itemsJSON, query, created, expires string
	var decided, decision sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, client_id, principal, app, scopes, items, query, created_at, expires_at, decided_at, decision
		FROM consent_requests WHERE id = ?`, id).Scan(&req.ID, &kind, &req.Client, &req.Principal, &req.App,
		&scopesJSON, &itemsJSON, &query, &created, &expires, &decided, &decision)
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	} else if err != nil {
		return Request{}, fmt.Errorf("oauth: read a request: %w", err)
	}
	req.Kind = Kind(kind)
	req.Created, req.Expires = parseTime(created), parseTime(expires)
	req.Decided, req.Decision = parseTime(decided.String), decision.String
	if err := jsonUnmarshal([]byte(scopesJSON), &req.Scopes); err != nil {
		return Request{}, fmt.Errorf("oauth: decode a request's scopes: %w", err)
	}
	if err := jsonUnmarshal([]byte(itemsJSON), &req.Items); err != nil {
		return Request{}, fmt.Errorf("oauth: decode a request's items: %w", err)
	}
	if query != "" {
		if err := jsonUnmarshal([]byte(query), &req.Query); err != nil {
			return Request{}, fmt.Errorf("oauth: decode a request's query: %w", err)
		}
	}
	req.URL = s.Issuer() + "/oauth/consent/" + req.ID
	return req, nil
}

// pending is how many requests wait for a decision.
func (s *Server) pending(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM consent_requests WHERE decided_at IS NULL AND expires_at > ?`,
		stamp(s.now())).Scan(&n); err != nil {
		return 0, fmt.Errorf("oauth: count the pending requests: %w", err)
	}
	return n, nil
}

// pendingRequests returns the requests that wait for a decision, oldest
// first.
func (s *Server) pendingRequests(ctx context.Context) ([]Request, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM consent_requests WHERE decided_at IS NULL AND expires_at > ? ORDER BY created_at`,
		stamp(s.now()))
	if err != nil {
		return nil, fmt.Errorf("oauth: read the pending requests: %w", err)
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
	out := []Request{}
	for _, id := range ids {
		req, err := s.request(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, nil
}

// decide records a decision, and reports whether this call made it: a
// request is decided once, while it is pending and unexpired.
func (s *Server) decide(ctx context.Context, id, decision string) (bool, error) {
	now := stamp(s.now())
	res, err := s.db.ExecContext(ctx, `UPDATE consent_requests SET decided_at = ?, decision = ?
		WHERE id = ? AND decided_at IS NULL AND expires_at > ?`, now, decision, id, now)
	if err != nil {
		return false, fmt.Errorf("oauth: decide a request: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("oauth: decide a request: %w", err)
	}
	return n == 1, nil
}

// deleteRequests deletes a client's pending requests, and its decided ones.
func (s *Server) deleteRequests(ctx context.Context, clientID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM consent_requests WHERE client_id = ?`, clientID); err != nil {
		return fmt.Errorf("oauth: delete a client's requests: %w", err)
	}
	return nil
}

// RequestAccess makes an access request: a bound, live client asks for items
// of an installed, enabled app with a hearth manifest
// (docs/specs/oauth.md, Requests).
func (s *Server) RequestAccess(ctx context.Context, clientID, app string, items []Item) (Request, error) {
	c, err := s.store.clientRow(ctx, clientID)
	if err != nil {
		return Request{}, err
	}
	if c.Principal == "" {
		return Request{}, fmt.Errorf("client %s is bound to no principal: %w", clientID, ErrNotFound)
	}
	if err := s.store.livePrincipal(ctx, c.Principal); err != nil {
		return Request{}, err
	}
	a, ok := s.apps.Get(app)
	if !ok || !a.Enabled() || a.Hearth == nil {
		return Request{}, fmt.Errorf("app %q is not installed with a hearth manifest: %w", app, ErrNotFound)
	}
	if len(items) < 1 || len(items) > MaxItems {
		return Request{}, fmt.Errorf("an access request holds 1 to %d items: %w", MaxItems, ErrInvalid)
	}
	for i, it := range items {
		if err := validItem(a, it); err != nil {
			return Request{}, fmt.Errorf("item %d: %w", i, err)
		}
	}
	n, err := s.pending(ctx)
	if err != nil {
		return Request{}, err
	}
	if n >= MaxPending {
		return Request{}, fmt.Errorf("%d requests are pending: %w", n, ErrTooMany)
	}
	id, err := newID()
	if err != nil {
		return Request{}, err
	}
	now := s.now().UTC()
	req := Request{ID: id, Kind: KindAccess, Client: c.ID, Principal: c.Principal, App: app,
		Scopes: []string{}, Items: normalize(items), Created: now, Expires: now.Add(RequestLifespan),
		URL: s.Issuer() + "/oauth/consent/" + id}
	if err := s.createRequest(ctx, req); err != nil {
		return Request{}, err
	}
	s.log.Info("access requested", "request", id, "client", c.ID, "principal", c.Principal, "app", app, "items", len(items))
	return req, nil
}

// normalize drops an empty selector map from each item, so that the request
// hash and the stored grant read the same.
func normalize(items []Item) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if len(it.Sel) == 0 {
			it.Sel = nil
		}
		out = append(out, it)
	}
	return out
}

// createSigninRequest records a sign-in for a client, whose authorization
// request is query, and returns it.
func (s *Server) createSigninRequest(ctx context.Context, clientID string, scopes []string, principal string, query url.Values) (Request, error) {
	n, err := s.pending(ctx)
	if err != nil {
		return Request{}, err
	}
	if n >= MaxPending {
		return Request{}, fmt.Errorf("%d requests are pending: %w", n, ErrTooMany)
	}
	id, err := newID()
	if err != nil {
		return Request{}, err
	}
	now := s.now().UTC()
	req := Request{ID: id, Kind: KindSignin, Client: clientID, Principal: principal, App: "",
		Scopes: scopes, Items: []Item{}, Created: now, Expires: now.Add(RequestLifespan), Query: query,
		URL: s.Issuer() + "/oauth/consent/" + id}
	if err := s.createRequest(ctx, req); err != nil {
		return Request{}, err
	}
	return req, nil
}

// expires reports whether the request may no longer be decided.
func (r Request) expires(now time.Time) bool { return !now.Before(r.Expires) }

// approveSignin binds the client to the principal with the scopes, and
// creates or revives the principal.
func (s *Server) approveSignin(ctx context.Context, req Request, principal string, scopes []string) error {
	if !ValidPrincipal(principal) {
		return fmt.Errorf("principal %q is not a name: %w", principal, ErrInvalid)
	}
	if len(scopes) == 0 {
		return fmt.Errorf("a sign-in needs 1 or more scopes: %w", ErrInvalid)
	}
	for _, sc := range scopes {
		if !slices.Contains(req.Scopes, sc) {
			return fmt.Errorf("scope %q was not asked for: %w", sc, ErrInvalid)
		}
	}
	if err := s.makePrincipal(ctx, principal); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE oauth_clients SET principal = ?, scope = ? WHERE id = ?`,
		principal, joinScopes(scopes), req.Client); err != nil {
		return fmt.Errorf("oauth: bind a client: %w", err)
	}
	return nil
}

// makePrincipal creates the principal, or revives a revoked one, which then
// starts with no grant.
func (s *Server) makePrincipal(ctx context.Context, name string) error {
	now := stamp(s.now())
	var revoked sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT revoked_at FROM principals WHERE name = ?`, name).Scan(&revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := s.db.ExecContext(ctx, `INSERT INTO principals (name, created_at) VALUES (?, ?)`, name, now); err != nil {
			return fmt.Errorf("oauth: make a principal: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("oauth: read a principal: %w", err)
	case !revoked.Valid:
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE principals SET revoked_at = NULL, created_at = ? WHERE name = ?`, now, name); err != nil {
		return fmt.Errorf("oauth: revive a principal: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM grants WHERE principal = ?`, name); err != nil {
		return fmt.Errorf("oauth: clear a revived principal's grants: %w", err)
	}
	return nil
}

// boundPrincipal is the principal a client is bound to, or "".
func (s *Server) boundPrincipal(ctx context.Context, clientID string) (string, error) {
	var principal sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT principal FROM oauth_clients WHERE id = ? AND revoked_at IS NULL`, clientID).Scan(&principal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("oauth: read a client: %w", err)
	}
	return principal.String, nil
}
