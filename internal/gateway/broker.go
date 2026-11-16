package gateway

import (
	"context"
	"net"
	"net/http"

	"github.com/coder/websocket"

	"github.com/frostyard/lodge/internal/peer"
)

// Broker is the event broker's door for clients, which lodge serves on
// /nats (internal/broker; docs/specs/lodge-http.md, The broker). A nil
// Broker is a broker that is off.
type Broker interface {
	// ServeClient moves the connection to the broker until either side
	// ends.
	ServeClient(ctx context.Context, c net.Conn)
}

// MaxBrokerMessage is the largest WebSocket message /nats takes, which is
// the broker's own payload cap.
const MaxBrokerMessage = 64 << 10

// nats serves /nats, the broker's WebSocket door on the lodge node
// (docs/specs/lodge-http.md, The broker; ADR-0022).
func (g *Gateway) nats(w http.ResponseWriter, r *http.Request, who peer.WhoIs) {
	// 1. The broker.
	if g.cfg.Broker == nil {
		fail(w, http.StatusNotFound, "not found", agentHeaders)
		return
	}
	// 2. Host, on the tailnet.
	if !g.Names().lodgeHost(r.Host) {
		fail(w, http.StatusForbidden, "this is not lodge's address", agentHeaders)
		return
	}
	// 3. No Origin: no browser calls it.
	if _, ok := r.Header["Origin"]; ok {
		fail(w, http.StatusForbidden, "browsers may not call /nats", agentHeaders)
		return
	}
	// 4. Method.
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		fail(w, http.StatusMethodNotAllowed, "method "+r.Method+" is not allowed here", agentHeaders)
		return
	}
	// 5. A WebSocket, and nothing else.
	if !isWebSocket(r) {
		fail(w, http.StatusBadRequest, "/nats takes a WebSocket", agentHeaders)
		return
	}
	// 6. Peer, on the tailnet: checks a and b. The token in the CONNECT
	// line names the client, in the broker.
	p, d := g.peerChecks(r, who)
	if d != nil {
		fail(w, d.status, d.line, agentHeaders)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		g.log.Warn("the broker's WebSocket did not upgrade", "node", p.NodeName, "err", err)
		return
	}
	g.log.Info("broker client", "node", p.NodeName, "login", p.Login)
	c.SetReadLimit(MaxBrokerMessage)
	nc := websocket.NetConn(r.Context(), c, websocket.MessageBinary)
	defer nc.Close()
	g.cfg.Broker.ServeClient(r.Context(), nc)
}
