package copilotauth

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// github is a fake of GitHub's device flow and token exchange.
type github struct {
	srv *httptest.Server

	mu sync.Mutex
	// polls answers each poll of the access token, in order.
	polls    []string
	polled   int
	exchange func(r *http.Request) (int, string)
}

func newGitHub(t *testing.T) *github {
	g := &github{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("client_id") != EditorClientID || r.PostForm.Get("scope") != Scope ||
			r.Header.Get("Accept") != "application/json" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"device_code":"dc-1","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","interval":5,"expires_in":900}`)
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.PostForm.Get("client_id") != EditorClientID || r.PostForm.Get("device_code") != "dc-1" ||
			r.PostForm.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
			io.WriteString(w, `{"error":"incorrect_device_code_or_client"}`)
			return
		}
		answer := g.polls[min(g.polled, len(g.polls)-1)]
		g.polled++
		io.WriteString(w, answer)
	})
	mux.HandleFunc("GET /copilot_internal/v2/token", func(w http.ResponseWriter, r *http.Request) {
		status, body := g.exchange(r)
		w.WriteHeader(status)
		io.WriteString(w, body)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

func (g *github) endpoints() Endpoints {
	return Endpoints{DeviceCode: g.srv.URL + "/login/device/code", AccessToken: g.srv.URL + "/login/oauth/access_token",
		Exchange: g.srv.URL + "/copilot_internal/v2/token"}
}

func TestSignIn(t *testing.T) {
	g := newGitHub(t)
	g.polls = []string{`{"error":"authorization_pending"}`, `{"error":"slow_down","interval":10}`, `{"access_token":"gho_owner","token_type":"bearer"}`}
	var shown []string
	var slept []time.Duration
	tok, err := SignIn(t.Context(), DeviceOptions{Endpoints: g.endpoints(),
		Show:  func(u, code string) { shown = append(shown, u, code) },
		Sleep: func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }})
	if err != nil || tok != "gho_owner" {
		t.Fatalf("SignIn = %q, %v", tok, err)
	}
	if len(shown) != 2 || shown[0] != "https://github.com/login/device" || shown[1] != "ABCD-1234" {
		t.Errorf("shown = %q", shown)
	}
	// The interval GitHub asked for, then the slower one slow_down asked for.
	if want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second}; len(slept) != 3 ||
		slept[0] != want[0] || slept[1] != want[1] || slept[2] != want[2] {
		t.Errorf("slept = %v", slept)
	}
}

func TestSignInRefusals(t *testing.T) {
	for name, answer := range map[string]string{
		"denied":  `{"error":"access_denied","error_description":"the user said no"}`,
		"expired": `{"error":"expired_token"}`,
	} {
		t.Run(name, func(t *testing.T) {
			g := newGitHub(t)
			g.polls = []string{answer}
			_, err := SignIn(t.Context(), DeviceOptions{Endpoints: g.endpoints(),
				Sleep: func(context.Context, time.Duration) error { return nil }})
			var de *DeviceError
			if !errors.As(err, &de) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	// A canceled sign-in stops waiting.
	g := newGitHub(t)
	g.polls = []string{`{"error":"authorization_pending"}`}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := SignIn(ctx, DeviceOptions{Endpoints: g.endpoints()}); !errors.Is(err, context.Canceled) {
		t.Errorf("a canceled sign-in: %v", err)
	}
}

func TestExchange(t *testing.T) {
	g := newGitHub(t)
	var seen http.Header
	g.exchange = func(r *http.Request) (int, string) {
		seen = r.Header.Clone()
		if r.Header.Get("Authorization") != "token gho_owner" {
			return http.StatusUnauthorized, `{"message":"Bad credentials"}`
		}
		return http.StatusOK, `{"token":"tid=1;exp=2;proxy-ep=proxy.individual.githubcopilot.com","expires_at":1792584000,` +
			`"refresh_in":1500,"sku":"monthly_subscriber","endpoints":{"api":"https://api.individual.githubcopilot.com/"}}`
	}
	now := time.Unix(1792582200, 0) // 30 minutes before the expiry
	tok, err := Exchange(t.Context(), nil, g.endpoints(), "gho_owner", now)
	if err != nil {
		t.Fatal(err)
	}
	if tok.API != "https://api.individual.githubcopilot.com" || tok.SKU != "monthly_subscriber" ||
		!tok.ExpiresAt.Equal(time.Unix(1792584000, 0)) || !tok.RenewAt.Equal(now.Add(1500*time.Second)) {
		t.Errorf("token = %+v", tok)
	}
	if !tok.Valid(now) || tok.Valid(now.Add(1500*time.Second)) {
		t.Errorf("Valid is wrong around RenewAt")
	}
	for k, v := range map[string]string{"Copilot-Integration-Id": IntegrationID, "Editor-Version": EditorVersion, "User-Agent": UserAgent} {
		if seen.Get(k) != v {
			t.Errorf("the exchange's %s = %q", k, seen.Get(k))
		}
	}
	// refresh_in later than two minutes before the expiry: the expiry wins.
	g.exchange = func(*http.Request) (int, string) {
		return http.StatusOK, `{"token":"t","expires_at":1792584000,"refresh_in":99999,"endpoints":{"api":"http://plain.example"}}`
	}
	tok, err = Exchange(t.Context(), nil, g.endpoints(), "gho_owner", now)
	if err != nil || !tok.RenewAt.Equal(time.Unix(1792584000-120, 0)) || tok.API != DefaultAPI {
		t.Errorf("token = %+v, %v (a plain-HTTP endpoint is ignored)", tok, err)
	}
}

func TestExchangeRefusals(t *testing.T) {
	g := newGitHub(t)
	for status, want := range map[int]string{401: "refused the token (401)", 404: "refused the token (404)", 403: "no Copilot access"} {
		g.exchange = func(*http.Request) (int, string) { return status, `{}` }
		_, err := Exchange(t.Context(), nil, g.endpoints(), "gho_x", time.Now())
		var re *RefusedError
		if !errors.As(err, &re) || re.Status != status || !strings.Contains(err.Error(), want) {
			t.Errorf("%d: %v", status, err)
		}
	}
	g.exchange = func(*http.Request) (int, string) { return 500, `` }
	if _, err := Exchange(t.Context(), nil, g.endpoints(), "gho_x", time.Now()); err == nil || errors.As(err, new(*RefusedError)) {
		t.Errorf("a 500 is not a refusal: %v", err)
	}
	g.exchange = func(*http.Request) (int, string) { return 200, `{"expires_at":1}` }
	if _, err := Exchange(t.Context(), nil, g.endpoints(), "gho_x", time.Now()); err == nil {
		t.Error("an answer without a token was taken")
	}
}

func TestInitiator(t *testing.T) {
	for body, want := range map[string]string{
		`{"messages":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`:                    "user",
		`{"messages":[{"role":"user","content":"hi"},{"role":"assistant"},{"role":"tool","content":"x"}]}`: "agent",
		`{"messages":[]}`: "agent",
		`{"input":"hi"}`:  "user",
		`{"input":[{"role":"user","content":"hi"}]}`:                             "user",
		`{"input":[{"type":"function_call_output","call_id":"c","output":"x"}]}`: "agent",
		`{}`: "agent",
	} {
		var m map[string]jsontext.Value
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		if got := Initiator(m); got != want {
			t.Errorf("Initiator(%s) = %q, want %q", body, got, want)
		}
	}
}
