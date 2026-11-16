package oauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/pkce"
	fsstorage "github.com/ory/fosite/storage"
)

// The kinds of row lodge keeps in oauth_sessions.
const (
	kindCode    = "code"
	kindAccess  = "access"
	kindRefresh = "refresh"
	kindPKCE    = "pkce"
)

// storage is fosite's storage on lodge's database: the clients, the code,
// access token, refresh token and PKCE sessions, and a transaction that
// every query joins while fosite holds one. It wraps no token: a row holds
// each token's HMAC signature alone (docs/specs/oauth.md, State).
type storage struct {
	db  *sql.DB
	now func() time.Time
	log *slog.Logger
	// revokeFails, when it returns an error, makes the write of a family's
	// revocation fail. It is Options.RevokeFails: a test's seam, nil in
	// lodge.
	revokeFails func(family string) error

	// mu guards waiting.
	mu sync.Mutex
	// waiting are the families whose revocation is not stored yet. Every
	// read of their tokens is refused until it is, so that a revocation
	// whose write failed is not lost.
	waiting map[string]struct{}
}

var (
	_ fosite.ClientManager          = (*storage)(nil)
	_ oauth2.CoreStorage            = (*storage)(nil)
	_ oauth2.TokenRevocationStorage = (*storage)(nil)
	_ pkce.PKCERequestStorage       = (*storage)(nil)
	_ fsstorage.Transactional       = (*storage)(nil)
)

// txState is the transaction fosite holds, and how many BeginTX calls are
// open on it: only the outermost commits or rolls back.
type txState struct {
	tx    *sql.Tx
	depth int
	// poisoned is set when an inner Rollback undid part of the work, which
	// this one transaction cannot undo alone: the outer Commit rolls back.
	poisoned error
	// families are the token families whose token was used twice in this
	// transaction. fosite rolls the transaction back, so they are revoked
	// once it is gone.
	families []string
}

type txKey struct{}

// BeginTX starts the transaction fosite's flows need, or joins the one this
// context already holds.
func (s *storage) BeginTX(ctx context.Context) (context.Context, error) {
	if st, ok := ctx.Value(txKey{}).(*txState); ok {
		st.depth++
		return ctx, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ctx, fmt.Errorf("oauth: begin a transaction: %w", err)
	}
	return context.WithValue(ctx, txKey{}, &txState{tx: tx, depth: 1}), nil
}

// Commit commits the outermost transaction of this context. A transaction
// an inner Rollback poisoned rolls back instead, and says so.
func (s *storage) Commit(ctx context.Context) error {
	st, ok := ctx.Value(txKey{}).(*txState)
	if !ok || st.depth <= 0 {
		return nil
	}
	if st.depth--; st.depth > 0 {
		return nil
	}
	if st.poisoned != nil {
		_ = st.tx.Rollback()
		s.revokeFamilies(ctx, st.families)
		return fmt.Errorf("oauth: commit a transaction that was rolled back inside it: %w", st.poisoned)
	}
	if err := st.tx.Commit(); err != nil {
		return fmt.Errorf("oauth: commit: %w", err)
	}
	s.revokeFamilies(ctx, st.families)
	return nil
}

// Rollback rolls back the outermost transaction of this context, and any
// inner one poisons it. The families whose token was used twice are revoked
// after the rollback, so that the revocation outlives fosite's transaction.
func (s *storage) Rollback(ctx context.Context) error {
	st, ok := ctx.Value(txKey{}).(*txState)
	if !ok || st.depth <= 0 {
		return nil
	}
	if st.depth--; st.depth > 0 {
		st.poisoned = errors.New("a transaction inside it was rolled back")
		return nil
	}
	err := st.tx.Rollback()
	s.revokeFamilies(ctx, st.families)
	if err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("oauth: roll back: %w", err)
	}
	return nil
}

// revokeFamilies revokes each family durably: fosite's transaction is gone,
// so the revocation is not undone with it. A family whose write fails stays
// in the waiting set, and its tokens stay refused.
func (s *storage) revokeFamilies(ctx context.Context, families []string) {
	if len(families) == 0 {
		return
	}
	ctx = context.WithoutCancel(ctx)
	for _, family := range families {
		if err := s.revokeFamily(ctx, family); err != nil {
			s.log.Error("revoke a token family after its reuse", "family", family, "err", err)
			continue
		}
		s.revocationStored(family)
	}
}

// awaitRevocation puts a family in the waiting set, before lodge tries to
// write its revocation.
func (s *storage) awaitRevocation(family string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waiting[family] = struct{}{}
}

// revocationStored takes a family out of the waiting set: its revocation is
// in the database.
func (s *storage) revocationStored(family string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.waiting, family)
}

// revoked reports whether a family waits for its revocation to be stored, in
// which case its tokens are refused as if it were revoked.
func (s *storage) revoked(family string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.waiting[family]
	return ok
}

// waitingFamilies lists the families whose revocation is not stored yet.
func (s *storage) waitingFamilies() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.waiting))
	for family := range s.waiting {
		out = append(out, family)
	}
	slices.Sort(out)
	return out
}

// retryRevocations writes the revocation of every waiting family again, and
// is what the hourly tick asks for.
func (s *storage) retryRevocations(ctx context.Context) {
	for _, family := range s.waitingFamilies() {
		if err := s.revokeFamily(context.WithoutCancel(ctx), family); err != nil {
			s.log.Error("revoke a token family again", "family", family, "err", err)
			continue
		}
		s.revocationStored(family)
	}
}

// revokeFamily marks every refresh token of a family inactive, and deletes
// its access tokens, in a transaction of its own.
func (s *storage) revokeFamily(ctx context.Context, family string) error {
	if s.revokeFails != nil {
		if err := s.revokeFails(family); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("oauth: begin a revocation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	txctx := context.WithValue(ctx, txKey{}, &txState{tx: tx, depth: 1})
	if err := s.RevokeRefreshToken(txctx, family); err != nil && !errors.Is(err, fosite.ErrNotFound) {
		return err
	}
	if err := s.RevokeAccessToken(txctx, family); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("oauth: commit a revocation: %w", err)
	}
	return nil
}

// markReused records a family whose code or refresh token was used twice, and
// returns the error the token endpoint reports: it names use, fosite's own
// error for the presented kind, and errTokenReused. The revocation runs when
// fosite's transaction, which it rolls back, is gone.
func (s *storage) markReused(ctx context.Context, family string, use error) error {
	// The family waits first, so that its tokens are refused even when the
	// write of its revocation fails.
	s.awaitRevocation(family)
	if st, ok := ctx.Value(txKey{}).(*txState); ok && st.depth > 0 {
		st.families = append(st.families, family)
	} else if err := s.revokeFamily(context.WithoutCancel(ctx), family); err != nil {
		s.log.Error("revoke a token family after its reuse", "family", family, "err", err)
	} else {
		s.revocationStored(family)
	}
	return &reusedError{family: family, use: use}
}

// errTokenReused means a code or a refresh token that another request
// consumed was presented.
var errTokenReused = errors.New("oauth: the token was already used")

// reusedError is a code or refresh token that was used already: its family
// is revoked, and the token endpoint answers invalid_grant.
type reusedError struct {
	family string
	// use is fosite's own error for the presented kind, which its flows
	// decide by.
	use error
}

func (e *reusedError) Error() string {
	return "oauth: the token was already used: family " + e.family
}

// Unwrap answers both errors: fosite's own for the presented kind, and
// errTokenReused, which the token endpoint answers invalid_grant for.
func (e *reusedError) Unwrap() []error {
	return []error{e.use, errTokenReused}
}

func txOf(ctx context.Context) *sql.Tx {
	st, _ := ctx.Value(txKey{}).(*txState)
	if st == nil {
		return nil
	}
	return st.tx
}

// exec runs a statement in fosite's transaction when there is one.
func (s *storage) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	if tx := txOf(ctx); tx != nil {
		return tx.ExecContext(ctx, q, args...)
	}
	return s.db.ExecContext(ctx, q, args...)
}

// queryRow runs a query in fosite's transaction when there is one.
func (s *storage) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	if tx := txOf(ctx); tx != nil {
		return tx.QueryRowContext(ctx, q, args...)
	}
	return s.db.QueryRowContext(ctx, q, args...)
}

// query runs a query in fosite's transaction when there is one.
func (s *storage) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	if tx := txOf(ctx); tx != nil {
		return tx.QueryContext(ctx, q, args...)
	}
	return s.db.QueryContext(ctx, q, args...)
}

// querier runs a statement or a query: lodge's database, or a transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// at returns the querier a statement runs in: q when it is given, else
// fosite's transaction, else the database.
func (s *storage) at(ctx context.Context, q querier) querier {
	if q != nil {
		return q
	}
	if tx := txOf(ctx); tx != nil {
		return tx
	}
	return s.db
}

// GetClient returns client id, which is always a public client; a revoked
// or unknown client is fosite.ErrNotFound.
func (s *storage) GetClient(ctx context.Context, id string) (fosite.Client, error) {
	c, err := s.clientRow(ctx, id)
	if err != nil {
		return nil, err
	}
	return &fosite.DefaultClient{
		ID:            c.ID,
		Public:        true,
		GrantTypes:    []string{"authorization_code", "refresh_token"},
		ResponseTypes: []string{"code"},
		Scopes:        []string{ScopeMCP, ScopeEvents},
		RedirectURIs:  c.RedirectURIs,
	}, nil
}

// ClientAssertionJWTValid answers nil: lodge takes no client assertions, so
// no JTI is known.
func (s *storage) ClientAssertionJWTValid(context.Context, string) error { return nil }

// SetClientAssertionJWT answers nil: lodge takes no client assertions.
func (s *storage) SetClientAssertionJWT(context.Context, string, time.Time) error { return nil }

// sessionRow is one oauth_sessions row.
type sessionRow struct {
	signature     string
	kind          string
	requestID     string
	clientID      string
	principal     string
	requestedAt   time.Time
	expiresAt     time.Time
	requested     []string
	granted       []string
	reqAudience   []string
	grantAudience []string
	form          url.Values
	session       sessionJSON
	active        bool
}

const sessionColumns = `signature, kind, request_id, client_id, principal, requested_at, expires_at,
	requested_scopes, granted_scopes, requested_audience, granted_audience, form, session, active`

// lifespan is how long a row of kind lives.
func lifespan(kind string) time.Duration {
	switch kind {
	case kindCode, kindPKCE:
		return AuthorizeCodeLifespan
	case kindAccess:
		return AccessTokenLifespan
	case kindRefresh:
		return RefreshTokenLifespan
	}
	return AccessTokenLifespan
}

// expiry is when a row of kind expires: the session's answer for that token
// type, or the request's time plus the kind's lifespan.
func expiry(kind string, req fosite.Requester) time.Time {
	var token fosite.TokenType
	switch kind {
	case kindCode, kindPKCE:
		token = fosite.AuthorizeCode
	case kindAccess:
		token = fosite.AccessToken
	case kindRefresh:
		token = fosite.RefreshToken
	}
	if sess := req.GetSession(); sess != nil {
		if exp := sess.GetExpiresAt(token); !exp.IsZero() {
			return exp.UTC()
		}
	}
	return req.GetRequestedAt().UTC().Add(lifespan(kind))
}

// create stores req's session under signature.
func (s *storage) create(ctx context.Context, kind, signature string, req fosite.Requester, form url.Values) error {
	scopes := []string{}
	if req.GetRequestedScopes() != nil {
		scopes = req.GetRequestedScopes()
	}
	granted := []string{}
	if req.GetGrantedScopes() != nil {
		granted = req.GetGrantedScopes()
	}
	requestedAud, grantedAud := []string{}, []string{}
	if req.GetRequestedAudience() != nil {
		requestedAud = req.GetRequestedAudience()
	}
	if req.GetGrantedAudience() != nil {
		grantedAud = req.GetGrantedAudience()
	}
	principal := ""
	if sess := req.GetSession(); sess != nil {
		principal = sess.GetSubject()
	}
	if form == nil {
		form = req.GetRequestForm()
	}
	if form == nil {
		form = url.Values{}
	}
	scopesJSON, err := jsonMarshal(scopes)
	if err != nil {
		return err
	}
	grantedJSON, err := jsonMarshal(granted)
	if err != nil {
		return err
	}
	requestedAudJSON, err := jsonMarshal(requestedAud)
	if err != nil {
		return err
	}
	grantedAudJSON, err := jsonMarshal(grantedAud)
	if err != nil {
		return err
	}
	formJSON, err := jsonMarshal(form)
	if err != nil {
		return err
	}
	sessionText, err := jsonMarshal(sessionFrom(req.GetSession()))
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO oauth_sessions
		(signature, kind, request_id, client_id, principal, requested_at, expires_at,
		 requested_scopes, granted_scopes, requested_audience, granted_audience, form, session, active)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
		ON CONFLICT (kind, signature) DO UPDATE SET
			request_id = excluded.request_id, expires_at = excluded.expires_at,
			requested_scopes = excluded.requested_scopes, granted_scopes = excluded.granted_scopes,
			requested_audience = excluded.requested_audience, granted_audience = excluded.granted_audience,
			form = excluded.form, session = excluded.session, active = 1`,
		signature, kind, req.GetID(), req.GetClient().GetID(), principal,
		stamp(req.GetRequestedAt()), stamp(expiry(kind, req)),
		string(scopesJSON), string(grantedJSON), string(requestedAudJSON), string(grantedAudJSON),
		string(formJSON), string(sessionText))
	if err != nil {
		return fmt.Errorf("oauth: store a %s session: %w", kind, err)
	}
	return nil
}

// get reads the row of kind and signature, and rebuilds its request.
func (s *storage) get(ctx context.Context, kind, signature string, session fosite.Session) (fosite.Requester, sessionRow, error) {
	var r sessionRow
	var requestedAt, expiresAt, scopes, granted, reqAud, grantAud, formText, sessionText string
	var active int
	err := s.queryRow(ctx, `SELECT `+sessionColumns+` FROM oauth_sessions WHERE kind = ? AND signature = ?`,
		kind, signature).Scan(&r.signature, &r.kind, &r.requestID, &r.clientID, &r.principal,
		&requestedAt, &expiresAt, &scopes, &granted, &reqAud, &grantAud, &formText, &sessionText, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, r, fosite.ErrNotFound
	} else if err != nil {
		return nil, r, fmt.Errorf("oauth: read a %s session: %w", kind, err)
	}
	r.requestedAt, r.expiresAt = parseTime(requestedAt), parseTime(expiresAt)
	r.active = active == 1
	for _, f := range []struct {
		text string
		into *[]string
	}{{scopes, &r.requested}, {granted, &r.granted}, {reqAud, &r.reqAudience}, {grantAud, &r.grantAudience}} {
		if err := jsonUnmarshal([]byte(f.text), f.into); err != nil {
			return nil, r, fmt.Errorf("oauth: decode a %s session: %w", kind, err)
		}
	}
	if err := jsonUnmarshal([]byte(formText), &r.form); err != nil {
		return nil, r, fmt.Errorf("oauth: decode a %s session's form: %w", kind, err)
	}
	if err := jsonUnmarshal([]byte(sessionText), &r.session); err != nil {
		return nil, r, fmt.Errorf("oauth: decode a %s session: %w", kind, err)
	}
	req, err := s.requester(ctx, r, session)
	if err != nil {
		return nil, r, err
	}
	return req, r, nil
}

// requester rebuilds a stored request: its client, as it is now, and its
// session, into the one the caller passed when it is lodge's.
func (s *storage) requester(ctx context.Context, r sessionRow, session fosite.Session) (fosite.Requester, error) {
	client, err := s.GetClient(ctx, r.clientID)
	if err != nil {
		return nil, err
	}
	req := fosite.NewRequest()
	req.ID = r.requestID
	req.RequestedAt = r.requestedAt
	req.Client = client
	req.RequestedScope = fosite.Arguments(r.requested)
	req.GrantedScope = fosite.Arguments(r.granted)
	req.RequestedAudience = fosite.Arguments(r.reqAudience)
	req.GrantedAudience = fosite.Arguments(r.grantAudience)
	if r.form != nil {
		req.Form = r.form
	}
	decoded := r.session.session()
	if into, ok := session.(*Session); ok && into != nil {
		into.set(decoded)
		req.Session = into
	} else {
		req.Session = decoded
	}
	return req, nil
}

// CreateAuthorizeCodeSession stores the request under code.
func (s *storage) CreateAuthorizeCodeSession(ctx context.Context, code string, req fosite.Requester) error {
	return s.create(ctx, kindCode, code, req, nil)
}

// GetAuthorizeCodeSession returns the request of code. A code that another
// exchange consumed is a reuse: it answers its request, the error fosite's
// code flow acts on, and its family is revoked.
func (s *storage) GetAuthorizeCodeSession(ctx context.Context, code string, session fosite.Session) (fosite.Requester, error) {
	req, r, err := s.get(ctx, kindCode, code, session)
	if err != nil {
		return nil, err
	}
	if s.revoked(r.requestID) {
		return nil, fosite.ErrNotFound
	}
	if !r.active {
		return req, s.markReused(ctx, r.requestID, fosite.ErrInvalidatedAuthorizeCode)
	}
	return req, nil
}

// InvalidateAuthorizeCodeSession consumes code: 1 conditional update, so
// that 1 exchange wins. A code another exchange consumed revokes its family,
// and no token is issued for it.
func (s *storage) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	res, err := s.exec(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND signature = ? AND active = 1`,
		kindCode, code)
	if err != nil {
		return fmt.Errorf("oauth: invalidate a code: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("oauth: invalidate a code: %w", err)
	}
	if n == 1 {
		return nil
	}
	var family string
	switch err := s.queryRow(ctx, `SELECT request_id FROM oauth_sessions WHERE kind = ? AND signature = ?`,
		kindCode, code).Scan(&family); {
	case errors.Is(err, sql.ErrNoRows):
		return fosite.ErrNotFound
	case err != nil:
		return fmt.Errorf("oauth: read a used code: %w", err)
	}
	return s.markReused(ctx, family, fosite.ErrInvalidatedAuthorizeCode)
}

// CreateAccessTokenSession stores the request under signature.
func (s *storage) CreateAccessTokenSession(ctx context.Context, signature string, req fosite.Requester) error {
	return s.create(ctx, kindAccess, signature, req, nil)
}

// GetAccessTokenSession returns the request of an access token.
func (s *storage) GetAccessTokenSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	req, r, err := s.get(ctx, kindAccess, signature, session)
	if err != nil {
		return nil, err
	}
	if s.revoked(r.requestID) {
		return nil, fosite.ErrNotFound
	}
	return req, nil
}

// DeleteAccessTokenSession forgets an access token.
func (s *storage) DeleteAccessTokenSession(ctx context.Context, signature string) error {
	if _, err := s.exec(ctx, `DELETE FROM oauth_sessions WHERE kind = ? AND signature = ?`, kindAccess, signature); err != nil {
		return fmt.Errorf("oauth: delete an access token: %w", err)
	}
	return nil
}

// CreateRefreshTokenSession stores the request under signature.
func (s *storage) CreateRefreshTokenSession(ctx context.Context, signature, _ string, req fosite.Requester) error {
	return s.create(ctx, kindRefresh, signature, req, nil)
}

// GetRefreshTokenSession returns the request of a refresh token. An
// inactive token answers its request and fosite.ErrInactiveToken.
func (s *storage) GetRefreshTokenSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	req, r, err := s.get(ctx, kindRefresh, signature, session)
	if err != nil {
		return nil, err
	}
	if s.revoked(r.requestID) {
		return nil, fosite.ErrNotFound
	}
	if !r.active {
		return req, fosite.ErrInactiveToken
	}
	return req, nil
}

// DeleteRefreshTokenSession forgets a refresh token.
func (s *storage) DeleteRefreshTokenSession(ctx context.Context, signature string) error {
	if _, err := s.exec(ctx, `DELETE FROM oauth_sessions WHERE kind = ? AND signature = ?`, kindRefresh, signature); err != nil {
		return fmt.Errorf("oauth: delete a refresh token: %w", err)
	}
	return nil
}

// RevokeRefreshToken marks every refresh token of the request's family
// inactive.
func (s *storage) RevokeRefreshToken(ctx context.Context, requestID string) error {
	res, err := s.exec(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND request_id = ?`, kindRefresh, requestID)
	if err != nil {
		return fmt.Errorf("oauth: revoke a refresh token: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("oauth: revoke a refresh token: %w", err)
	} else if n == 0 {
		return fosite.ErrNotFound
	}
	return nil
}

// RevokeAccessToken deletes every access token of the request's family.
func (s *storage) RevokeAccessToken(ctx context.Context, requestID string) error {
	if _, err := s.exec(ctx, `DELETE FROM oauth_sessions WHERE kind = ? AND request_id = ?`, kindAccess, requestID); err != nil {
		return fmt.Errorf("oauth: revoke an access token: %w", err)
	}
	return nil
}

// RotateRefreshToken consumes the present refresh token in 1 conditional
// update, before the flow stores the new pair, and deletes the family's
// access tokens. A token another request consumed revokes the family, and no
// pair is issued.
func (s *storage) RotateRefreshToken(ctx context.Context, requestID, signature string) error {
	res, err := s.exec(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND signature = ? AND active = 1`,
		kindRefresh, signature)
	if err != nil {
		return fmt.Errorf("oauth: rotate a refresh token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("oauth: rotate a refresh token: %w", err)
	}
	if n == 0 {
		// A concurrent request consumed it, or it was replayed: the family
		// goes, and this one gets no pair.
		return s.markReused(ctx, requestID, fosite.ErrInactiveToken)
	}
	return s.RevokeAccessToken(ctx, requestID)
}

// CreatePKCERequestSession stores the challenge of a code.
func (s *storage) CreatePKCERequestSession(ctx context.Context, signature string, req fosite.Requester) error {
	return s.create(ctx, kindPKCE, signature, req, nil)
}

// GetPKCERequestSession returns the challenge of a code.
func (s *storage) GetPKCERequestSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	req, _, err := s.get(ctx, kindPKCE, signature, session)
	return req, err
}

// DeletePKCERequestSession keeps the challenge of a code: fosite deletes it
// before it checks the verifier, so a code tried with the wrong verifier
// would stop working with the right one. The challenge expires with its
// code, and the sweep deletes it.
func (s *storage) DeletePKCERequestSession(context.Context, string) error { return nil }

// deleteClientSessions deletes every session of a client, in q.
func (s *storage) deleteClientSessions(ctx context.Context, q querier, clientID string) error {
	if _, err := s.at(ctx, q).ExecContext(ctx, `DELETE FROM oauth_sessions WHERE client_id = ?`, clientID); err != nil {
		return fmt.Errorf("oauth: delete a client's sessions: %w", err)
	}
	return nil
}
