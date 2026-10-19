package lodgeapp

import (
	"context"
	"net"
	"net/http"
	"time"
)

// AgentSocket is lodge's agent socket, where an agent on lodge's host
// reaches other apps' /mcp/<id> (app-contract.md, The agent socket).
const AgentSocket = "/run/lodge/agent.sock"

// AgentURL is app id's MCP endpoint on the agent socket, for AgentClient.
// The host is a placeholder: the client dials the socket whatever it is.
func AgentURL(id string) string { return "http://lodge/mcp/" + id }

// AgentClient returns an HTTP client of lodge's agent socket under e, which
// sends token as its bearer on every request. lodge admits the connection
// only from a user its configuration names as an agent, and the token only
// when it was issued for that user (lodgectl token issue … --agent USER).
// An agent uses one client per app it calls, at AgentURL(id), as the MCP
// client's HTTP client. It needs no Tailscale.
func (e Env) AgentClient(token string) *http.Client {
	path := e.Path(AgentSocket)
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}
	return &http.Client{Transport: bearer{token: token, next: tr}}
}

// bearer sets Authorization on each request, on a copy.
type bearer struct {
	token string
	next  http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}
