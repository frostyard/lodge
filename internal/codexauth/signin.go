package codexauth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"html"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// SignInOptions configure SignIn.
type SignInOptions struct {
	Endpoints Endpoints
	HTTP      *http.Client
	// HostID is this computer's ext_agent_host_id (HostID).
	HostID string
	// ClientID is a registration's issued client, to sign in to it again;
	// empty registers lodge as a new client.
	ClientID string
	// Email is sent as login_hint, and IDToken as id_token_hint, when set.
	Email   string
	IDToken string
	// Port is the loopback callback's port: DefaultPort when 0, falling
	// back to any free port if that one is taken; any free port when
	// negative.
	Port int
	// Open opens the authorize URL in a browser; the URL is printed anyway.
	Open func(url string)
	// Out gets the instructions.
	Out io.Writer
	Now func() time.Time
}

// SignIn runs the sign-in on this computer: it listens on a loopback
// callback, sends the owner's browser to the authorize URL, exchanges the
// code with PKCE, verifies the ID token and the plan's scope, and returns
// the login.
func SignIn(ctx context.Context, o SignInOptions) (Login, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 30 * time.Second}
	}
	port := max(o.Port, 0)
	if o.Port == 0 {
		port = DefaultPort
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil && o.Port == 0 {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return Login{}, fmt.Errorf("the sign-in's callback: %w", err)
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d%s", ln.Addr().(*net.TCPAddr).Port, CallbackPath)

	state, nonce, verifier := random(32), random(32), random(48)
	client := cmpOr(o.ClientID, DynamicClient)
	q := url.Values{
		"client_id":             {client},
		"ext_agent_host_id":     {o.HostID},
		"response_type":         {"code"},
		"redirect_uri":          {redirect},
		"scope":                 {Scope},
		"resource":              {Resource},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge_method": {"S256"},
		"code_challenge":        {challenge(verifier)},
	}
	if o.ClientID == "" {
		q.Set("agent_name_hint", AgentName)
	}
	if o.Email != "" {
		q.Set("login_hint", o.Email)
	}
	if o.IDToken != "" {
		q.Set("id_token_hint", o.IDToken)
	}
	authURL := o.Endpoints.Authorize + "?" + q.Encode()

	type result struct {
		code, client string
		err          error
	}
	got := make(chan result, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != CallbackPath {
			http.NotFound(w, r)
			return
		}
		v := r.URL.Query()
		if v.Get("state") != state {
			page(w, http.StatusBadRequest, "This sign-in link is stale. Run lodgectl provider login again.")
			return // a stale or forged callback; keep waiting for ours
		}
		var res result
		switch {
		case v.Get("error") != "":
			res.err = fmt.Errorf("the sign-in was refused: %s %s", v.Get("error"), v.Get("error_description"))
		case v.Get("code") == "":
			res.err = errors.New("the callback carried no code")
		default:
			res.code, res.client = v.Get("code"), v.Get("client_id")
		}
		if res.err != nil {
			page(w, http.StatusOK, "lodge was not signed in. You can close this tab.")
		} else {
			page(w, http.StatusOK, "lodge is signed in to your ChatGPT plan. You can close this tab and return to the terminal.")
		}
		select {
		case got <- res:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	if o.Out != nil {
		fmt.Fprintf(o.Out, "Continue with ChatGPT in your browser. If it did not open, visit:\n\n  %s\n\n", authURL)
	}
	if o.Open != nil {
		o.Open(authURL)
	}
	var res result
	select {
	case <-ctx.Done():
		return Login{}, fmt.Errorf("waiting for the browser: %w", ctx.Err())
	case res = <-got:
	}
	if res.err != nil {
		return Login{}, res.err
	}
	issued := res.client
	switch {
	case o.ClientID == "" && (issued == "" || issued == DynamicClient):
		return Login{}, errors.New("the registration issued no client_id")
	case o.ClientID != "" && issued != "" && issued != o.ClientID:
		return Login{}, fmt.Errorf("the callback named client %s, not %s", issued, o.ClientID)
	case o.ClientID != "":
		issued = o.ClientID
	}

	t, err := postToken(ctx, o.HTTP, o.Endpoints, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {issued},
		"code":          {res.code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirect},
		"resource":      {Resource},
	})
	if err != nil {
		return Login{}, err
	}
	now := o.Now()
	claims, err := VerifyIDToken(ctx, o.HTTP, o.Endpoints, t.IDToken, issued, nonce, now)
	if err != nil {
		return Login{}, err
	}
	if !HasPlanScope(t.Scope) {
		return Login{}, fmt.Errorf("the plan's use was not granted (scope %q lacks %s)", t.Scope, PlanScope)
	}
	return Login{
		Type: Type, ClientID: issued, Account: claims.Sub, Email: claims.Email, Scope: t.Scope,
		AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, IDToken: t.IDToken,
		ExpiresAt: now.Add(time.Duration(t.ExpiresIn) * time.Second), EarliestRefreshAt: earliest(t.EarliestRefreshAt),
	}, nil
}

func page(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>lodge</title><p>%s</p>\n", html.EscapeString(msg))
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Claims are the ID token's claims lodge reads.
type Claims struct {
	Iss   string         `json:"iss"`
	Sub   string         `json:"sub"`
	Aud   jsontext.Value `json:"aud"` // a string, or an array of them
	Exp   float64        `json:"exp"`
	Nonce string         `json:"nonce"`
	Email string         `json:"email"`
}

// VerifyIDToken checks raw's RS256 signature against the issuer's
// published keys, and its issuer, audience, expiry and nonce.
func VerifyIDToken(ctx context.Context, hc *http.Client, ep Endpoints, raw, clientID, nonce string, now time.Time) (Claims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("the ID token is not a JWT")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &head); err != nil {
		return Claims{}, fmt.Errorf("the ID token's header: %w", err)
	}
	if head.Alg != "RS256" {
		return Claims{}, fmt.Errorf("the ID token is signed with %q, not RS256", head.Alg)
	}
	key, err := issuerKey(ctx, hc, ep, head.Kid)
	if err != nil {
		return Claims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, errors.New("the ID token's signature is not base64url")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return Claims{}, errors.New("the ID token's signature does not verify")
	}
	var c Claims
	if err := decodePart(parts[1], &c); err != nil {
		return Claims{}, fmt.Errorf("the ID token's claims: %w", err)
	}
	var auds []string
	var one string
	if json.Unmarshal(c.Aud, &one) == nil {
		auds = []string{one}
	} else {
		_ = json.Unmarshal(c.Aud, &auds)
	}
	switch {
	case c.Iss != ep.Issuer:
		return Claims{}, fmt.Errorf("the ID token's issuer is %q", c.Iss)
	case !slices.Contains(auds, clientID):
		return Claims{}, errors.New("the ID token is not for this client")
	case c.Exp == 0 || !now.Before(time.Unix(int64(c.Exp), 0).Add(time.Minute)):
		return Claims{}, errors.New("the ID token has expired")
	case c.Nonce != nonce:
		return Claims{}, errors.New("the ID token's nonce does not match")
	case c.Sub == "":
		return Claims{}, errors.New("the ID token names no account")
	}
	return c, nil
}

func decodePart(s string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// issuerKey finds the issuer's RSA key kid through discovery.
func issuerKey(ctx context.Context, hc *http.Client, ep Endpoints, kid string) (*rsa.PublicKey, error) {
	var disc struct {
		JWKSURI string `json:"jwks_uri"`
	}
	if err := getJSON(ctx, hc, strings.TrimSuffix(ep.Issuer, "/")+"/.well-known/openid-configuration", &disc); err != nil {
		return nil, fmt.Errorf("the issuer's discovery: %w", err)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := getJSON(ctx, hc, disc.JWKSURI, &set); err != nil {
		return nil, fmt.Errorf("the issuer's keys: %w", err)
	}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || (kid != "" && k.Kid != kid) {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) > 4 {
			continue
		}
		return &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}, nil
	}
	return nil, fmt.Errorf("the issuer publishes no RSA key %q", kid)
}

func getJSON(ctx context.Context, hc *http.Client, u string, v any) error {
	if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://127.0.0.1") {
		return fmt.Errorf("refusing %q: not https", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s answered %d", u, resp.StatusCode)
	}
	return json.UnmarshalRead(io.LimitReader(resp.Body, 1<<20), v)
}
