// Package hearthtest is hearth's example app, for lodge's tests and its
// development build: 1 note service on hearth, with the operations, grants and
// Connect API of an app on hearth (docs/specs/app-contract.md, Apps on hearth).
//
// It is a copy of frostyard/hearth's examples/notes-lite, which is its own
// module and so cannot be imported; internal/hearthtest/notelite says what was
// changed.
package hearthtest

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/frostyard/hearth/authz"
	"github.com/frostyard/hearth/connectserver"
	hearthlodge "github.com/frostyard/hearth/lodge"

	"github.com/frostyard/lodge/internal/hearthtest/notelite"
	"github.com/frostyard/lodge/internal/hearthtest/notelite/notesv1"
	"github.com/frostyard/lodge/internal/hearthtest/notelite/notesv1/notesv1connect"
)

// ID is the app's ID, which its manifest names.
const ID = "notelite"

// Version is the app's version, which its manifest names.
const Version = "0.1.0"

// Catalog is the app's tool catalog, in the shape of an MCP tools/list result:
// 1 tool for each of its agent operations, with the description and the
// schemas that hearth generates (docs/specs/app-contract.md, Apps on hearth).
func Catalog() ([]byte, error) {
	type tool struct {
		Name         string         `json:"name"`
		Description  string         `json:"description"`
		InputSchema  jsontext.Value `json:"inputSchema"`
		OutputSchema jsontext.Value `json:"outputSchema,omitzero"`
	}
	tools := notesv1.NoteServiceTools(nil)
	out := struct {
		Tools []tool `json:"tools"`
	}{Tools: make([]tool, 0, len(tools))}
	for _, t := range tools {
		out.Tools = append(out.Tools, tool{Name: t.Name, Description: t.Description,
			InputSchema: jsontext.Value(t.InputSchema), OutputSchema: jsontext.Value(t.OutputSchema)})
	}
	return json.Marshal(out, json.Deterministic(true))
}

// manifest is app id's app.json: an app on hearth, with its 2 files beside it.
func manifest(user string) ([]byte, error) {
	return json.Marshal(struct {
		Contract int    `json:"contract"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Icon     string `json:"icon"`
		Version  string `json:"version"`
		User     string `json:"user"`
		Hearth   string `json:"hearth"`
		Tools    string `json:"tools"`
	}{1, ID, "Notes Lite", "icon.svg", Version, user, "manifest.json", "tools.json"})
}

// Install writes the app into the layout under root, as lodge's installer would:
// its app.json, its hearth manifest, its tool catalog and its web bundle.
func Install(root, user string) error {
	app, err := manifest(user)
	if err != nil {
		return err
	}
	catalog, err := Catalog()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, "usr/share/lodge/apps", ID)
	files := map[string][]byte{
		"app.json":      app,
		"manifest.json": notelite.Manifest,
		"tools.json":    catalog,
		"web/index.html": []byte("<!doctype html><title>" + ID + "</title><p>" + ID +
			" frame</p>"),
		"web/icon.svg": []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24"><title>Notes Lite</title></svg>`),
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// App is the running app: its server, its database, and the caller of its last
// request.
type App struct {
	// Server is the app's lodge socket's server.
	Server *http.Server

	db *sql.DB
	mu sync.Mutex
	// kind, principal, grant and path are Lodge-Caller, Lodge-Principal,
	// Lodge-Grant and the path of the last request that hearth's adapter
	// admitted.
	kind, principal, grant, path string
	// hold makes each RPC wait, and the channels report it: started when an
	// RPC is waiting, ended when its request's context ended first.
	hold           <-chan struct{}
	started, ended chan struct{}
}

// LastCaller is the principal and the grant that the app last saw, as lodge
// sent them: Lodge-Principal, and Lodge-Grant's base64url text.
func (a *App) LastCaller() (principal, grant string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.principal, a.grant
}

// LastKind is the caller kind of the app's last request: Lodge-Caller. hearth's
// adapter admits only principal, so a request of another kind is refused before
// it is recorded.
func (a *App) LastKind() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.kind
}

// LastPath is the path of the app's last request, such as
// /connect/example.notes.v1.NoteService/ListNotes.
func (a *App) LastPath() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.path
}

// Hold makes each RPC wait until hold is closed, or until its request's
// context ends, and returns 2 channels: started reports each RPC that waits,
// and ended reports each RPC whose context ended first. It is for a test that
// revokes or expires a call in flight.
func (a *App) Hold(hold <-chan struct{}) (started, ended <-chan struct{}) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hold, a.started, a.ended = hold, make(chan struct{}, 4), make(chan struct{}, 4)
	return a.started, a.ended
}

// wait is 1 RPC's wait, when a test holds the app: it returns an error when the
// request's context ended first.
func (a *App) wait(ctx context.Context) error {
	a.mu.Lock()
	hold, started, ended := a.hold, a.started, a.ended
	a.mu.Unlock()
	if hold == nil {
		return nil
	}
	select {
	case started <- struct{}{}:
	default:
	}
	select {
	case <-hold:
		return nil
	case <-ctx.Done():
		select {
		case ended <- struct{}{}:
		default:
		}
		return ctx.Err()
	}
}

// Close stops the app's server and closes its database.
func (a *App) Close() error {
	err := a.Server.Close()
	if cerr := a.db.Close(); err == nil {
		err = cerr
	}
	return err
}

// Serve opens the app's database at dbPath and serves it on l, the app's lodge
// socket, for the connections of lodgeUID alone. It returns once the app is
// serving: GET /health outside hearth's adapter, and the Connect API under
// /connect/.
func Serve(ctx context.Context, l net.Listener, lodgeUID int, dbPath string) (*App, error) {
	db, err := notelite.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	app := &App{db: db}
	api := (&notelite.Service{DB: db, Notify: func() {}}).API()
	// Connect decodes the body before the unary interceptor runs, so a held
	// RPC still sees its context end when lodge ends the call. HandlerOptions
	// puts hearth's error mapping first, the outermost, and it keeps the
	// *connect.Error that connectFailure returns here as it is.
	interceptor := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if err := app.wait(ctx); err != nil {
				return nil, connect.NewError(connect.CodeCanceled, err)
			}
			res, err := next(ctx, req)
			if err != nil {
				return nil, connectFailure(err)
			}
			return res, nil
		}
	})
	path, handler := notesv1connect.NewNoteServiceHandler(api,
		connectserver.HandlerOptions(connect.WithInterceptors(interceptor))...)
	inner := http.NewServeMux()
	inner.Handle(path, handler)
	root := http.NewServeMux()
	root.HandleFunc("/health", health)
	// The adapter trusts Lodge-Principal and Lodge-Grant on this socket only,
	// after its peer check, so the app records them inside it (ADR-0024).
	root.Handle(connectserver.LodgePrefix, connectserver.Lodge("/", app.record(inner)))
	app.Server = &http.Server{Handler: root, ConnContext: hearthlodge.ConnContext,
		ReadHeaderTimeout: 10 * time.Second}
	go func() {
		// Serve returns after Close, which a caller asks for.
		_ = app.Server.Serve(hearthlodge.Listener(l, lodgeUID))
	}()
	return app, nil
}

// health answers the app's state, as every app does, outside the adapter.
func health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"version":"` + Version + `","protocol":1}`))
}

// record keeps the caller of the last request that hearth's adapter admitted:
// a request it refused never reaches here.
func (a *App) record(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := authz.CallerFrom(r.Context()); ok {
			a.mu.Lock()
			a.kind = r.Header.Get("Lodge-Caller")
			a.principal, a.grant = r.Header.Get(hearthlodge.PrincipalHeader), r.Header.Get(hearthlodge.GrantHeader)
			// The adapter strips /connect before it reaches this handler.
			a.path = "/connect" + r.URL.Path
			a.mu.Unlock()
		}
		next.ServeHTTP(w, r)
	})
}

// connectFailure is err as a Connect error: a refused grant is permission
// denied with the app's own text, a missing row is not found, and anything else
// is the app's own failure.
func connectFailure(err error) *connect.Error {
	var denied *authz.DeniedError
	switch {
	case errors.As(err, &denied):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, sql.ErrNoRows):
		return connect.NewError(connect.CodeNotFound, err)
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("the app failed: %w", err))
}
