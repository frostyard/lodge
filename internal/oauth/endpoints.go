package oauth

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ory/fosite"
)

// registration is /oauth/register's request (RFC 7591). lodge reads these
// members and ignores the others (docs/specs/oauth.md, Registration).
type registration struct {
	RedirectURIs            *[]string `json:"redirect_uris"`
	ClientName              *string   `json:"client_name"`
	GrantTypes              []string  `json:"grant_types"`
	ResponseTypes           []string  `json:"response_types"`
	TokenEndpointAuthMethod *string   `json:"token_endpoint_auth_method"`
	Scope                   *string   `json:"scope"`
}

// registrationAnswer is /oauth/register's answer.
type registrationAnswer struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
	Scope                   string   `json:"scope"`
}

// defaultClientName is the name of a client that gives none.
const defaultClientName = "unnamed client"

// register registers a client (RFC 7591): public, with no secret, protected
// by PKCE.
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			s.writeError(w, http.StatusBadRequest, "invalid_client_metadata", "the body is larger than 16 KiB")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid_client_metadata", "the body cannot be read")
		return
	}
	var in registration
	if err := jsonUnmarshal(body, &in); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_client_metadata", "the body is not a JSON object of client metadata")
		return
	}
	if in.RedirectURIs == nil || len(*in.RedirectURIs) == 0 {
		s.writeError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris is required")
		return
	}
	uris, err := redirectURIs(*in.RedirectURIs)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
		return
	}
	name := defaultClientName
	if in.ClientName != nil {
		if n := utf8.RuneCountInString(*in.ClientName); n < 1 || n > MaxClientName || hasControl(*in.ClientName) {
			s.writeError(w, http.StatusBadRequest, "invalid_client_metadata",
				fmt.Sprintf("client_name must be 1 to %d characters without control characters", MaxClientName))
			return
		}
		name = *in.ClientName
	}
	for _, g := range in.GrantTypes {
		if g != "authorization_code" && g != "refresh_token" {
			s.writeError(w, http.StatusBadRequest, "invalid_client_metadata",
				"grant_types may hold authorization_code and refresh_token")
			return
		}
	}
	for _, rt := range in.ResponseTypes {
		if rt != "code" {
			s.writeError(w, http.StatusBadRequest, "invalid_client_metadata", "response_types may hold code")
			return
		}
	}
	if in.Scope != nil {
		for _, sc := range scopes(*in.Scope) {
			if sc != ScopeMCP && sc != ScopeEvents {
				s.writeError(w, http.StatusBadRequest, "invalid_client_metadata", "scope may hold mcp and events")
				return
			}
		}
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		s.writeError(w, http.StatusInternalServerError, "server_error", "lodge cannot make a client ID")
		return
	}
	id := "lgc_" + base64.RawURLEncoding.EncodeToString(raw)
	now := s.now().UTC()
	urisJSON, err := jsonMarshal(uris)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "server_error", "lodge cannot encode the redirect URIs")
		return
	}
	// The count and the insert are 1 statement: 2 registrations at once
	// cannot both pass the limit (docs/specs/oauth.md, Registration).
	res, err := s.db.ExecContext(r.Context(), `INSERT INTO oauth_clients (id, name, redirect_uris, registered_at)
		SELECT ?, ?, ?, ? WHERE (SELECT COUNT(*) FROM oauth_clients WHERE principal IS NULL AND revoked_at IS NULL) < ?`,
		id, name, string(urisJSON), stamp(now), MaxUnboundClients)
	if err != nil {
		s.log.Error("register a client", "err", err)
		s.writeError(w, http.StatusInternalServerError, "server_error", "lodge cannot store the client")
		return
	}
	if n, err := res.RowsAffected(); err != nil {
		s.log.Error("count the registered client", "err", err)
		s.writeError(w, http.StatusInternalServerError, "server_error", "lodge cannot store the client")
		return
	} else if n == 0 {
		s.writeError(w, http.StatusTooManyRequests, "temporarily_unavailable",
			fmt.Sprintf("%d clients wait for a consent; register again later", MaxUnboundClients))
		return
	}
	s.log.Info("oauth client registered", "client", id, "name", name)
	// Every client is public: a token_endpoint_auth_method a client sends is
	// replaced with none (RFC 7591, section 3.2.1).
	s.writeJSON(w, http.StatusCreated, registrationAnswer{
		ClientID:                id,
		ClientIDIssuedAt:        now.Unix(),
		ClientName:              name,
		RedirectURIs:            uris,
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
		Scope:                   ScopeMCP + " " + ScopeEvents,
	})
}

// hasControl reports whether s holds a control character.
func hasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// redirectURIs checks a registration's redirect URIs: 1 to 8 strings of at
// most 512 bytes, each an absolute URI with no fragment, and either https,
// or http on a loopback host.
func redirectURIs(uris []string) ([]string, error) {
	if len(uris) > MaxRedirectURIs {
		return nil, fmt.Errorf("redirect_uris holds at most %d URIs", MaxRedirectURIs)
	}
	for _, u := range uris {
		if len(u) > MaxRedirectURILength {
			return nil, fmt.Errorf("redirect_uri %.40q… is longer than %d bytes", u, MaxRedirectURILength)
		}
		if err := checkRedirectURI(u); err != nil {
			return nil, err
		}
	}
	return uris, nil
}

// dnsNameRE is a DNS name's form: labels of a to z, A to Z, 0 to 9 and
// dashes, separated by dots.
var dnsNameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)

// portRE is a port's form: digits alone.
var portRE = regexp.MustCompile(`^[0-9]{1,5}$`)

// checkRedirectHost checks a redirect URI's authority: no user name, and a
// host that is a DNS name, an IPv4 literal or a bracketed IPv6 literal, with
// a port of digits alone. The host goes into a page's
// Content-Security-Policy, so a host that would add to that policy is
// refused (docs/specs/oauth.md, Registration; lodge-http.md, OAuth routes).
func checkRedirectHost(u *url.URL) error {
	if u.User != nil {
		return fmt.Errorf("redirect_uri %q has a user name", u.String())
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	bracketed := strings.HasPrefix(u.Host, "[")
	switch {
	case bracketed:
		if ip == nil || ip.To4() != nil {
			return fmt.Errorf("redirect_uri %q has a host that is not an IPv6 address", u.String())
		}
	case ip != nil:
		if ip.To4() == nil {
			return fmt.Errorf("redirect_uri %q has an IPv6 address that is not in brackets", u.String())
		}
	case host == "" || len(host) > 253 || !dnsNameRE.MatchString(host):
		return fmt.Errorf("redirect_uri %q has a host that is not a DNS name or an IP address", u.String())
	}
	if port := u.Port(); port != "" && !portRE.MatchString(port) {
		return fmt.Errorf("redirect_uri %q has a port that is not digits", u.String())
	}
	return nil
}

// checkRedirectURI checks one redirect URI.
func checkRedirectURI(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("redirect_uri %q is not an absolute URI", raw)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("redirect_uri %q has a fragment", raw)
	}
	if err := checkRedirectHost(u); err != nil {
		return err
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		switch u.Hostname() {
		case "127.0.0.1", "::1", "localhost":
			return nil
		}
		return fmt.Errorf("redirect_uri %q is http on a host that is not a loopback one", raw)
	}
	return fmt.Errorf("redirect_uri %q is neither https nor http on a loopback host", raw)
}

// checkResource checks a request's resource parameter (RFC 8707): it is the
// issuer, or starts with the issuer and "/".
func (s *Server) checkResource(form url.Values) error {
	issuer := s.Issuer()
	for _, resource := range form["resource"] {
		if resource != issuer && !strings.HasPrefix(resource, issuer+"/") {
			return fosite.ErrInvalidRequest.WithHintf("The resource parameter is not lodge's: %s", issuer)
		}
	}
	return nil
}

// authorize is the authorization endpoint: fosite parses the request, and
// lodge records a sign-in that an admin decides.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	if err := r.ParseForm(); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "the request cannot be read")
		return
	}
	ctx := r.Context()
	// A request without scope asks for mcp.
	if r.Form.Get("scope") == "" {
		r.Form.Set("scope", DefaultScope)
	}
	ar, err := s.provider.NewAuthorizeRequest(ctx, r)
	if err != nil {
		if ar == nil {
			ar = fosite.NewAuthorizeRequest()
		}
		s.provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}
	// A resource that is not lodge's is refused, and so is a request that
	// does not bring a S256 code challenge: both go back to the client,
	// whose redirect URI fosite has matched by now.
	if err := s.checkResource(ar.GetRequestForm()); err != nil {
		s.provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}
	if err := checkPKCE(ar); err != nil {
		s.provider.WriteAuthorizeError(ctx, w, ar, err)
		return
	}
	client, err := s.store.clientRow(ctx, ar.GetClient().GetID())
	if err != nil {
		s.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrInvalidClient.WithHint("The client is not known."))
		return
	}
	principal := client.Principal
	if principal == "" {
		principal = ProposePrincipal(client.Name)
	}
	query := signinQuery(r.Form)
	// A client with one redirect URI may leave it out: lodge stores the one
	// fosite matched, so that the decision can answer it.
	if query.Get("redirect_uri") == "" {
		query.Set("redirect_uri", ar.GetRedirectURI().String())
	}
	req, err := s.createSigninRequest(ctx, client.ID, append([]string{}, ar.GetRequestedScopes()...), principal, query)
	if errors.Is(err, ErrTooMany) {
		writeOneLine(w, http.StatusTooManyRequests, "64 requests wait for a decision: decide one, then sign in again")
		return
	} else if err != nil {
		s.log.Error("record a sign-in", "err", err)
		s.provider.WriteAuthorizeError(ctx, w, ar, fosite.ErrServerError.WithHint("lodge cannot record the request."))
		return
	}
	s.log.Info("sign-in requested", "request", req.ID, "client", client.ID, "scopes", joinScopes(req.Scopes))
	w.Header().Set("Location", "/oauth/consent/"+req.ID)
	w.WriteHeader(http.StatusSeeOther)
}

// checkPKCE requires a S256 code challenge and refuses plain
// (docs/specs/oauth.md, Rules).
func checkPKCE(ar fosite.AuthorizeRequester) error {
	challenge := ar.GetRequestForm().Get("code_challenge")
	method := ar.GetRequestForm().Get("code_challenge_method")
	switch {
	case challenge == "":
		return fosite.ErrInvalidRequest.WithHint("A code_challenge is required: the authorization code grant needs PKCE.")
	case method != "S256":
		return fosite.ErrInvalidRequest.WithHint("code_challenge_method must be S256: plain is refused.")
	}
	return nil
}

// signinQuery is the part of an authorization request that a decision needs
// to rebuild it.
func signinQuery(form url.Values) url.Values {
	out := url.Values{}
	for _, k := range []string{"client_id", "redirect_uri", "response_type", "scope", "state",
		"code_challenge", "code_challenge_method", "resource", "audience"} {
		for _, v := range form[k] {
			out.Add(k, v)
		}
	}
	return out
}

// authorizeRequest rebuilds a sign-in's authorization request from the query
// lodge stored.
func (s *Server) authorizeRequest(r *http.Request, query url.Values) (fosite.AuthorizeRequester, error) {
	u := *r.URL
	u.RawQuery = query.Encode()
	rebuilt := r.Clone(r.Context())
	rebuilt.URL = &u
	rebuilt.Method = http.MethodGet
	rebuilt.Body = http.NoBody
	rebuilt.Form = nil
	rebuilt.PostForm = nil
	ar, err := s.provider.NewAuthorizeRequest(rebuilt.Context(), rebuilt)
	if err != nil {
		if ar == nil {
			ar = fosite.NewAuthorizeRequest()
		}
		return ar, err
	}
	return ar, nil
}

// token is the token endpoint: the authorization code grant and the refresh
// token grant.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	ctx := r.Context()
	if err := r.ParseForm(); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "the request cannot be read")
		return
	}
	if err := s.checkResource(r.PostForm); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "the resource parameter is not lodge's")
		return
	}
	// The request carries a flag that the storage sets when a code or a
	// refresh token was used twice: then its family is revoked in the
	// transaction that found it, no token is stored, and this request
	// answers invalid_grant (docs/specs/oauth.md, Tokens).
	reuse := &reuseFlag{}
	ctx = context.WithValue(ctx, reuseKey{}, reuse)
	req, err := s.provider.NewAccessRequest(ctx, r.WithContext(ctx), NewSession())
	if err != nil {
		s.provider.WriteAccessError(ctx, w, req, err)
		return
	}
	resp, err := s.provider.NewAccessResponse(ctx, req)
	if err != nil {
		// A second request that presents a code: fosite's code flow revokes
		// the family when it reads a used code at its first read, and
		// answers an error when it reads one at its second. lodge revokes
		// the family here, in a transaction of its own, and answers
		// invalid_grant either way.
		if errors.Is(err, fosite.ErrInvalidatedAuthorizeCode) {
			if ok, rerr := s.revokeUsedCode(ctx, r.PostForm.Get("code")); rerr != nil {
				s.log.Error("revoke a used code's family", "err", rerr)
			} else if ok {
				s.writeError(w, http.StatusBadRequest, "invalid_grant",
					"the code was used already: every token of its sign-in is revoked")
				return
			}
		}
		s.provider.WriteAccessError(ctx, w, req, err)
		return
	}
	if reuse.used {
		s.provider.WriteAccessError(ctx, w, req,
			fosite.ErrInvalidGrant.WithHint("The token was used already; every token of its sign-in is revoked."))
		return
	}
	// fosite derives expires_in from the session's expiry and the wall
	// clock, which loses the fraction of a second. lodge states the
	// lifespan it configured instead (docs/specs/oauth.md, Tokens).
	resp.SetExpiresIn(AccessTokenLifespan)
	s.provider.WriteAccessResponse(ctx, w, req, resp)
}

// revokeUsedCode revokes the family of a code that a second request
// presented, in a transaction of its own, and reports whether it found it.
func (s *Server) revokeUsedCode(ctx context.Context, code string) (bool, error) {
	ctx = context.WithoutCancel(ctx)
	var family string
	err := s.store.queryRow(ctx, `SELECT request_id FROM oauth_sessions WHERE kind = ? AND signature = ?`,
		kindCode, s.strategy.AuthorizeCodeSignature(ctx, code)).Scan(&family)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("oauth: read a used code: %w", err)
	}
	if err := s.store.revokeFamily(ctx, family); err != nil {
		return false, err
	}
	return true, nil
}

// revoke is the revocation endpoint (RFC 7009).
func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxBody)
	ctx := r.Context()
	err := s.provider.NewRevocationRequest(ctx, r)
	s.provider.WriteRevocationResponse(ctx, w, err)
}

// redirectOrigin is the origin of a redirect URI: scheme://host:port. The
// origin goes into a page's Content-Security-Policy, so it is built only
// from a URI that passes checkRedirectURI.
func redirectOrigin(raw string) (string, error) {
	if err := checkRedirectURI(raw); err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + u.Host, nil
}

// has reports whether list holds v.
func has(list []string, v string) bool { return slices.Contains(list, v) }
