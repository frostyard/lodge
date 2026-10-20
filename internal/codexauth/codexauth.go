// Package codexauth signs lodge in to the owner's ChatGPT plan with Sign in
// with ChatGPT, OpenAI's plan usage for open-source and locally hosted apps
// (docs/specs/inference.md, The codex provider; ADR-0015). lodgectl runs
// the sign-in on the owner's computer, where a browser is, and writes a
// login file; lodge's host imports it and keeps it fresh with Refresh.
package codexauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The sign-in's fixed values
// (https://developers.openai.com/siwc/token-sharing-open-source/sign-in).
const (
	Issuer        = "https://auth.openai.com"
	Resource      = "https://api.openai.com/v1"
	Scope         = "openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"
	PlanScope     = "chatgpt.tokens.use.direct"
	DynamicClient = "dynamic_agent_client"
	AgentName     = "lodge"
	CallbackPath  = "/auth/callback"
	DefaultPort   = 1455
	// Type is a login file's type.
	Type = "codex"
)

// Endpoints are the sign-in's addresses; tests point them at a fake.
type Endpoints struct {
	Issuer    string // the ID token's iss, and where discovery is
	Authorize string
	Token     string
}

// OpenAI is the production sign-in.
var OpenAI = Endpoints{
	Issuer:    Issuer,
	Authorize: Issuer + "/api/accounts/authorize",
	Token:     Issuer + "/api/accounts/oauth/token",
}

// Login is a signed-in registration: the login file lodgectl writes and
// lodge imports. Its tokens are secrets, shown nowhere.
type Login struct {
	Type string `json:"type"`
	// ClientID is the client OpenAI issued at registration, bound to the
	// owner and workspace; a later sign-in reuses it.
	ClientID string `json:"client_id"`
	// Account is the ID token's sub, and Email its email.
	Account           string    `json:"account"`
	Email             string    `json:"email,omitzero"`
	Scope             string    `json:"scope"`
	AccessToken       string    `json:"access_token"`
	RefreshToken      string    `json:"refresh_token"`
	IDToken           string    `json:"id_token,omitzero"`
	ExpiresAt         time.Time `json:"expires_at"`
	EarliestRefreshAt time.Time `json:"earliest_refresh_at,omitzero"`
}

// Validate checks a login file before lodge imports it.
func (l Login) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if l.Type != Type {
		bad("type is %q, not %q", l.Type, Type)
	}
	if l.ClientID == "" || l.ClientID == DynamicClient {
		bad("no issued client_id")
	}
	if l.Account == "" {
		bad("no account")
	}
	if !HasPlanScope(l.Scope) {
		bad("the grant lacks %s: the plan's use was not authorized", PlanScope)
	}
	if l.AccessToken == "" || l.RefreshToken == "" {
		bad("no tokens")
	}
	if l.ExpiresAt.IsZero() {
		bad("no expires_at")
	}
	return errors.Join(errs...)
}

// SignedIn reports whether the login still holds tokens; a sign-out keeps
// the registration and clears them.
func (l Login) SignedIn() bool { return l.AccessToken != "" && l.RefreshToken != "" }

// SignOut clears the tokens and keeps the registration, for a later
// sign-in with the same client.
func (l Login) SignOut() Login {
	l.AccessToken, l.RefreshToken, l.IDToken = "", "", ""
	l.ExpiresAt, l.EarliestRefreshAt = time.Time{}, time.Time{}
	return l
}

// HasPlanScope reports whether a granted scope lets the token use the plan.
func HasPlanScope(scope string) bool { return slices.Contains(strings.Fields(scope), PlanScope) }

// Due reports whether the access token should be refreshed at now: within
// two minutes of its expiry, and not before the earliest refresh.
func (l Login) Due(now time.Time) bool {
	if now.Before(l.EarliestRefreshAt) && now.Add(10*time.Second).Before(l.ExpiresAt) {
		return false
	}
	return !now.Add(2 * time.Minute).Before(l.ExpiresAt)
}

// tokenAnswer is the token endpoint's answer, or its error.
type tokenAnswer struct {
	AccessToken       string         `json:"access_token"`
	RefreshToken      string         `json:"refresh_token"`
	IDToken           string         `json:"id_token"`
	ExpiresIn         int64          `json:"expires_in"`
	Scope             string         `json:"scope"`
	EarliestRefreshAt jsontext.Value `json:"earliest_refresh_at"`
	Error             string         `json:"error"`
	ErrorDescription  string         `json:"error_description"`
}

// SignedOutError is a refresh the sign-in refused for good: the owner signs
// in again (https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery).
type SignedOutError struct{ Code string }

func (e *SignedOutError) Error() string { return "the sign-in ended (" + e.Code + ")" }

// signedOut are the refresh errors after which only a new sign-in helps.
var signedOut = []string{"invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired",
	"refresh_token_invalidated", "refresh_token_reused", "invalid_client"}

// Refresh trades l's refresh token for new tokens. The answer's refresh
// token replaces l's, so the caller saves the result before using it, and
// never refreshes one login twice at once. A *SignedOutError means the
// owner must sign in again.
func Refresh(ctx context.Context, hc *http.Client, ep Endpoints, l Login, now time.Time) (Login, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {l.ClientID},
		"refresh_token": {l.RefreshToken},
		"resource":      {Resource},
	}
	t, err := postToken(ctx, hc, ep, form)
	if err != nil {
		return l, err
	}
	l.AccessToken = t.AccessToken
	if t.RefreshToken != "" {
		l.RefreshToken = t.RefreshToken
	}
	if t.IDToken != "" {
		l.IDToken = t.IDToken
	}
	if t.Scope != "" {
		l.Scope = t.Scope
	}
	l.ExpiresAt = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	l.EarliestRefreshAt = earliest(t.EarliestRefreshAt)
	return l, nil
}

// postToken posts form to the token endpoint.
func postToken(ctx context.Context, hc *http.Client, ep Endpoints, form url.Values) (tokenAnswer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.Token, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return tokenAnswer{}, fmt.Errorf("the token endpoint: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return tokenAnswer{}, fmt.Errorf("the token endpoint: %w", err)
	}
	var t tokenAnswer
	_ = json.Unmarshal(b, &t)
	if resp.StatusCode != http.StatusOK {
		if slices.Contains(signedOut, t.Error) {
			return t, &SignedOutError{Code: t.Error}
		}
		return t, fmt.Errorf("the token endpoint answered %d %s", resp.StatusCode, t.Error)
	}
	if t.AccessToken == "" || t.ExpiresIn <= 0 {
		return t, errors.New("the token endpoint answered no access token")
	}
	return t, nil
}

// earliest reads earliest_refresh_at, seconds since the epoch or RFC 3339;
// zero when absent.
func earliest(v jsontext.Value) time.Time {
	var n float64
	if json.Unmarshal(v, &n) == nil && n > 0 {
		return time.Unix(int64(n), 0).UTC()
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
			return time.Unix(n, 0).UTC()
		}
	}
	return time.Time{}
}

// random is n random bytes, base64url without padding.
func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// challenge is PKCE's S256 challenge of verifier.
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// HostID is this computer's stable ext_agent_host_id, made once in dir
// (lodgectl's configuration directory). It names the host, and proves
// nothing.
func HostID(dir string) (string, error) {
	path := filepath.Join(dir, "host-id")
	if b, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(b)); strings.HasPrefix(id, "urn:uuid:") {
			return id, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	u := make([]byte, 16)
	_, _ = rand.Read(u)
	u[6] = u[6]&0x0f | 0x40
	u[8] = u[8]&0x3f | 0x80
	id := fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}
