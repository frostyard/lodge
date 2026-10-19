package testapp_test

import (
	"bufio"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/frostyard/lodge/kit/internal/testapp"
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
	defer nc.Close()
	// One frame without its newline, as the contract allows, then one with.
	for _, msg := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"rpc.hello","params":{}}` + "\n",
		`{"jsonrpc":"2.0","id":2,"method":"rpc.ping"}` + "\n",
	} {
		if _, err := io.WriteString(nc, msg); err != nil {
			t.Fatal(err)
		}
	}
	r := bufio.NewReader(nc)
	for _, want := range []string{`"method":"rpc.hello"`, `"method":"rpc.ping"`} {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(line, want) || !strings.Contains(line, `"node":"e2e"`) ||
			!strings.Contains(line, `"session":"`+kittest.SessionID+`"`) {
			t.Errorf("reply %q, want %s from node e2e in the session", line, want)
		}
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
