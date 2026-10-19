// Package testapp is the smallest app on the kit: a line-delimited
// JSON-RPC echo on /api, a blob on /content/, an MCP stand-in behind
// mcpsession and a device echo. Its tests run the contract's socket rules
// against kittest.
package testapp

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/frostyard/lodge/kit/lodgeapp"
	"github.com/frostyard/lodge/kit/manifest"
	"github.com/frostyard/lodge/kit/mcpsession"
)

// Manifest is the test app's app.json.
var Manifest = manifest.Manifest{
	Contract: manifest.Contract, ID: "test", Name: "Test", Icon: "icon.svg",
	Version: "0.0.1", User: "lodgetest", Key: "t",
	Content: true, Device: true, MCP: true,
}

// New returns the test app's server.
func New() *lodgeapp.Server {
	return &lodgeapp.Server{
		Health: func(context.Context) lodgeapp.Health {
			return lodgeapp.Health{OK: true, Version: Manifest.Version, Protocol: 1}
		},
		API: serveRPC,
		Content: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/content/blob" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("untrusted"))
		}),
		Device: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, _ := lodgeapp.CallerFrom(r.Context())
			_, _ = fmt.Fprintf(w, "%s %s from %s", r.Method, r.URL.Path, c.Name())
		}),
		MCP: mcpsession.New(&fakeMCP{}, mcpsession.Options{}),
	}
}

type frame struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitzero"`
	Method  string         `json:"method,omitzero"`
	Params  jsontext.Value `json:"params,omitzero"`
	Result  any            `json:"result,omitzero"`
}

// serveRPC answers every request with its method, params and the
// caller's node name, one line per message, as an app's own server would.
func serveRPC(ctx context.Context, nc net.Conn, c lodgeapp.Caller) {
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	sc := bufio.NewScanner(nc)
	sc.Buffer(make([]byte, 64<<10), lodgeapp.MaxMessage+1)
	for sc.Scan() {
		var m frame
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			return
		}
		out, err := json.Marshal(frame{JSONRPC: "2.0", ID: m.ID, Result: map[string]any{
			"method": m.Method, "params": m.Params, "node": c.NodeName, "session": c.Session}})
		if err != nil {
			return
		}
		if _, err := nc.Write(append(out, '\n')); err != nil {
			return
		}
	}
}

// fakeMCP stands in for an MCP SDK handler: a POST without a session opens
// one, and DELETE ends it.
type fakeMCP struct {
	next atomic.Int64
}

func (f *fakeMCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get(mcpsession.Header)
	switch {
	case r.Method == http.MethodPost && id == "":
		id = fmt.Sprintf("s%d", f.next.Add(1))
		w.Header().Set(mcpsession.Header, id)
		c, _ := lodgeapp.CallerFrom(r.Context())
		_, _ = fmt.Fprintf(w, "session %s for %s", id, c.Token)
	case id == "":
		http.Error(w, "no session", http.StatusBadRequest)
	case r.Method == http.MethodDelete:
		w.WriteHeader(http.StatusNoContent)
	default:
		_, _ = fmt.Fprintf(w, "%s in %s", strings.ToLower(r.Method), id)
	}
}
