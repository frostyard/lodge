// Package oauth is lodge's OAuth 2.1 authorization server, and the state
// behind its consent and grants pages (docs/specs/oauth.md; ADR-0023): the
// principals, the clients, the grants and the tokens. internal/gateway
// routes to it after the checks of specs/lodge-http.md, OAuth routes.
//
// Only this package imports github.com/ory/fosite.
package oauth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/compose"
	"github.com/ory/fosite/handler/oauth2"
	enigma "github.com/ory/fosite/token/hmac"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/store"
)

// What a scope lets a client use (docs/specs/oauth.md, Names).
const (
	// ScopeMCP lets a client call lodge's MCP gateway.
	ScopeMCP = "mcp"
	// ScopeEvents lets a client subscribe to the broker.
	ScopeEvents = "events"
	// DefaultScope is what an authorization request without scope asks for.
	DefaultScope = ScopeMCP
)

// Lifespans and limits (docs/specs/oauth.md).
const (
	// AccessTokenLifespan is how long an access token lives.
	AccessTokenLifespan = time.Hour
	// RefreshTokenLifespan is how long a refresh token lives.
	RefreshTokenLifespan = 30 * 24 * time.Hour
	// AuthorizeCodeLifespan is how long an authorization code is good for.
	AuthorizeCodeLifespan = 10 * time.Minute
	// RequestLifespan is how long a pending request may be decided.
	RequestLifespan = 24 * time.Hour
	// MaxPending is how many requests may be pending at once.
	MaxPending = 64
	// MaxUnboundClients is how many clients no consent has bound lodge holds.
	MaxUnboundClients = 256
	// MaxBody bounds an OAuth request's body.
	MaxBody = 16 << 10
	// MaxItems bounds an access request's items.
	MaxItems = 32
	// MaxRedirectURIs bounds a client's redirect URIs.
	MaxRedirectURIs = 8
	// MaxRedirectURILength bounds one redirect URI.
	MaxRedirectURILength = 512
	// MaxClientName bounds a client's name.
	MaxClientName = 64
)

const (
	// unboundClientLifespan is how long a client no consent has bound lives.
	unboundClientLifespan = 24 * time.Hour
	// requestKeep is how long a decided or expired request is kept.
	requestKeep = 24 * time.Hour
	// lastUseEvery is how often a client's last use is recorded.
	lastUseEvery = time.Minute
	// sweepEvery is how often lodge deletes what expired.
	sweepEvery = time.Hour
	// pageLifetime is how long the grants page's CSRF token is good for.
	pageLifetime = time.Hour
	// pageSkew is how early the grants page's CSRF token is good from.
	pageSkew = time.Minute
)

// The routes the gateway calls this package for.
const (
	// RouteMetadata is /.well-known/oauth-authorization-server.
	RouteMetadata = "metadata"
	// RouteResource is /.well-known/oauth-protected-resource and its
	// gateway/mcp form.
	RouteResource = "resource"
	// RouteRegister is /oauth/register.
	RouteRegister = "register"
	// RouteAuthorize is /oauth/authorize, which the gateway runs the
	// consent checks on.
	RouteAuthorize = "authorize"
	// RouteToken is /oauth/token.
	RouteToken = "token"
	// RouteRevoke is /oauth/revoke.
	RouteRevoke = "revoke"
	// RouteConsent is /oauth/consent/<id>.
	RouteConsent = "consent"
	// RouteGrants is /oauth/grants.
	RouteGrants = "grants"
	// RouteGrantRevoke is /oauth/grants/revoke.
	RouteGrantRevoke = "grants/revoke"
)

// Errors this package returns for a request it refuses.
var (
	// ErrInvalid means a value breaks its form.
	ErrInvalid = errors.New("invalid")
	// ErrNotFound means the client, request or app does not exist, or is
	// not one this call may use.
	ErrNotFound = errors.New("not found")
	// ErrTooMany means too many requests or clients are held.
	ErrTooMany = errors.New("too many")
)

// Apps is what the consent page asks of the installed apps.
type Apps interface {
	// Get returns app id, and whether it is installed.
	Get(id string) (apps.App, bool)
}

// Options are what a Server needs.
type Options struct {
	// DB is lodge's database.
	DB *sql.DB
	// Apps are the installed apps, for the hearth manifests.
	Apps Apps
	// Issuer is lodge's origin, https://LODGE, which changes after the
	// nodes join the tailnet.
	Issuer func() string
	// Now is the clock [time.Now].
	Now func() time.Time
	// Log is the log [discard].
	Log *slog.Logger
	// OnGrant is called after a grant changed: a principal, and an app.
	OnGrant func(principal, app string)
	// OnRevoke is called after a client or a principal was revoked: the
	// principal, and the clients that went with it.
	OnRevoke func(principal string, clients []string)
}

// Server is lodge's OAuth authorization server and its pages.
type Server struct {
	db       *sql.DB
	apps     Apps
	issuer   func() string
	now      func() time.Time
	log      *slog.Logger
	onGrant  func(principal, app string)
	onRevoke func(principal string, clients []string)

	provider fosite.OAuth2Provider
	strategy *strategy
	store    *storage
	// tokensKey is fosite's HMAC secret, pagesKey the CSRF key, both 32
	// random bytes from lodge's database.
	tokensKey, pagesKey []byte
}

// New opens the server over o.DB, making its keys at the first start.
func New(ctx context.Context, o Options) (*Server, error) {
	if o.DB == nil {
		return nil, errors.New("oauth: no database")
	}
	if o.Issuer == nil {
		return nil, errors.New("oauth: no issuer")
	}
	s := &Server{db: o.DB, apps: o.Apps, issuer: o.Issuer, log: o.Log, onGrant: o.OnGrant, onRevoke: o.OnRevoke}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	s.now = o.Now
	if s.now == nil {
		s.now = time.Now
	}
	var err error
	if s.tokensKey, err = s.key(ctx, "tokens"); err != nil {
		return nil, err
	}
	if s.pagesKey, err = s.key(ctx, "pages"); err != nil {
		return nil, err
	}
	s.store = &storage{db: o.DB, now: s.now}
	cfg := &fosite.Config{
		AccessTokenLifespan:            AccessTokenLifespan,
		RefreshTokenLifespan:           RefreshTokenLifespan,
		AuthorizeCodeLifespan:          AuthorizeCodeLifespan,
		GlobalSecret:                   s.tokensKey,
		EnforcePKCE:                    true,
		EnforcePKCEForPublicClients:    true,
		EnablePKCEPlainChallengeMethod: false,
		ScopeStrategy:                  fosite.ExactScopeStrategy,
		// A non-nil, empty list issues a refresh token with every grant.
		RefreshTokenScopes:         []string{},
		SendDebugMessagesToClients: false,
	}
	s.strategy = &strategy{HMACSHAStrategyUnPrefixed: oauth2.NewHMACSHAStrategyUnPrefixed(&enigma.HMACStrategy{Config: cfg}, cfg)}
	// Only these 5 factories: the code grant, the refresh token grant,
	// revocation and introspection, and PKCE.
	s.provider = compose.Compose(cfg, s.store, s.strategy,
		compose.OAuth2AuthorizeExplicitFactory,
		compose.OAuth2RefreshTokenGrantFactory,
		compose.OAuth2TokenRevocationFactory,
		compose.OAuth2TokenIntrospectionFactory,
		compose.OAuth2PKCEFactory,
	)
	return s, nil
}

// key returns lodge's key name, making it at the first call.
func (s *Server) key(ctx context.Context, name string) ([]byte, error) {
	read := func() ([]byte, error) {
		var text string
		err := s.db.QueryRowContext(ctx, `SELECT key FROM oauth_keys WHERE name = ?`, name).Scan(&text)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		} else if err != nil {
			return nil, fmt.Errorf("read lodge's oauth keys: %w", err)
		}
		key, err := hex.DecodeString(text)
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("lodge's oauth key %q is not 32 bytes", name)
		}
		return key, nil
	}
	key, err := read()
	if err != nil || key != nil {
		return key, err
	}
	fresh := make([]byte, 32)
	if _, err := rand.Read(fresh); err != nil {
		return nil, fmt.Errorf("make lodge's oauth keys: %w", err)
	}
	// Another start may have made it in between: the first row wins.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO oauth_keys (name, key, created_at) VALUES (?, ?, ?) ON CONFLICT (name) DO NOTHING`,
		name, hex.EncodeToString(fresh), s.now().UTC().Format(store.TimeFormat)); err != nil {
		return nil, fmt.Errorf("write lodge's oauth keys: %w", err)
	}
	key, err = read()
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, fmt.Errorf("lodge's oauth key %q is missing after it was made", name)
	}
	return key, nil
}

// Issuer is lodge's origin, https://LODGE, with no trailing "/".
func (s *Server) Issuer() string { return strings.TrimSuffix(s.issuer(), "/") }

// Resource is the resource lodge's tokens are for.
func (s *Server) Resource() string { return s.Issuer() + "/gateway/mcp" }

// stamp is t as lodge's database keeps times.
func stamp(t time.Time) string { return t.UTC().Format(store.TimeFormat) }

// parseTime reads a stored time, or the zero time.
func parseTime(s string) time.Time {
	t, _ := time.Parse(store.TimeFormat, s)
	return t
}

// Run deletes what expired every hour until ctx ends.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.sweep(ctx); err != nil {
				s.log.Warn("oauth sweep", "err", err)
			}
		}
	}
}

// sweep deletes the expired sessions, the unbound clients older than 24
// hours, and the requests that were decided or expired 24 hours ago
// (docs/specs/oauth.md, State).
func (s *Server) sweep(ctx context.Context) error {
	now := s.now().UTC()
	for _, q := range []struct {
		what string
		sql  string
		arg  []any
	}{
		{"sessions", `DELETE FROM oauth_sessions WHERE expires_at <= ?`, []any{stamp(now)}},
		{"unbound clients", `DELETE FROM oauth_clients WHERE principal IS NULL AND registered_at <= ?`,
			[]any{stamp(now.Add(-unboundClientLifespan))}},
		{"requests", `DELETE FROM consent_requests WHERE COALESCE(decided_at, expires_at) <= ?`,
			[]any{stamp(now.Add(-requestKeep))}},
	} {
		if _, err := s.db.ExecContext(ctx, q.sql, q.arg...); err != nil {
			return fmt.Errorf("delete the %s: %w", q.what, err)
		}
	}
	return nil
}

// ServeOAuth answers an OAuth route the gateway's checks let through:
// the metadata documents, registration, the authorization, token and
// revocation endpoints.
func (s *Server) ServeOAuth(w http.ResponseWriter, r *http.Request, route string) {
	switch route {
	case RouteMetadata:
		s.writeMetadata(w, r, s.authorizationMetadata())
	case RouteResource:
		s.writeMetadata(w, r, s.resourceMetadata())
	case RouteRegister:
		s.register(w, r)
	case RouteToken:
		s.token(w, r)
	case RouteRevoke:
		s.revoke(w, r)
	default:
		s.writeError(w, http.StatusNotFound, "invalid_request", "no such OAuth route")
	}
}

type authorizationMetadataDocument struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	ScopesSupported                   []string `json:"scopes_supported"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	RevocationAuthMethodsSupported    []string `json:"revocation_endpoint_auth_methods_supported"`
}

type resourceMetadataDocument struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	ScopesSupported        []string `json:"scopes_supported"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ResourceName           string   `json:"resource_name"`
}

func (s *Server) authorizationMetadata() authorizationMetadataDocument {
	issuer := s.Issuer()
	return authorizationMetadataDocument{
		Issuer:                            issuer,
		AuthorizationEndpoint:             issuer + "/oauth/authorize",
		TokenEndpoint:                     issuer + "/oauth/token",
		RegistrationEndpoint:              issuer + "/oauth/register",
		RevocationEndpoint:                issuer + "/oauth/revoke",
		ScopesSupported:                   []string{ScopeMCP, ScopeEvents},
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code", "refresh_token"},
		CodeChallengeMethodsSupported:     []string{"S256"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		RevocationAuthMethodsSupported:    []string{"none"},
	}
}

func (s *Server) resourceMetadata() resourceMetadataDocument {
	return resourceMetadataDocument{
		Resource:               s.Resource(),
		AuthorizationServers:   []string{s.Issuer()},
		ScopesSupported:        []string{ScopeMCP, ScopeEvents},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "lodge",
	}
}

// writeMetadata writes a metadata document.
func (s *Server) writeMetadata(w http.ResponseWriter, r *http.Request, doc any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	b, err := jsonMarshal(doc)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "server_error", "lodge cannot encode its metadata")
		return
	}
	_, _ = w.Write(b)
}

// writeJSON writes v as the answer, with status.
func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := jsonMarshal(v)
	if err != nil {
		http.Error(w, "lodge cannot encode its answer", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

type oauthError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitzero"`
}

// writeError writes an OAuth JSON error (RFC 6749, section 5.2).
func (s *Server) writeError(w http.ResponseWriter, status int, code, description string) {
	s.writeJSON(w, status, oauthError{Error: code, ErrorDescription: description})
}

// writeOneLine writes one line, for a refusal that is not an OAuth error.
func writeOneLine(w http.ResponseWriter, status int, line string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Del("Content-Length")
	w.WriteHeader(status)
	_, _ = fmt.Fprintln(w, oneLine(line))
}

// oneLine replaces control characters, so a line stays one line.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// scopes splits a space-separated scope value into its scopes.
func scopes(v string) []string {
	var out []string
	for _, s := range strings.Split(v, " ") {
		if s != "" && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	return out
}

// joinScopes joins scopes with one space, sorted.
func joinScopes(list []string) string {
	out := slices.Clone(list)
	slices.Sort(out)
	return strings.Join(out, " ")
}
