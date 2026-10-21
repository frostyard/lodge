package inference

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/frostyard/lodge/internal/copilotauth"
)

// copilotCache is a copilot provider's Copilot token, in memory only, and
// the GitHub token it came from.
type copilotCache struct {
	github string
	tok    copilotauth.Token
}

func copilotSignIn(name string) error {
	return signInError{"not signed in: run lodgectl provider login " + name +
		" --copilot on any computer, then sudo lodgectl provider key " + name + " < the token file here"}
}

func copilotRefused(name string, err *copilotauth.RefusedError) error {
	if err.Status == http.StatusForbidden {
		return signInError{err.Error()}
	}
	return signInError{fmt.Sprintf("%s: sign in again with lodgectl provider login %s --copilot, then sudo lodgectl provider key %s",
		err.Error(), name, name)}
}

// copilotToken is a copilot provider's Copilot token, exchanged again when
// it is due, when its GitHub token changed, or when force is set. Exchanges
// are serialized per server.
func (s *Server) copilotToken(ctx context.Context, p *Provider, force bool) (copilotauth.Token, error) {
	github := s.key(ctx, p.Name)
	if github == "" {
		return copilotauth.Token{}, copilotSignIn(p.Name)
	}
	s.copilotMu.Lock()
	defer s.copilotMu.Unlock()
	now := s.now()
	c, ok := s.copilot[p.Name]
	if ok && c.github == github && !force && c.tok.Valid(now) {
		return c.tok, nil
	}
	ectx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tok, err := copilotauth.Exchange(ectx, s.client, s.copilotAuth, github, now)
	var refused *copilotauth.RefusedError
	switch {
	case errors.As(err, &refused):
		delete(s.copilot, p.Name)
		s.log.Warn("copilot sign-in refused", "provider", p.Name, "status", refused.Status)
		return copilotauth.Token{}, copilotRefused(p.Name, refused)
	case err != nil && ok && c.github == github && now.Before(c.tok.ExpiresAt):
		s.log.Warn("copilot token exchange failed; the token is still valid", "provider", p.Name, "err", err)
		return c.tok, nil
	case err != nil:
		return copilotauth.Token{}, err
	}
	if p.Cfg.BaseURL != "" {
		tok.API = p.Cfg.BaseURL
	}
	s.copilot[p.Name] = copilotCache{github: github, tok: tok}
	return tok, nil
}

// copilotLabel names a copilot provider's plan for lodgectl status, from
// its last exchange.
func (s *Server) copilotLabel(provider string) string {
	s.copilotMu.Lock()
	defer s.copilotMu.Unlock()
	if c, ok := s.copilot[provider]; ok && c.tok.SKU != "" {
		return "GitHub Copilot (" + c.tok.SKU + ")"
	}
	return ""
}

// copilotRequest is a request to the Copilot API with the token and the
// editor integration's headers.
func copilotRequest(ctx context.Context, method, u string, body io.Reader, tok copilotauth.Token) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	copilotauth.SetHeaders(req.Header)
	return req, nil
}

// copilotGet GETs path of the Copilot API, exchanging once more after a 401.
func (s *Server) copilotGet(ctx context.Context, p *Provider, path string) (int, []byte, error) {
	for attempt := range 2 {
		tok, err := s.copilotToken(ctx, p, attempt > 0)
		if err != nil {
			return 0, nil, err
		}
		req, err := copilotRequest(ctx, http.MethodGet, tok.API+path, nil, tok)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := s.client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		return resp.StatusCode, b, err
	}
	return http.StatusUnauthorized, nil, nil
}

// probeCopilot checks a copilot provider: a usable sign-in, and GET /models.
func (s *Server) probeCopilot(ctx context.Context, p *Provider) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, CheckTimeout+30*time.Second)
	defer cancel()
	status, _, err := s.copilotGet(ctx, p, "/models")
	var sie signInError
	switch {
	case errors.As(err, &sie):
		return false, sie.reason
	case err != nil:
		return false, "cannot be reached"
	case status/100 != 2:
		return false, fmt.Sprintf("GET /models answered %d", status)
	}
	return true, ""
}

// copilotModels lists the models the Copilot plan offers.
func (s *Server) copilotModels(ctx context.Context, p *Provider) ([]Model, error) {
	status, b, err := s.copilotGet(ctx, p, "/models")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET /models answered %d", status)
	}
	var list struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("GET /models: %w", err)
	}
	out := []Model{}
	for _, m := range list.Data {
		out = append(out, Model{ID: m.ID, Name: m.Name})
	}
	return out, nil
}

// sendCopilot sends an app's request to the Copilot API unchanged but for
// its model, exchanging once more after a 401. A sign-in that fails is a
// signInError.
func (s *Server) sendCopilot(ctx context.Context, p *Provider, endpoint string, members map[string]jsontext.Value, body []byte, accept string) (*http.Response, error) {
	initiator := copilotauth.Initiator(members)
	for attempt := range 2 {
		tok, err := s.copilotToken(ctx, p, attempt > 0)
		if err != nil {
			return nil, err
		}
		req, err := copilotRequest(ctx, http.MethodPost, tok.API+endpoint, bytes.NewReader(body), tok)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", accept)
		req.Header.Set("Openai-Intent", "conversation-edits")
		req.Header.Set("X-Initiator", initiator)
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			continue
		}
		return resp, nil
	}
	return nil, errors.New("unreachable")
}
