package inference

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/frostyard/lodge/internal/codexauth"
	"github.com/frostyard/lodge/internal/store"
)

// CodexBaseURL is where a codex provider sends the plan's requests, unless
// its configuration names another.
const CodexBaseURL = "https://api.openai.com/v1"

// baseURL is a provider's API base.
func baseURL(pc ProviderConfig) string {
	if pc.BaseURL == "" && pc.Type == TypeCodex {
		return CodexBaseURL
	}
	return pc.BaseURL
}

// ErrNotCodex means a sign-in was given to a provider of another type.
var ErrNotCodex = errors.New("the provider is not of type codex")

// signInError is why a codex provider has no usable sign-in; its text is
// the provider's reason in lodgectl status.
type signInError struct{ reason string }

func (e signInError) Error() string { return e.reason }

func notSignedIn(name string) error {
	return signInError{"not signed in: run lodgectl provider login " + name +
		" on a computer with a browser, then sudo lodgectl provider import " + name + " < the login file here"}
}

func signedOut(name, code string, l codexauth.Login) error {
	return signInError{fmt.Sprintf("signed out (%s): sign in again with lodgectl provider login %s --client-id %s, then import it",
		code, name, l.ClientID)}
}

// login is provider's stored sign-in.
func (s *Server) login(ctx context.Context, provider string) (codexauth.Login, bool, error) {
	var l codexauth.Login
	if s.db == nil {
		return l, false, nil
	}
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT login FROM provider_logins WHERE provider = ?`, provider).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return l, false, nil
	}
	if err != nil {
		return l, false, fmt.Errorf("read a provider's sign-in: %w", err)
	}
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		return l, false, fmt.Errorf("read a provider's sign-in: %w", err)
	}
	return l, true, nil
}

func (s *Server) saveLogin(ctx context.Context, provider string, l codexauth.Login) error {
	b, err := json.Marshal(l)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO provider_logins (provider, login, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (provider) DO UPDATE SET login = excluded.login, updated_at = excluded.updated_at`,
		provider, string(b), s.now().UTC().Format(store.TimeFormat))
	if err != nil {
		return fmt.Errorf("save a provider's sign-in: %w", err)
	}
	return nil
}

// ImportLogin replaces a codex provider's sign-in with a login file's.
func (s *Server) ImportLogin(ctx context.Context, provider string, l codexauth.Login) error {
	pc, ok := s.state().cfg.Providers[provider]
	switch {
	case !ok:
		return ErrNoProvider
	case pc.Type != TypeCodex:
		return ErrNotCodex
	}
	if err := l.Validate(); err != nil {
		return fmt.Errorf("the login file: %w", err)
	}
	s.loginMu.Lock()
	err := s.saveLogin(ctx, provider, l)
	s.loginMu.Unlock()
	if err != nil {
		return err
	}
	s.CheckSoon()
	return nil
}

// accessToken is a codex provider's access token, refreshed first when it
// is due or force is set. Refreshes are serialized, and the rotated
// refresh token is saved before the access token is used.
func (s *Server) accessToken(ctx context.Context, p *Provider, force bool) (string, error) {
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	l, ok, err := s.login(ctx, p.Name)
	switch {
	case err != nil:
		return "", err
	case !ok:
		return "", notSignedIn(p.Name)
	case !l.SignedIn():
		return "", signedOut(p.Name, "earlier", l)
	}
	now := s.now()
	if !force && !l.Due(now) {
		return l.AccessToken, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	next, err := codexauth.Refresh(rctx, s.client, s.auth, l, now)
	var out *codexauth.SignedOutError
	switch {
	case errors.As(err, &out):
		s.log.Warn("codex sign-in ended", "provider", p.Name, "code", out.Code)
		if serr := s.saveLogin(ctx, p.Name, l.SignOut()); serr != nil {
			s.log.Error("clear an ended sign-in", "provider", p.Name, "err", serr)
		}
		return "", signedOut(p.Name, out.Code, l)
	case err != nil && now.Before(l.ExpiresAt):
		s.log.Warn("codex refresh failed; the access token is still valid", "provider", p.Name, "err", err)
		return l.AccessToken, nil
	case err != nil:
		return "", fmt.Errorf("refresh the sign-in: %w", err)
	}
	if err := s.saveLogin(ctx, p.Name, next); err != nil {
		// The new refresh token is lost with it: the next refresh will be
		// refused, and the owner signs in again.
		s.log.Error("save a refreshed sign-in", "provider", p.Name, "err", err)
	}
	return next.AccessToken, nil
}

// loginLabel names a codex provider's account for lodgectl status.
func (s *Server) loginLabel(ctx context.Context, provider string) string {
	l, ok, err := s.login(ctx, provider)
	if err != nil || !ok {
		return ""
	}
	return fmt.Sprintf("%s (client %s)", cmpString(l.Email, l.Account), l.ClientID)
}

// probeCodex checks a codex provider: a usable sign-in, and GET /models.
func (s *Server) probeCodex(ctx context.Context, p *Provider) (bool, string) {
	for attempt := range 2 {
		tok, err := s.accessToken(ctx, p, attempt > 0)
		if err != nil {
			return false, err.Error()
		}
		cctx, cancel := context.WithTimeout(ctx, CheckTimeout)
		status, _, err := s.get(cctx, baseURL(p.Cfg)+"/models", tok)
		cancel()
		switch {
		case err != nil:
			return false, "cannot be reached"
		case status == http.StatusUnauthorized && attempt == 0:
			continue
		case status/100 != 2:
			return false, fmt.Sprintf("GET /models answered %d", status)
		}
		return true, ""
	}
	return false, "the plan refused the sign-in"
}

// get GETs u with a bearer token, and reads at most 4 MiB of the answer.
func (s *Server) get(ctx context.Context, u, token string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	return resp.StatusCode, b, err
}

// Model is one of a provider's models, for lodgectl provider models.
type Model struct {
	ID   string `json:"id"`
	Name string `json:"name,omitzero"`
}

// Models lists a provider's models: the slugs the signed-in plan offers
// for codex, or GET /models' ids for openai and copilot.
func (s *Server) Models(ctx context.Context, provider string) ([]Model, error) {
	p, ok := s.state().providers[provider]
	if !ok {
		return nil, ErrNoProvider
	}
	var tok string
	switch p.Cfg.Type {
	case TypeCodex:
		t, err := s.accessToken(ctx, p, false)
		if err != nil {
			return nil, err
		}
		tok = t
	case TypeOpenAI:
		tok = s.key(ctx, provider)
	case TypeCopilot:
		return s.copilotModels(ctx, p)
	default:
		return nil, fmt.Errorf("provider %s is not built", provider)
	}
	status, b, err := s.get(ctx, baseURL(p.Cfg)+"/models", tok)
	if err != nil {
		return nil, fmt.Errorf("GET /models: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET /models answered %d", status)
	}
	var list struct {
		Models []struct {
			Slug        string `json:"slug"`
			DisplayName string `json:"display_name"`
			Visibility  string `json:"visibility"`
		} `json:"models"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("GET /models: %w", err)
	}
	out := []Model{}
	for _, m := range list.Models {
		if m.Visibility == "list" || m.Visibility == "" {
			out = append(out, Model{ID: m.Slug, Name: m.DisplayName})
		}
	}
	for _, m := range list.Data {
		out = append(out, Model{ID: m.ID})
	}
	return out, nil
}

// forwardCodex sends a request to a codex provider as an unstored,
// streamed Responses request, and answers the app in the shape it asked
// for. It returns the status it answered.
func (s *Server) forwardCodex(w http.ResponseWriter, r *http.Request, p *Provider, endpoint string,
	members map[string]jsontext.Value, stream bool, rec *Record) int {
	ctx := r.Context()
	var payload []byte
	var chat *chatRequest
	var err error
	if endpoint == "/chat/completions" {
		var body []byte
		if body, err = json.Marshal(members); err == nil {
			payload, chat, err = chatToResponses(body)
		}
	} else {
		payload, err = sanitizeResponses(members)
	}
	if err != nil {
		apiError(w, http.StatusBadRequest, "invalid_request_error", "unsupported", err.Error())
		return http.StatusBadRequest
	}

	var resp *http.Response
	var sent time.Time
	for attempt := 0; ; attempt++ {
		tok, err := s.accessToken(ctx, p, attempt > 0)
		if err != nil {
			var si signInError
			if errors.As(err, &si) && p.set(false, si.reason) {
				s.log.Warn("inference provider unavailable", "provider", p.Name, "reason", si.reason)
			}
			apiError(w, http.StatusServiceUnavailable, "server_error", "provider_unavailable",
				fmt.Sprintf("provider %s is unavailable: %v", p.Name, err))
			return http.StatusServiceUnavailable
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL(p.Cfg)+"/responses", bytes.NewReader(payload))
		if err != nil {
			apiError(w, http.StatusBadGateway, "server_error", "provider_failed", "lodge cannot build the provider's request")
			return http.StatusBadGateway
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		sent = s.now()
		resp, err = s.client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return 499
			}
			s.log.Warn("inference provider failed", "provider", p.Name, "err", err)
			p.set(false, "a request could not reach it")
			s.CheckSoon()
			apiError(w, http.StatusBadGateway, "server_error", "provider_failed", "provider "+p.Name+" could not be reached")
			return http.StatusBadGateway
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			continue // refresh, and try once more
		}
		break
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var e struct {
			Error  *apiErr `json:"error"`
			Detail string  `json:"detail"`
		}
		_ = json.Unmarshal(b, &e)
		ae := apiErr{Message: e.Detail}
		if e.Error != nil {
			ae = *e.Error
		}
		status := resp.StatusCode
		if ae.Code != "" && planStatus(ae.Code) != http.StatusBadGateway {
			status = planStatus(ae.Code)
		}
		s.log.Warn("the codex provider refused a request", "provider", p.Name, "status", resp.StatusCode, "code", ae.Code)
		apiError(w, status, errorType(status), cmpString(ae.Code, "provider_error"),
			cmpString(planMessage(ae), fmt.Sprintf("provider %s answered %d", p.Name, resp.StatusCode)))
		return status
	}

	first := func() {
		if rec.FirstByte < 0 {
			rec.FirstByte = s.now().Sub(sent)
		}
	}
	setUsage := func(o *responseObject) {
		if o != nil && o.Usage != nil {
			rec.Input, rec.Output = o.Usage.InputTokens, o.Usage.OutputTokens
		}
	}
	rc := http.NewResponseController(w)
	flush := func() { _ = rc.Flush() }

	switch {
	case chat != nil && stream:
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		c := &chunker{w: w, flush: flush, created: s.now().Unix(),
			usage: chat.StreamOptions != nil && chat.StreamOptions.IncludeUsage}
		finished := false
		err := events(resp.Body, func(ev event, _ []byte) bool {
			first()
			if ev.Type == "response.completed" || ev.Type == "response.incomplete" {
				setUsage(ev.Response)
			}
			done, werr := c.event(ev)
			finished = done
			return !done && werr == nil
		})
		if !finished && ctx.Err() == nil {
			msg := errEarlyEnd.Error()
			if err != nil {
				msg += ": " + err.Error()
			}
			_ = c.write(map[string]any{"error": map[string]any{"message": msg, "type": "server_error", "code": "provider_failed"}})
		}
		return http.StatusOK

	case chat != nil:
		var final *responseObject
		var items []outputItem
		var failed *apiErr
		_ = events(resp.Body, func(ev event, _ []byte) bool {
			first()
			if e, ok := ev.failure(); ok {
				failed = &e
				return false
			}
			if ev.Type == "response.output_item.done" && ev.Item != nil {
				items = append(items, *ev.Item)
			}
			if ev.Type == "response.completed" || ev.Type == "response.incomplete" {
				final = ev.Response
				return false
			}
			return true
		})
		return s.answerWhole(w, final, failed, func(o *responseObject) any {
			setUsage(o)
			return chatCompletion(o, items, s.now().Unix())
		})

	case stream:
		// An app speaking Responses gets the provider's events as they come.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		tap := newTap(true)
		buf := make([]byte, 32<<10)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				first()
				tap.Write(buf[:n])
				if _, werr := w.Write(buf[:n]); werr != nil {
					break
				}
				flush()
			}
			if err != nil {
				break
			}
		}
		tap.Finish()
		rec.Input, rec.Output = tap.in, tap.out
		return http.StatusOK

	default:
		var final map[string]jsontext.Value
		var obj *responseObject
		var items []jsontext.Value
		var failed *apiErr
		_ = events(resp.Body, func(ev event, raw []byte) bool {
			first()
			if e, ok := ev.failure(); ok {
				failed = &e
				return false
			}
			var m map[string]jsontext.Value
			if json.Unmarshal(raw, &m) != nil {
				return true
			}
			switch ev.Type {
			case "response.output_item.done":
				items = append(items, m["item"])
			case "response.completed", "response.incomplete":
				_ = json.Unmarshal(m["response"], &final)
				obj = ev.Response
				return false
			}
			return true
		})
		if obj != nil && final == nil {
			obj = nil
		}
		if obj != nil && len(obj.Output) == 0 && len(items) > 0 {
			// An unstored response's completed event has no output: give
			// the app the items its stream sent.
			final["output"], _ = json.Marshal(items)
		}
		return s.answerWhole(w, obj, failed, func(o *responseObject) any {
			setUsage(o)
			return final
		})
	}
}

// answerWhole answers a non-streamed request once its stream has ended.
func (s *Server) answerWhole(w http.ResponseWriter, final *responseObject, failed *apiErr, body func(*responseObject) any) int {
	switch {
	case failed != nil:
		status := planStatus(failed.Code)
		apiError(w, status, errorType(status), cmpString(failed.Code, "provider_error"), planMessage(*failed))
		return status
	case final == nil:
		apiError(w, http.StatusBadGateway, "server_error", "provider_failed", errEarlyEnd.Error())
		return http.StatusBadGateway
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.MarshalWrite(w, body(final))
	return http.StatusOK
}

// errorType is an OpenAI error type for status.
func errorType(status int) string {
	switch {
	case status == http.StatusTooManyRequests:
		return "rate_limit_error"
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "permission_error"
	case status/100 == 4:
		return "invalid_request_error"
	}
	return "server_error"
}
