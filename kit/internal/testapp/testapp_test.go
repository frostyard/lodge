package testapp_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/frostyard/lodge/kit/internal/testapp"
	"github.com/frostyard/lodge/kit/jsonrpc"
	"github.com/frostyard/lodge/kit/kittest"
)

func TestSocketRules(t *testing.T) {
	l := kittest.Start(t, testapp.Manifest.ID, testapp.New())
	kittest.CheckRules(t, l, testapp.Manifest)
}

func TestAPIRoundTrip(t *testing.T) {
	l := kittest.Start(t, testapp.Manifest.ID, testapp.New())
	nc, err := l.DialAPI(t.Context(), kittest.Session())
	if err != nil {
		t.Fatal(err)
	}
	c := jsonrpc.NewClient[testapp.Code](nc)
	defer c.Close()
	if err := c.Call(t.Context(), "echo", map[string]string{"text": "early"}, nil); !errors.Is(err, &jsonrpc.Error[testapp.Code]{Code: jsonrpc.CodeNotReady}) {
		t.Errorf("echo before hello = %v, want notReady", err)
	}
	if err := c.Call(t.Context(), "rpc.hello", nil, nil); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := c.Call(t.Context(), "echo", map[string]string{"text": "hi"}, &got); err != nil {
		t.Fatal(err)
	}
	if got["text"] != "hi" || got["node"] != kittest.NodeName || got["session"] != kittest.SessionID {
		t.Errorf("echo = %v, want hi from node e2e in the session", got)
	}
}

func TestRoutes(t *testing.T) {
	l := kittest.Start(t, testapp.Manifest.ID, testapp.New())
	for _, tc := range []struct {
		method, path string
		code         int
		body         string
	}{
		{http.MethodGet, "/content/blob", 200, "untrusted"},
		{http.MethodHead, "/content/blob", 200, ""},
		{http.MethodPost, "/content/blob", 405, "POST is not allowed here\n"},
		{http.MethodPut, "/device/pair", 200, "PUT /device/pair from e2e"},
		{http.MethodGet, "/content/../mcp", 400, "the path is not clean\n"},
	} {
		c := kittest.Content()
		if strings.HasPrefix(tc.path, "/device/") {
			c = kittest.Device()
		}
		code, body := l.Request(t, tc.method, tc.path, c)
		if code != tc.code || body != tc.body {
			t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.path, code, body, tc.code, tc.body)
		}
	}
}
