// Package testapp is the smallest app on the kit: the kit's JSON-RPC
// server on /api, a blob on /content/, an MCP stand-in behind mcpsession
// and a device echo. Its tests run the contract's socket rules
// against kittest.
package testapp

import (
	"context"
	"encoding/json/jsontext"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/frostyard/lodge/kit/jsonrpc"
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
	api, router := rpc()
	return &lodgeapp.Server{
		Health: func(context.Context) lodgeapp.Health {
			return lodgeapp.Health{OK: true, Version: Manifest.Version, Protocol: 1}
		},
		API: func(ctx context.Context, nc net.Conn, _ lodgeapp.Caller) { api.ServeConn(ctx, nc, router) },
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

// Code is the test app's error-code type.
type Code int

// rpc is the test app's API, on the kit's JSON-RPC server: rpc.hello, and
// echo, which answers its text with the caller's node and session.
func rpc() (*jsonrpc.Server[Code], *jsonrpc.Router[lodgeapp.Kind, Code]) {
	r := jsonrpc.NewRouter[lodgeapp.Kind, Code](jsonrpc.RouterOptions[lodgeapp.Kind]{
		Caller: func(ctx context.Context) lodgeapp.Kind {
			c, _ := lodgeapp.CallerFrom(ctx)
			return c.Kind
		},
	})
	session := []lodgeapp.Kind{lodgeapp.Session}
	r.Handle("rpc.hello", jsonrpc.Route[lodgeapp.Kind]{Callers: session, Handler: func(ctx context.Context, _ jsontext.Value) (any, error) {
		jsonrpc.ConnFrom(ctx).SetReady("test")
		return nil, nil
	}})
	r.Handle("echo", jsonrpc.Route[lodgeapp.Kind]{Callers: session, Handler: func(ctx context.Context, raw jsontext.Value) (any, error) {
		var p struct {
			Text string `json:"text"`
		}
		if err := jsonrpc.DecodeParams[Code](raw, &p, []string{"text"}); err != nil {
			return nil, err
		}
		c, _ := lodgeapp.CallerFrom(ctx)
		return map[string]string{"text": p.Text, "node": c.NodeName, "session": c.Session}, nil
	}})
	return jsonrpc.NewServer[Code](jsonrpc.ServerOptions{Hello: "rpc.hello"}), r
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
