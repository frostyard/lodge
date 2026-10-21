// Package copilotauth signs a copilot provider in to the owner's GitHub
// Copilot plan (ADR-0016; docs/specs/inference.md, The copilot provider):
// GitHub's device flow for a GitHub token, and the exchange of that token
// for the short-lived Copilot token the Copilot API takes.
package copilotauth

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// EditorClientID is Copilot's editor OAuth client, the one whose tokens the
// token exchange takes (ADR-0016).
const EditorClientID = "Iv1.b507a08c87ecfe98"

// Scope is what the device flow asks for.
const Scope = "read:user"

// DefaultAPI is the Copilot API when an exchange names no endpoint.
const DefaultAPI = "https://api.githubcopilot.com"

// Endpoints are GitHub's URLs for the sign-in and the exchange.
type Endpoints struct {
	DeviceCode  string
	AccessToken string
	Exchange    string
}

// GitHub is github.com's endpoints.
var GitHub = Endpoints{
	DeviceCode:  "https://github.com/login/device/code",
	AccessToken: "https://github.com/login/oauth/access_token",
	Exchange:    "https://api.github.com/copilot_internal/v2/token",
}

// The headers an editor integration sends (ADR-0016). They are sent on the
// exchange and on every request to the Copilot API.
const (
	IntegrationID       = "vscode-chat"
	EditorVersion       = "vscode/1.107.0"
	EditorPluginVersion = "copilot-chat/0.35.0"
	UserAgent           = "GitHubCopilotChat/0.35.0"
)

// SetHeaders sets the editor integration's headers on h.
func SetHeaders(h http.Header) {
	h.Set("Copilot-Integration-Id", IntegrationID)
	h.Set("Editor-Version", EditorVersion)
	h.Set("Editor-Plugin-Version", EditorPluginVersion)
	h.Set("User-Agent", UserAgent)
}

// maxAnswer bounds every answer read here.
const maxAnswer = 1 << 20

// --- The device flow ---

// DeviceOptions are a device flow's parameters.
type DeviceOptions struct {
	Endpoints Endpoints
	Client    *http.Client // [http.DefaultClient]
	// Show tells the owner where to enter the code.
	Show func(verificationURI, userCode string)
	// Sleep waits d, or until ctx ends [a timer].
	Sleep func(ctx context.Context, d time.Duration) error
}

// DeviceError is the device flow's refusal: the code expired, or the owner
// denied it.
type DeviceError struct{ Code, Description string }

func (e *DeviceError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("GitHub's device flow ended: %s: %s", e.Code, e.Description)
	}
	return "GitHub's device flow ended: " + e.Code
}

// SignIn runs GitHub's device flow under the editor client and returns the
// GitHub token.
func SignIn(ctx context.Context, o DeviceOptions) (string, error) {
	if o.Client == nil {
		o.Client = http.DefaultClient
	}
	if o.Sleep == nil {
		o.Sleep = sleep
	}
	var dc struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	if err := postForm(ctx, o.Client, o.Endpoints.DeviceCode, url.Values{"client_id": {EditorClientID}, "scope": {Scope}}, &dc); err != nil {
		return "", fmt.Errorf("start GitHub's device flow: %w", err)
	}
	u, err := url.Parse(dc.VerificationURI)
	if dc.DeviceCode == "" || dc.UserCode == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", errors.New("start GitHub's device flow: an incomplete answer")
	}
	if o.Show != nil {
		o.Show(u.String(), dc.UserCode)
	}
	interval := time.Duration(max(dc.Interval, 5)) * time.Second
	deadline := time.Now().Add(time.Duration(max(dc.ExpiresIn, 60)) * time.Second)
	for time.Now().Before(deadline) {
		if err := o.Sleep(ctx, interval); err != nil {
			return "", err
		}
		var at struct {
			AccessToken      string `json:"access_token"`
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
			Interval         int    `json:"interval"`
		}
		err := postForm(ctx, o.Client, o.Endpoints.AccessToken, url.Values{"client_id": {EditorClientID},
			"device_code": {dc.DeviceCode}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}}, &at)
		switch {
		case err != nil:
			return "", fmt.Errorf("GitHub's device flow: %w", err)
		case at.AccessToken != "":
			return at.AccessToken, nil
		case at.Error == "authorization_pending":
		case at.Error == "slow_down":
			interval = max(time.Duration(at.Interval)*time.Second, interval+5*time.Second)
		case at.Error != "":
			return "", &DeviceError{Code: at.Error, Description: at.ErrorDescription}
		default:
			return "", errors.New("GitHub's device flow: an answer with neither a token nor an error")
		}
	}
	return "", &DeviceError{Code: "expired_token"}
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func postForm(ctx context.Context, c *http.Client, u string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s answered %d", u, resp.StatusCode)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%s: %w", u, err)
	}
	return nil
}

// --- The exchange ---

// Token is a Copilot token and where to use it.
type Token struct {
	Token string
	// API is the Copilot API's base URL, with no trailing slash.
	API string
	// SKU names the plan, for lodgectl status.
	SKU       string
	ExpiresAt time.Time
	// RenewAt is when to exchange again: the answer's refresh_in, and never
	// later than two minutes before the expiry.
	RenewAt time.Time
}

// Valid reports whether t may still be used at now without renewing.
func (t Token) Valid(now time.Time) bool { return t.Token != "" && now.Before(t.RenewAt) }

// RefusedError is an exchange GitHub refused: the GitHub token is no
// longer valid (401, 404), or the account has no Copilot (403).
type RefusedError struct{ Status int }

func (e *RefusedError) Error() string {
	if e.Status == http.StatusForbidden {
		return "this GitHub account has no Copilot access"
	}
	return fmt.Sprintf("GitHub refused the token (%d)", e.Status)
}

// Exchange trades a GitHub token for a Copilot token.
func Exchange(ctx context.Context, c *http.Client, e Endpoints, githubToken string, now time.Time) (Token, error) {
	if c == nil {
		c = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.Exchange, nil)
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Accept", "application/json")
	SetHeaders(req.Header)
	resp, err := c.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("the Copilot token exchange: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return Token{}, fmt.Errorf("the Copilot token exchange: %w", err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return Token{}, &RefusedError{Status: resp.StatusCode}
	default:
		return Token{}, fmt.Errorf("the Copilot token exchange answered %d", resp.StatusCode)
	}
	var a struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
		RefreshIn int64  `json:"refresh_in"`
		SKU       string `json:"sku"`
		Endpoints struct {
			API string `json:"api"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return Token{}, fmt.Errorf("the Copilot token exchange: %w", err)
	}
	if a.Token == "" || a.ExpiresAt == 0 {
		return Token{}, errors.New("the Copilot token exchange: an answer without a token or an expiry")
	}
	t := Token{Token: a.Token, SKU: a.SKU, ExpiresAt: time.Unix(a.ExpiresAt, 0), API: DefaultAPI}
	if api, err := url.Parse(a.Endpoints.API); err == nil && api.Scheme == "https" && api.Host != "" && api.User == nil &&
		api.RawQuery == "" && api.Fragment == "" {
		t.API = strings.TrimRight(api.String(), "/")
	}
	t.RenewAt = t.ExpiresAt.Add(-2 * time.Minute)
	if a.RefreshIn > 0 {
		if r := now.Add(time.Duration(a.RefreshIn) * time.Second); r.Before(t.RenewAt) {
			t.RenewAt = r
		}
	}
	return t, nil
}

// Initiator is X-Initiator for a request body: "user" when its last
// message, or its input, is the user's, and "agent" otherwise (ADR-0016).
func Initiator(members map[string]jsontext.Value) string {
	if raw, ok := members["messages"]; ok {
		var msgs []struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(raw, &msgs) == nil && len(msgs) > 0 && msgs[len(msgs)-1].Role == "user" {
			return "user"
		}
		return "agent"
	}
	if raw, ok := members["input"]; ok {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return "user"
		}
		var items []struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(raw, &items) == nil && len(items) > 0 && items[len(items)-1].Role == "user" {
			return "user"
		}
	}
	return "agent"
}
