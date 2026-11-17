package hearthtest_test

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/frostyard/hearth/authz"

	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/hearthtest"
	"github.com/frostyard/lodge/internal/hearthtest/notelite/notesv1"
	"github.com/frostyard/lodge/internal/mcpgw"
	"github.com/frostyard/lodge/internal/oauth"
)

// start installs the app under root, loads it with lodge's registry, and serves
// it on the app's lodge socket.
func start(t *testing.T, root string) (*hearthtest.App, *apps.Registry) {
	t.Helper()
	if err := hearthtest.Install(root, "appnotelite"); err != nil {
		t.Fatal(err)
	}
	r := apps.New(root, nil)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	a, ok := r.Get(hearthtest.ID)
	if !ok || !a.Enabled() {
		t.Fatalf("the app is not enabled: %+v", a)
	}
	sock := filepath.Join(root, "run/lodge/apps", hearthtest.ID+".sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	app, err := hearthtest.Serve(t.Context(), l, os.Getuid(), filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Errorf("close the app: %v", err)
		}
	})
	return app, r
}

// TestManifestAndCatalog checks that lodge loads the copy's app.json, its
// hearth manifest and its tool catalog: 4 operations, each with its tool.
func TestManifestAndCatalog(t *testing.T) {
	root := t.TempDir()
	if err := hearthtest.Install(root, "appnotelite"); err != nil {
		t.Fatal(err)
	}
	r := apps.New(root, nil)
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	a, ok := r.Get(hearthtest.ID)
	if !ok || !a.Enabled() {
		t.Fatalf("the app is not enabled: %+v", a)
	}
	if a.Hearth == nil || len(a.Hearth.Operations) != 4 || len(a.Hearth.Tools) != 4 {
		t.Fatalf("the app's hearth manifest = %+v", a.Hearth)
	}
	var agent int
	for _, op := range a.Hearth.Operations {
		if op.Agent() && op.Tool != "" {
			agent++
		}
	}
	if agent != 4 {
		t.Errorf("%d operations are agents', want 4", agent)
	}
	catalog, err := hearthtest.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"create_note", "delete_note", "get_note", "list_notes"} {
		if !strings.Contains(string(catalog), `"name":"`+name+`"`) {
			t.Errorf("the catalog has no tool %q: %s", name, catalog)
		}
	}
}

// TestConnect checks the app's Connect API on its lodge socket: an allowed call
// answers, and the app records the caller that lodge sent (ADR-0024,
// docs/specs/app-contract.md, Apps on hearth).
func TestConnect(t *testing.T) {
	root := t.TempDir()
	app, r := start(t, root)
	client := connect.NewClient[notesv1.ListNotesRequest, notesv1.ListNotesResponse](
		&http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", r.SocketPath(hearthtest.ID))
		}}},
		"http://"+hearthtest.ID+"/connect/example.notes.v1.NoteService/ListNotes")
	call := func(grant string) (*connect.Response[notesv1.ListNotesResponse], error) {
		req := connect.NewRequest(&notesv1.ListNotesRequest{Folder: "/Work"})
		req.Header().Set("Lodge-Caller", "principal")
		req.Header().Set("Lodge-Principal", "chani")
		req.Header().Set("Lodge-Grant", grant)
		return client.CallUnary(t.Context(), req)
	}
	grant, err := base64.RawURLEncoding.DecodeString("W3siY2FwIjoibm90ZWxpdGU6cmVhZCJ9XQ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(base64.RawURLEncoding.EncodeToString(grant)); err != nil {
		t.Fatalf("an allowed call: %v", err)
	}
	principal, sent := app.LastCaller()
	if principal != "chani" || sent != base64.RawURLEncoding.EncodeToString(grant) {
		t.Errorf("the app's last caller = %q, %q", principal, sent)
	}
	// A grant the operation's capability is not in is refused with the app's
	// own text.
	_, err = call(base64.RawURLEncoding.EncodeToString([]byte(`[{"cap":"notelite:write","sel":{"folder":"/Home"}}]`)))
	var ce *connect.Error
	if err == nil || !asConnectError(err, &ce) || ce.Code() != connect.CodePermissionDenied ||
		!strings.Contains(ce.Message(), "permission denied") {
		t.Errorf("a refused call = %v", err)
	}
}

// TestConnectRefusesOtherCallers checks that the app's Connect API admits the
// caller kind principal only: a call without Lodge-Caller, or with another
// kind, is 403 with 1 line, and the app records no caller (docs/specs/app-contract.md,
// hearth's docs/specs/connect.md).
func TestConnectRefusesOtherCallers(t *testing.T) {
	root := t.TempDir()
	app, r := start(t, root)
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", r.SocketPath(hearthtest.ID))
	}}}
	grant := base64.RawURLEncoding.EncodeToString([]byte(`[{"cap":"notelite:read"}]`))
	for _, kind := range []string{"", "session", "agent", "device", "content"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			"http://"+hearthtest.ID+"/connect/example.notes.v1.NoteService/ListNotes", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if kind != "" {
			req.Header.Set("Lodge-Caller", kind)
		}
		req.Header.Set("Lodge-Principal", "chani")
		req.Header.Set("Lodge-Grant", grant)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusForbidden || !strings.HasPrefix(string(body), "forbidden: ") ||
			strings.Count(string(body), "\n") != 1 {
			t.Errorf("a call with the kind %q = %d %q, want 403 and 1 line that starts forbidden: ", kind, resp.StatusCode, body)
		}
	}
	if kind, path := app.LastKind(), app.LastPath(); kind != "" || path != "" {
		t.Errorf("the app recorded the kind %q on %q, want no caller", kind, path)
	}
}

// asConnectError reports whether err is a Connect error, and sets target to it.
func asConnectError(err error, target **connect.Error) bool {
	ce, ok := err.(*connect.Error)
	if ok {
		*target = ce
	}
	return ok
}

// TestGrantHeaderRoundTrip checks that lodge's Lodge-Grant header is a grant
// that hearth's authz reads: the items as sent, and each item's paths
// (ADR-0024, docs/specs/app-contract.md, Apps on hearth).
func TestGrantHeaderRoundTrip(t *testing.T) {
	items := []oauth.Item{
		{Cap: "notelite:read", Sel: map[string]string{"folder": "/Work"}},
		{Cap: "notelite:write"},
	}
	header, err := mcpgw.GrantHeader(items)
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(header) {
		t.Errorf("the header %q is not base64url without padding", header)
	}
	data, err := base64.RawURLEncoding.Strict().DecodeString(header)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := authz.ParseGrant(data)
	if err != nil {
		t.Fatalf("hearth's authz reads the grant: %v", err)
	}
	// An item holds its capability on its path, and the app's own check is the
	// one that holds.
	if !grant.Allows("notelite:read", "folder", "/Work") {
		t.Error("the grant does not hold notelite:read on /Work")
	}
	if grant.Allows("notelite:read", "folder", "/Home") {
		t.Error("the grant holds notelite:read on /Home, which no item names")
	}
	// An item without sel holds the root path.
	if !grant.Allows("notelite:write", "folder", "/Anywhere") {
		t.Error("the grant does not hold notelite:write on the root path")
	}
	// A capability no item names is refused.
	if grant.Allows("notelite:delete", "folder", "/") {
		t.Error("the grant holds a capability that no item names")
	}
	// Every capability of the app on the root path, as a frame's call sends.
	all := []oauth.Item{{Cap: "notelite:read"}, {Cap: "notelite:write"}}
	header, err = mcpgw.GrantHeader(all)
	if err != nil {
		t.Fatal(err)
	}
	if data, err = base64.RawURLEncoding.DecodeString(header); err != nil {
		t.Fatal(err)
	}
	if grant, err = authz.ParseGrant(data); err != nil {
		t.Fatal(err)
	}
	for _, cap := range []string{"notelite:read", "notelite:write"} {
		if err := grant.Check(cap, authz.Sel{Selector: "folder", Path: "/Reading/Inbox"}); err != nil {
			t.Errorf("the grant of every capability: %v", err)
		}
	}
}
