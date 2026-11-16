package oauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
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

// Commit commits the outermost transaction of this context.
func (s *storage) Commit(ctx context.Context) error {
	st, ok := ctx.Value(txKey{}).(*txState)
	if !ok {
		return nil
	}
	if st.depth--; st.depth > 0 {
		return nil
	}
	if err := st.tx.Commit(); err != nil {
		return fmt.Errorf("oauth: commit: %w", err)
	}
	return nil
}

// Rollback rolls back the outermost transaction of this context.
func (s *storage) Rollback(ctx context.Context) error {
	st, ok := ctx.Value(txKey{}).(*txState)
	if !ok {
		return nil
	}
	if st.depth--; st.depth > 0 {
		return nil
	}
	if err := st.tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return fmt.Errorf("oauth: roll back: %w", err)
	}
	return nil
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

// GetAuthorizeCodeSession returns the request of code. An invalidated code
// answers its request and fosite.ErrInvalidatedAuthorizeCode.
func (s *storage) GetAuthorizeCodeSession(ctx context.Context, code string, session fosite.Session) (fosite.Requester, error) {
	req, r, err := s.get(ctx, kindCode, code, session)
	if err != nil {
		return nil, err
	}
	if !r.active {
		return req, fosite.ErrInvalidatedAuthorizeCode
	}
	return req, nil
}

// InvalidateAuthorizeCodeSession marks code used.
func (s *storage) InvalidateAuthorizeCodeSession(ctx context.Context, code string) error {
	res, err := s.exec(ctx, `UPDATE oauth_sessions SET active = 0 WHERE kind = ? AND signature = ?`, kindCode, code)
	if err != nil {
		return fmt.Errorf("oauth: invalidate a code: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("oauth: invalidate a code: %w", err)
	} else if n == 0 {
		return fosite.ErrNotFound
	}
	return nil
}

// CreateAccessTokenSession stores the request under signature.
func (s *storage) CreateAccessTokenSession(ctx context.Context, signature string, req fosite.Requester) error {
	return s.create(ctx, kindAccess, signature, req, nil)
}

// GetAccessTokenSession returns the request of an access token.
func (s *storage) GetAccessTokenSession(ctx context.Context, signature string, session fosite.Session) (fosite.Requester, error) {
	req, _, err := s.get(ctx, kindAccess, signature, session)
	return req, err
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

// RotateRefreshToken invalidates the family's refresh tokens and deletes
// its access tokens, before the flow stores the new pair.
func (s *storage) RotateRefreshToken(ctx context.Context, requestID, _ string) error {
	if err := s.RevokeRefreshToken(ctx, requestID); err != nil && !errors.Is(err, fosite.ErrNotFound) {
		return err
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

// deleteClientSessions deletes every session of a client.
func (s *storage) deleteClientSessions(ctx context.Context, clientID string) error {
	if _, err := s.exec(ctx, `DELETE FROM oauth_sessions WHERE client_id = ?`, clientID); err != nil {
		return fmt.Errorf("oauth: delete a client's sessions: %w", err)
	}
	return nil
}
