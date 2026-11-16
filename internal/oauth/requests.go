package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json/v2"
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
	ID        string   `json:"id"`
	Kind      Kind     `json:"kind"`
	Client    string   `json:"client"`
	Principal string   `json:"principal"`
	App       string   `json:"app"`
	Scopes    []string `json:"scopes"`
	Items     []Item   `json:"items"`
	// Major is an access request's app compatibility line when it was made
	// (apps.HearthMajor): an approval grants its items for that line only.
	Major    string    `json:"major,omitzero"`
	Created  time.Time `json:"created"`
	Expires  time.Time `json:"expires"`
	Decided  time.Time `json:"decided,omitzero"`
	Decision string    `json:"decision,omitzero"`
	// URL is where the request is decided: ISSUER/oauth/consent/<id>.
	URL string `json:"url,omitzero"`
	// Query is a sign-in's authorization request, as it is stored.
	Query url.Values `json:"-"`
}

// canonical is the JSON the request hash is taken over: the members in this
// order, the scopes sorted, the keys of each sel sorted, the items in the
// request's order, and no spaces. It is deterministic: a map's order is not.
func (r Request) canonical() ([]byte, error) {
	scopes := append([]string{}, r.Scopes...)
	slices.Sort(scopes)
	items := r.Items
	if items == nil {
		items = []Item{}
	}
	return json.Marshal(struct {
		ID        string   `json:"id"`
		Kind      Kind     `json:"kind"`
		Client    string   `json:"client"`
		Principal string   `json:"principal"`
		App       string   `json:"app"`
		Scopes    []string `json:"scopes"`
		Items     []Item   `json:"items"`
	}{r.ID, r.Kind, r.Client, r.Principal, r.App, scopes, items}, json.Deterministic(true))
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

// canonicalItem is the JSON of 1 item: the members in this order, no spaces,
// the keys of its sel sorted, and no sel member when the item has no selector.
// It is deterministic, and it is the form the items of a request's hash take.
func canonicalItem(it Item) ([]byte, error) {
	if len(it.Sel) == 0 {
		it.Sel = nil
	}
	return json.Marshal(it, json.Deterministic(true))
}

// ItemHash is the item's SHA-256, in lowercase hex: what the grants page holds
// for 1 item of a grant, and what RemoveGrantItem removes by.
func ItemHash(it Item) string {
	b, err := canonicalItem(it)
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

// createRequest stores req in 1 statement that re-checks everything the
// entry needs: its client is live, an access request's client is still bound
// to its principal and that principal is live, and fewer than MaxPending
// requests are pending. A check and its insert cannot be split, so a
// revocation cannot slip between them (docs/specs/oauth.md, Requests).
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
	statement := `INSERT INTO consent_requests
		(id, kind, client_id, principal, app, scopes, items, query, major, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM oauth_clients WHERE id = ? AND revoked_at IS NULL`
	args := []any{req.ID, string(req.Kind), req.Client, req.Principal, req.App, string(scopesJSON), string(itemsJSON),
		query, req.Major, stamp(req.Created), stamp(req.Expires), req.Client}
	if req.Kind == KindAccess {
		statement += ` AND principal = ?`
		args = append(args, req.Principal)
	}
	statement += `)`
	if req.Kind == KindAccess {
		statement += ` AND EXISTS (SELECT 1 FROM principals WHERE name = ? AND revoked_at IS NULL)`
		args = append(args, req.Principal)
	}
	statement += ` AND (SELECT COUNT(*) FROM consent_requests WHERE decided_at IS NULL AND expires_at > ?) < ?`
	args = append(args, stamp(s.now()), MaxPending)
	res, err := s.store.at(ctx, nil).ExecContext(ctx, statement, args...)
	if err != nil {
		return fmt.Errorf("oauth: store a request: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("oauth: store a request: %w", err)
	} else if n == 0 {
		return s.requestRefused(ctx, req)
	}
	return nil
}

// requestRefused says why a request was not stored: its client is unknown,
// revoked or not bound to it; an access request's principal is revoked; or
// too many requests wait for a decision.
func (s *Server) requestRefused(ctx context.Context, req Request) error {
	c, err := s.store.clientRow(ctx, req.Client)
	if err != nil {
		return err
	}
	if req.Kind == KindAccess {
		if c.Principal != req.Principal {
			return fmt.Errorf("client %s is bound to principal %s: %w", req.Client, c.Principal, ErrNotFound)
		}
		if err := s.store.livePrincipal(ctx, nil, req.Principal); err != nil {
			return err
		}
	}
	return fmt.Errorf("%d requests are pending: %w", MaxPending, ErrTooMany)
}

// request reads request id.
func (s *Server) request(ctx context.Context, id string) (Request, error) {
	var req Request
	var kind, scopesJSON, itemsJSON, query, created, expires string
	var decided, decision sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, client_id, principal, app, scopes, items, query, major, created_at, expires_at, decided_at, decision
		FROM consent_requests WHERE id = ?`, id).Scan(&req.ID, &kind, &req.Client, &req.Principal, &req.App,
		&scopesJSON, &itemsJSON, &query, &req.Major, &created, &expires, &decided, &decision)
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

// decide records a decision in q, and reports whether this call made it: a
// request is decided once, while it is pending and unexpired.
func (s *Server) decide(ctx context.Context, q querier, id, decision string) (bool, error) {
	now := stamp(s.now())
	res, err := q.ExecContext(ctx, `UPDATE consent_requests SET decided_at = ?, decision = ?
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

// deleteRequests deletes a client's pending requests, and its decided ones,
// in q.
func (s *Server) deleteRequests(ctx context.Context, q querier, clientID string) error {
	if _, err := s.store.at(ctx, q).ExecContext(ctx, `DELETE FROM consent_requests WHERE client_id = ?`, clientID); err != nil {
		return fmt.Errorf("oauth: delete a client's requests: %w", err)
	}
	return nil
}

// RequestAccess makes an access request: a bound, live client asks for items
// of an installed, enabled app with a hearth manifest, for the app's current
// compatibility line (docs/specs/oauth.md, Requests).
func (s *Server) RequestAccess(ctx context.Context, clientID, app string, items []Item) (Request, error) {
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
	c, err := s.store.clientRow(ctx, clientID)
	if err != nil {
		return Request{}, err
	}
	if c.Principal == "" {
		return Request{}, fmt.Errorf("client %s is bound to no principal: %w", clientID, ErrNotFound)
	}
	if err := s.store.livePrincipal(ctx, nil, c.Principal); err != nil {
		return Request{}, err
	}
	if s.beforeRequestInsert != nil {
		s.beforeRequestInsert()
	}
	id, err := newID()
	if err != nil {
		return Request{}, err
	}
	now := s.now().UTC()
	req := Request{ID: id, Kind: KindAccess, Client: clientID, Principal: c.Principal, App: app, Scopes: []string{},
		Items: normalize(items), Major: a.Hearth.Major, Created: now, Expires: now.Add(RequestLifespan), URL: s.Issuer() + "/oauth/consent/" + id}
	// The insert re-checks the client, its binding and the principal, so a
	// revocation between the check and it leaves no request.
	if err := s.createRequest(ctx, req); err != nil {
		return Request{}, err
	}
	s.log.Info("access requested", "request", id, "client", clientID, "principal", req.Principal, "app", app, "items", len(items))
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

// goneError is a request whose client or principal is revoked: it can only
// be declined, and usually a revocation deleted it already.
type goneError struct{ line string }

func (e *goneError) Error() string { return e.line }

// checkAccessLive re-checks, in an approval's transaction, that the request's
// client is live and bound to its principal, and that the principal is live.
func (s *Server) checkAccessLive(ctx context.Context, q querier, req Request) error {
	c, err := s.store.clientRowAt(ctx, q, req.Client)
	if errors.Is(err, ErrNotFound) {
		return &goneError{line: "client " + req.Client + " is revoked: decline this request"}
	} else if err != nil {
		return err
	}
	if c.Principal != req.Principal {
		return &boundError{client: req.Client, principal: c.Principal}
	}
	if err := s.store.livePrincipal(ctx, q, req.Principal); err != nil {
		if errors.Is(err, ErrNotFound) {
			return &goneError{line: "principal " + req.Principal + " is revoked: decline this request"}
		}
		return err
	}
	return nil
}

// boundError is a client that another approval bound to another principal:
// the request can only be declined.
type boundError struct {
	client, principal string
}

func (e *boundError) Error() string {
	return fmt.Sprintf("client %s is bound to principal %s: decline this request", e.client, e.principal)
}

// approveSignin binds the client to the principal with the scopes, and
// creates or revives the principal, in q: with the decision, in 1
// transaction. A client another approval bound stays as it is, and
// boundError says so.
func (s *Server) approveSignin(ctx context.Context, q querier, req Request, principal string, scopes []string) error {
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
	if err := s.makePrincipal(ctx, q, principal); err != nil {
		return err
	}
	// A client is bound by its first approved sign-in, and never rebound.
	res, err := q.ExecContext(ctx, `UPDATE oauth_clients SET principal = ?, scope = ?
		WHERE id = ? AND revoked_at IS NULL AND (principal IS NULL OR principal = ?)`,
		principal, joinScopes(scopes), req.Client, principal)
	if err != nil {
		return fmt.Errorf("oauth: bind a client: %w", err)
	}
	switch n, err := res.RowsAffected(); {
	case err != nil:
		return fmt.Errorf("oauth: bind a client: %w", err)
	case n == 0:
		// Somebody else bound it: report who it is bound to now.
		bound, err := s.boundPrincipalIn(ctx, q, req.Client)
		if err != nil {
			return err
		}
		return &boundError{client: req.Client, principal: bound}
	}
	return nil
}

// makePrincipal creates the principal in q, or revives a revoked one, which
// then starts with no grant.
func (s *Server) makePrincipal(ctx context.Context, q querier, name string) error {
	now := stamp(s.now())
	var revoked sql.NullString
	err := q.QueryRowContext(ctx, `SELECT revoked_at FROM principals WHERE name = ?`, name).Scan(&revoked)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := q.ExecContext(ctx, `INSERT INTO principals (name, created_at) VALUES (?, ?)`, name, now); err != nil {
			return fmt.Errorf("oauth: make a principal: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("oauth: read a principal: %w", err)
	case !revoked.Valid:
		return nil
	}
	if _, err := q.ExecContext(ctx, `UPDATE principals SET revoked_at = NULL, created_at = ? WHERE name = ?`, now, name); err != nil {
		return fmt.Errorf("oauth: revive a principal: %w", err)
	}
	if _, err := q.ExecContext(ctx, `DELETE FROM grants WHERE principal = ?`, name); err != nil {
		return fmt.Errorf("oauth: clear a revived principal's grants: %w", err)
	}
	return nil
}

// boundPrincipal is the principal a client is bound to, or "".
func (s *Server) boundPrincipal(ctx context.Context, clientID string) (string, error) {
	return s.boundPrincipalIn(ctx, s.db, clientID)
}

// boundPrincipalIn is the principal a client is bound to, read in q.
func (s *Server) boundPrincipalIn(ctx context.Context, q querier, clientID string) (string, error) {
	var principal sql.NullString
	err := q.QueryRowContext(ctx, `SELECT principal FROM oauth_clients WHERE id = ? AND revoked_at IS NULL`, clientID).Scan(&principal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", fmt.Errorf("oauth: read a client: %w", err)
	}
	return principal.String, nil
}
