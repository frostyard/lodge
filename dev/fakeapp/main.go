// Command lodge-fakeapp is a stand-in app for lodge's development build:
// it installs a manifest and a small web bundle under LODGE_ROOT, and
// serves the app contract on its socket with the kit's lodgeapp.Server.
// Its page speaks the shell's protocol by hand (docs/specs/lodge-client.md),
// so the shell can be tried with two of them before any real app is
// installed. It is not shipped.
//
//	LODGE_ROOT=$ROOT lodge-fakeapp --id one --name One --key o
package main

import (
	"bufio"
	"bytes"
	"context"
	"embed"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/frostyard/lodge/kit/lodgeapp"
	"github.com/frostyard/lodge/kit/manifest"
)

//go:embed web
var web embed.FS

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lodge-fakeapp:", err)
		os.Exit(1)
	}
}

func run() error {
	m := manifest.Manifest{Contract: manifest.Contract, Icon: "icon.svg", Version: "0.0.0-dev"}
	flag.StringVar(&m.ID, "id", "fake", "the app's id")
	flag.StringVar(&m.Name, "name", "Fake", "the app's name")
	flag.StringVar(&m.Key, "key", "", "the switcher key")
	flag.BoolVar(&m.Content, "content", true, "serve /content/")
	flag.BoolVar(&m.ContentLinks, "content-links", false, "set contentLinks")
	flag.BoolVar(&m.MCP, "mcp", true, "serve /mcp")
	flag.BoolVar(&m.Device, "device", true, "serve /device/")
	installOnly := flag.Bool("install", false, "install the manifest and bundle, then exit")
	flag.Parse()
	m.User = "fake" + m.ID
	if err := m.Validate(); err != nil {
		return err
	}
	env, err := lodgeapp.LoadEnv()
	if err != nil {
		return err
	}
	if env.Root == "" {
		return errors.New("set LODGE_ROOT: the fake app is for the development build only")
	}
	if err := install(env, m); err != nil {
		return err
	}
	if *installOnly {
		return nil
	}
	ln, err := env.Listen(m.ID)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil)).With("app", m.ID)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("serving", "socket", env.SocketPath(m.ID))
	return server(m, log).Serve(ctx, ln)
}

// install writes app.json and the bundle under the root's apps directory.
func install(env lodgeapp.Env, m manifest.Manifest) error {
	dir := env.Path(filepath.Join("/usr/share/lodge/apps", m.ID))
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "web"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "app.json"), b, 0o644); err != nil {
		return err
	}
	return fs.WalkDir(web, "web", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := web.ReadFile(p)
		if err != nil {
			return err
		}
		// The bundle's paths are absolute (base-uri is 'none').
		data = bytes.ReplaceAll(data, []byte("{{ID}}"), []byte(m.ID))
		return os.WriteFile(filepath.Join(dir, p), data, 0o644)
	})
}

func whoami(w http.ResponseWriter, r *http.Request) {
	c, _ := lodgeapp.CallerFrom(r.Context())
	w.Header().Set("Content-Type", "application/json")
	_ = json.MarshalWrite(w, map[string]any{"kind": c.Kind, "name": c.Name(), "login": c.Login, "displayName": c.DisplayName,
		"node": c.NodeName, "token": c.Token, "scope": c.Scope, "path": r.URL.Path})
}

func server(m manifest.Manifest, log *slog.Logger) *lodgeapp.Server {
	return &lodgeapp.Server{
		Health: func(context.Context) lodgeapp.Health {
			return lodgeapp.Health{OK: true, Version: m.Version, Protocol: 1}
		},
		// API answers every JSON-RPC request with who called and what.
		API: func(ctx context.Context, nc net.Conn, c lodgeapp.Caller) {
			log.Info("session", "login", c.Login, "node", c.NodeName, "session", c.Session)
			sc := bufio.NewScanner(nc)
			sc.Buffer(make([]byte, 64<<10), 16<<20)
			for sc.Scan() {
				var req struct {
					ID     jsontext.Value `json:"id"`
					Method string         `json:"method"`
				}
				if json.Unmarshal(sc.Bytes(), &req) != nil || len(req.ID) == 0 {
					continue
				}
				resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID,
					"result": map[string]any{"app": m.ID, "method": req.Method, "caller": c.Name(), "login": c.Login}})
				if _, err := nc.Write(append(resp, '\n')); err != nil {
					return
				}
			}
		},
		X: http.HandlerFunc(whoami),
		Content: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<!doctype html><title>content</title><body style="font:14px sans-serif">`+
				`<p>Untrusted content from %s, at %s.</p><p><a href="https://example.com/" target="_blank">A link</a></p>`+
				`<script>document.body.append("script ran: the sandbox is broken")</script>`, m.ID, r.URL.Path)
		}),
		MCP:    http.HandlerFunc(whoami),
		Device: http.HandlerFunc(whoami),
		Logger: log,
	}
}
