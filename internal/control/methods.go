package control

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"strings"
	"time"

	"github.com/frostyard/lodge/internal/agents"
	"github.com/frostyard/lodge/internal/allow"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/codexauth"
	"github.com/frostyard/lodge/internal/inference"
	"github.com/frostyard/lodge/internal/peer"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/tokens"
)

// Status is lodge.status's answer.
type Status struct {
	Nodes     []Node                     `json:"nodes"`
	Apps      []AppStatus                `json:"apps"`
	Sessions  int                        `json:"sessions"`
	Providers []inference.ProviderStatus `json:"providers"`
	// InferenceRefused is why the inference configuration was refused.
	InferenceRefused string `json:"inferenceRefused,omitzero"`
	// AgentSocket is the agent socket and its agents.
	AgentSocket *agents.Status `json:"agentSocket,omitzero"`
}

// AppStatus is one app in lodge.status.
type AppStatus struct {
	ID     string     `json:"id"`
	State  apps.State `json:"state"`
	Reason string     `json:"reason,omitzero"`
}

// Issued is lodge.issueToken's answer: the token, shown this once.
type Issued struct {
	Token string `json:"token"`
	App   string `json:"app"`
	URL   string `json:"url"`
	// Agent is the user the token is for, on the agent socket at Socket.
	Agent  string `json:"agent,omitzero"`
	Socket string `json:"socket,omitzero"`
}

// Closed is how many sessions or streams a command closed.
type Closed struct {
	Closed int `json:"closed"`
}

// AllowedNode is lodge.allowNode's answer.
type AllowedNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (s *Server) call(ctx context.Context, method string, p jsontext.Value) (any, error) {
	switch method {
	case "lodge.status":
		return s.status(ctx, p)
	case "lodge.apps":
		if err := params(p, &struct{}{}); err != nil {
			return nil, err
		}
		return s.d.Apps.List(), nil
	case "lodge.reload":
		if err := params(p, &struct{}{}); err != nil {
			return nil, err
		}
		if err := s.d.Reload(); err != nil {
			return nil, err
		}
		return s.d.Apps.List(), nil
	case "lodge.logins":
		if err := params(p, &struct{}{}); err != nil {
			return nil, err
		}
		return s.d.Allow.Logins(ctx)
	case "lodge.allowLogin":
		return s.allowLogin(ctx, p)
	case "lodge.disallowLogin":
		return s.disallowLogin(ctx, p)
	case "lodge.nodes":
		if err := params(p, &struct{}{}); err != nil {
			return nil, err
		}
		return s.d.Allow.Nodes(ctx)
	case "lodge.allowNode":
		return s.allowNode(ctx, p)
	case "lodge.disallowNode":
		return s.disallowNode(ctx, p)
	case "lodge.tokens":
		if err := params(p, &struct{}{}); err != nil {
			return nil, err
		}
		return s.d.Tokens.List(ctx)
	case "lodge.issueToken":
		return s.issueToken(ctx, p)
	case "lodge.revokeToken":
		return s.revokeToken(ctx, p)
	case "lodge.sessions":
		if err := params(p, &struct{}{}); err != nil {
			return nil, err
		}
		return s.d.Sessions.List(), nil
	case "lodge.endSession":
		return s.endSession(p)
	case "lodge.usage":
		return s.usage(ctx, p)
	case "lodge.providerKey":
		return s.providerKey(ctx, p)
	case "lodge.providerImport":
		return s.providerImport(ctx, p)
	case "lodge.providerModels":
		return s.providerModels(ctx, p)
	}
	return nil, errorf(CodeMethodNotFound, "no method %q", method)
}

func (s *Server) status(ctx context.Context, p jsontext.Value) (any, error) {
	if err := params(p, &struct{}{}); err != nil {
		return nil, err
	}
	st := Status{Nodes: []Node{}, Apps: []AppStatus{}, Sessions: s.d.Sessions.Count(), Providers: []inference.ProviderStatus{}}
	if s.d.Inference != nil {
		st.Providers, st.InferenceRefused = s.d.Inference.Status()
	}
	if s.d.Nodes != nil {
		st.Nodes = append(st.Nodes, s.d.Nodes(ctx)...)
	}
	if s.d.Agents != nil {
		as := s.d.Agents.Status()
		st.AgentSocket = &as
	}
	for _, a := range s.d.Apps.List() {
		st.Apps = append(st.Apps, AppStatus{ID: a.ID, State: a.State, Reason: a.Reason})
	}
	return st, nil
}

func (s *Server) allowLogin(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Login string `json:"login"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	switch err := s.d.Allow.AllowLogin(ctx, in.Login); {
	case errors.Is(err, allow.ErrInvalid):
		return nil, errorf(CodeInvalidParams, "a login is 1 to 256 bytes of UTF-8 without control characters")
	case errors.Is(err, allow.ErrConflict):
		return nil, errorf(CodeConflict, "login %s is already allowed", in.Login)
	case err != nil:
		return nil, err
	}
	s.d.Log.Info("login allowed", "login", in.Login)
	return struct{}{}, nil
}

func (s *Server) disallowLogin(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Login string `json:"login"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	switch err := s.d.Allow.DisallowLogin(ctx, in.Login); {
	case errors.Is(err, allow.ErrNotFound):
		return nil, errorf(CodeNotFound, "login %s is not allowed", in.Login)
	case err != nil:
		return nil, err
	}
	n := s.d.Sessions.EndLogin(in.Login)
	s.d.Log.Info("login disallowed", "login", in.Login, "sessions closed", n)
	return Closed{n}, nil
}

// resolveNode finds node, a StableNodeID or a name, among the lodge
// node's peers.
func (s *Server) resolveNode(ctx context.Context, node string) (AllowedNode, error) {
	if node == "" {
		return AllowedNode{}, errorf(CodeInvalidParams, "name a node: its ID or its name")
	}
	var peers []peer.Peer
	if s.d.Peers != nil {
		var err error
		if peers, err = s.d.Peers(ctx); err != nil {
			return AllowedNode{}, err
		}
	}
	for _, p := range peers {
		if p.NodeID == node {
			return AllowedNode{ID: p.NodeID, Name: p.NodeName}, nil
		}
	}
	var named []peer.Peer
	for _, p := range peers {
		if p.NodeName == strings.ToLower(node) {
			named = append(named, p)
		}
	}
	switch {
	case len(named) == 1:
		return AllowedNode{ID: named[0].NodeID, Name: named[0].NodeName}, nil
	case len(named) > 1:
		return AllowedNode{}, errorf(CodeInvalidParams, "%d peers are named %s: allow one by its ID", len(named), node)
	case allow.ValidNodeID(node):
		// Not among the peers now (offline, or no tailnet): an ID is still
		// a node to allow.
		return AllowedNode{ID: node}, nil
	}
	return AllowedNode{}, errorf(CodeNotFound, "no peer is named %s", node)
}

func (s *Server) allowNode(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Node string `json:"node"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	n, err := s.resolveNode(ctx, in.Node)
	if err != nil {
		return nil, err
	}
	switch err := s.d.Allow.AllowNode(ctx, n.ID, n.Name); {
	case errors.Is(err, allow.ErrInvalid):
		return nil, errorf(CodeInvalidParams, "%s is not a node ID", n.ID)
	case errors.Is(err, allow.ErrConflict):
		return nil, errorf(CodeConflict, "node %s is already allowed", n.ID)
	case err != nil:
		return nil, err
	}
	s.d.Log.Info("node allowed", "node", n.ID, "name", n.Name)
	return n, nil
}

func (s *Server) disallowNode(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Node string `json:"node"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	id := in.Node
	if nodes, err := s.d.Allow.Nodes(ctx); err == nil {
		var named []string
		for _, n := range nodes {
			if n.ID == in.Node {
				named = []string{n.ID}
				break
			}
			if n.Name != "" && n.Name == strings.ToLower(in.Node) {
				named = append(named, n.ID)
			}
		}
		switch len(named) {
		case 1:
			id = named[0]
		case 0:
		default:
			return nil, errorf(CodeInvalidParams, "%d allowed nodes are named %s: disallow one by its ID", len(named), in.Node)
		}
	}
	switch err := s.d.Allow.DisallowNode(ctx, id); {
	case errors.Is(err, allow.ErrNotFound):
		return nil, errorf(CodeNotFound, "node %s is not allowed", in.Node)
	case err != nil:
		return nil, err
	}
	n := s.d.Sessions.EndNode(id)
	s.d.Log.Info("node disallowed", "node", id, "sessions closed", n)
	return Closed{n}, nil
}

func (s *Server) issueToken(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Name  string `json:"name"`
		App   string `json:"app"`
		Scope string `json:"scope"`
		Agent string `json:"agent,omitzero"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	a, ok := s.d.Apps.Get(in.App)
	if !ok || !a.Enabled() || !a.Manifest.MCP {
		return nil, errorf(CodeNotFound, "no enabled app %q serves MCP", in.App)
	}
	if in.Agent != "" && (s.d.Agents == nil || !s.d.Agents.Listed(in.Agent)) {
		return nil, errorf(CodeInvalidParams, "%s is not an agent: list the user in agents in /etc/lodge/config.json, then lodgectl reload", in.Agent)
	}
	secret, err := s.d.Tokens.Issue(ctx, in.Name, in.App, in.Scope, in.Agent)
	switch {
	case errors.Is(err, tokens.ErrInvalid):
		return nil, errorf(CodeInvalidParams, "%v: a name is 1 to 64 of [A-Za-z0-9@._-], a scope 1 to 256 visible ASCII characters", err)
	case errors.Is(err, tokens.ErrConflict):
		return nil, errorf(CodeConflict, "a live token is named %s", in.Name)
	case err != nil:
		return nil, err
	}
	s.d.Log.Info("token issued", "token", in.Name, "app", in.App, "agent", in.Agent)
	if in.Agent != "" {
		return Issued{Token: secret, App: in.App, URL: "/mcp/" + in.App, Agent: in.Agent, Socket: s.d.Agents.Status().Path}, nil
	}
	url := ""
	if s.d.MCPURL != nil {
		url = s.d.MCPURL(in.App)
	}
	return Issued{Token: secret, App: in.App, URL: url}, nil
}

func (s *Server) revokeToken(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	id, err := s.d.Tokens.Revoke(ctx, in.Name)
	switch {
	case errors.Is(err, tokens.ErrNotFound):
		return nil, errorf(CodeNotFound, "no live token is named %s", in.Name)
	case err != nil:
		return nil, err
	}
	n := 0
	if s.d.EndToken != nil {
		n = s.d.EndToken(id)
	}
	s.d.Log.Info("token revoked", "token", in.Name, "streams closed", n)
	return Closed{n}, nil
}

func (s *Server) endSession(p jsontext.Value) (any, error) {
	var in struct {
		ID string `json:"id"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	if err := s.d.Sessions.End(in.ID); errors.Is(err, sessions.ErrNotFound) {
		return nil, errorf(CodeNotFound, "no session %s", in.ID)
	}
	return Closed{1}, nil
}

// usage sums the inference log per app and alias, since a day (UTC).
func (s *Server) usage(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Since string `json:"since,omitzero"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	var since time.Time
	if in.Since != "" {
		t, err := time.Parse(time.DateOnly, in.Since)
		if err != nil {
			return nil, errorf(CodeInvalidParams, "since must be a day, YYYY-MM-DD")
		}
		since = t
	}
	if s.d.Inference == nil {
		return []inference.Sum{}, nil
	}
	return s.d.Inference.Usage(ctx, since)
}

// validKey reports whether a provider key is 1 to 4096 visible ASCII
// characters, without spaces.
func validKey(k string) bool {
	if k == "" || len(k) > 4096 {
		return false
	}
	for i := range len(k) {
		if k[i] < '!' || k[i] > '~' {
			return false
		}
	}
	return true
}

// providerKey sets or, given an empty key, removes a provider's key.
func (s *Server) providerKey(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Provider string `json:"provider"`
		Key      string `json:"key"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	if in.Key != "" && !validKey(in.Key) {
		return nil, errorf(CodeInvalidParams, "a key is 1 to 4096 visible ASCII characters")
	}
	if s.d.Inference == nil {
		return nil, errorf(CodeNotFound, "no provider %q", in.Provider)
	}
	switch err := s.d.Inference.SetKey(ctx, in.Provider, in.Key); {
	case errors.Is(err, inference.ErrNoProvider):
		return nil, errorf(CodeNotFound, "no provider %q in lodge's configuration", in.Provider)
	case err != nil:
		return nil, err
	}
	s.d.Log.Info("provider key changed", "provider", in.Provider, "removed", in.Key == "")
	return struct {
		Removed bool `json:"removed"`
	}{in.Key == ""}, nil
}

// providerImport replaces a codex provider's sign-in with a login file's
// (docs/specs/inference.md, The codex provider).
func (s *Server) providerImport(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Provider string          `json:"provider"`
		Login    codexauth.Login `json:"login"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	if s.d.Inference == nil {
		return nil, errorf(CodeNotFound, "no provider %q", in.Provider)
	}
	switch err := s.d.Inference.ImportLogin(ctx, in.Provider, in.Login); {
	case errors.Is(err, inference.ErrNoProvider):
		return nil, errorf(CodeNotFound, "no provider %q in lodge's configuration", in.Provider)
	case errors.Is(err, inference.ErrNotCodex):
		return nil, errorf(CodeInvalidParams, "provider %s is not of type codex", in.Provider)
	case err != nil:
		return nil, errorf(CodeInvalidParams, "%v", err)
	}
	s.d.Log.Info("provider sign-in imported", "provider", in.Provider, "client", in.Login.ClientID)
	return struct {
		Account  string `json:"account"`
		ClientID string `json:"client_id"`
	}{cmpOr(in.Login.Email, in.Login.Account), in.Login.ClientID}, nil
}

// providerModels lists a provider's models.
func (s *Server) providerModels(ctx context.Context, p jsontext.Value) (any, error) {
	var in struct {
		Provider string `json:"provider"`
	}
	if err := params(p, &in); err != nil {
		return nil, err
	}
	if s.d.Inference == nil {
		return nil, errorf(CodeNotFound, "no provider %q", in.Provider)
	}
	list, err := s.d.Inference.Models(ctx, in.Provider)
	switch {
	case errors.Is(err, inference.ErrNoProvider):
		return nil, errorf(CodeNotFound, "no provider %q in lodge's configuration", in.Provider)
	case err != nil:
		return nil, errorf(CodeConflict, "%v", err)
	}
	return list, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
