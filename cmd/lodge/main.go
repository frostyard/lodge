// Command lodge is the tailnet web shell for frostyard's first-party apps
// (docs/design/overview.md). It joins the tailnet as two nodes, lodge and
// lodge-content, serves the shell and each app's files, and proxies to
// the apps' sockets (docs/specs/lodge-http.md). lodgectl manages it over
// its control socket.
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/frostyard/lodge/internal/allow"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/control"
	"github.com/frostyard/lodge/internal/gateway"
	"github.com/frostyard/lodge/internal/inference"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/store"
	"github.com/frostyard/lodge/internal/tailnet"
	"github.com/frostyard/lodge/internal/tokens"
)

// Set by the Makefile's -ldflags.
var (
	version = "dev"
	commit  = "none"
)

// options are lodge's flags.
type options struct {
	node, contentNode     string
	listen, contentListen string
	root                  string
	config                string
	logLevel              string
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "lodge:", err)
		os.Exit(1)
	}
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "lodge %s (%s)\n", version, commit)
}

func parseFlags(args []string, stderr io.Writer) (options, bool, error) {
	var o options
	fs := flag.NewFlagSet("lodge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.node, "node", "lodge", "the lodge node's tailnet name; empty joins no tailnet")
	fs.StringVar(&o.contentNode, "content-node", "lodge-content", "the content node's tailnet name")
	fs.StringVar(&o.listen, "listen", "", "plain HTTP for the lodge node's routes, on a loopback address")
	fs.StringVar(&o.contentListen, "content-listen", "", "plain HTTP for the content routes, on a loopback address")
	fs.StringVar(&o.root, "root", "", "move every path of the install layout under this directory")
	fs.StringVar(&o.config, "config", "/etc/lodge/config.json", "lodge's configuration (moved under --root)")
	fs.StringVar(&o.logLevel, "log-level", "info", "debug, info, warn or error")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return o, false, err
	}
	if fs.NArg() != 0 {
		return o, false, fmt.Errorf("unexpected arguments: %q", fs.Args())
	}
	if *showVersion {
		return o, true, nil
	}
	for _, a := range []string{o.listen, o.contentListen} {
		if a != "" && !loopback(a) {
			return o, false, fmt.Errorf("--listen and --content-listen take a loopback address (127.0.0.0/8 or [::1]), not %q", a)
		}
	}
	if o.root != "" && !filepath.IsAbs(o.root) {
		return o, false, errors.New("--root must be an absolute path")
	}
	if e2eBuild && (o.listen == "" || o.contentListen == "") {
		return o, false, errors.New("the development build needs --listen and --content-listen")
	}
	if !e2eBuild && o.node == "" && o.listen == "" {
		return o, false, errors.New("nothing to serve: give --node or --listen")
	}
	return o, false, nil
}

// loopback reports whether addr is "ip:port" with a loopback IP.
func loopback(addr string) bool {
	ap, err := netip.ParseAddrPort(addr)
	return err == nil && ap.Addr().Unmap().IsLoopback()
}

func run(args []string, stdout, stderr io.Writer) error {
	o, versionOnly, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	if versionOnly {
		printVersion(stdout)
		return nil
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(o.logLevel)); err != nil {
		return fmt.Errorf("--log-level: %w", err)
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, o, log)
}

// layout is a path of the install layout under --root.
func (o options) layout(p string) string { return filepath.Join(cmp.Or(o.root, "/"), p) }

func serve(ctx context.Context, o options, log *slog.Logger) error {
	log.Info("lodge starting", "version", version, "commit", commit, "development", e2eBuild)
	stateDir := o.layout("/var/lib/lodge")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return fmt.Errorf("state directory: %w", err)
	}
	db, err := store.Open(ctx, filepath.Join(stateDir, "lodge.db"))
	if err != nil {
		return err
	}
	defer db.Close()

	reg := apps.New(o.root, log)
	if err := reg.CheckSocketDir(); err != nil {
		return err
	}
	if err := reg.Load(); err != nil {
		return err
	}

	// The plain loopback listeners, bound first so that their names are
	// the addresses they got.
	var plainLodge, plainContentLn net.Listener
	for _, l := range []struct {
		addr string
		ln   *net.Listener
	}{{o.listen, &plainLodge}, {o.contentListen, &plainContentLn}} {
		if l.addr == "" {
			continue
		}
		if *l.ln, err = net.Listen("tcp", l.addr); err != nil {
			return fmt.Errorf("listen on %s: %w", l.addr, err)
		}
		defer (*l.ln).Close()
	}
	names := gateway.LoopbackNames(addrOf(plainLodge), addrOf(plainContentLn))

	ss := sessions.NewRegistry()
	al := allow.New(db, nil)
	tk := tokens.New(db, nil)

	// The inference layer (docs/specs/inference.md). Under --root every app
	// runs as the developer, whose uid then names its app in Lodge-App.
	devUID := -1
	if o.root != "" {
		devUID = os.Getuid()
	}
	inf := inference.New(inference.Options{DB: db, Callers: reg, DevUID: devUID, Log: log.With("part", "inference")})
	configPath := o.layout(o.config)
	inf.Load(inference.LoadConfig(configPath))

	reload := func() error {
		if err := reg.Load(); err != nil {
			return err
		}
		reg.Poll(ctx)
		inf.Load(inference.LoadConfig(configPath))
		return nil
	}

	// The tailnet nodes start without waiting to join: the control socket
	// and the health polls run while they wait for a login, so lodgectl
	// status shows the login URL. The development build joins no tailnet.
	var lodgeNode, contentNode *tailnet.Node
	if !e2eBuild && o.node != "" {
		lodgeNode, contentNode, err = newNodes(o, stateDir, log)
		if err != nil {
			return err
		}
		defer lodgeNode.Close()
		defer contentNode.Close()
	}

	// The gateway exists once the nodes have names; until then nothing it
	// serves can be open.
	var gw atomic.Pointer[gateway.Gateway]
	var mcpBase atomic.Value
	mcpBase.Store(names.LodgeOrigin)
	deps := control.Deps{
		Allow: al, Tokens: tk, Sessions: ss, Apps: reg, Reload: reload, Log: log, Inference: inf,
		EndToken: func(id int64) int {
			if g := gw.Load(); g != nil {
				return g.EndToken(id)
			}
			return 0
		},
		MCPURL: func(id string) string { return mcpBase.Load().(string) + "/mcp/" + id },
		Nodes: func(ctx context.Context) []control.Node {
			var out []control.Node
			for _, n := range []*tailnet.Node{lodgeNode, contentNode} {
				if n != nil {
					st := n.Status(ctx)
					out = append(out, control.Node{Name: st.Hostname, DNSName: st.DNSName, State: st.State,
						LoginURL: st.LoginURL, Up: st.State == "Running"})
				}
			}
			return out
		},
	}
	if lodgeNode != nil {
		deps.Peers = lodgeNode.Peers
	}
	ctlLn, err := control.Listen(o.layout("/run/lodge/control.sock"))
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	var servers []*http.Server
	var serversMu sync.Mutex
	defer func() {
		log.Info("lodge stopping")
		cancel()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		serversMu.Lock()
		for _, s := range servers {
			_ = s.Shutdown(sctx)
			_ = s.Close()
		}
		serversMu.Unlock()
		wg.Wait()
	}()
	wg.Go(func() { reg.Run(ctx) })
	wg.Go(func() {
		if err := control.New(deps).Serve(ctx, ctlLn); err != nil {
			errs <- err
		}
	})
	wg.Go(func() {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				if err := reload(); err != nil {
					log.Error("reload the apps", "err", err)
				} else {
					log.Info("apps reloaded")
				}
			}
		}
	})
	log.Info("control socket ready", "path", o.layout("/run/lodge/control.sock"))

	// The inference socket is in group lodge-apps, which lodge is in
	// (packaging/sysusers.d); under --root, it stays the developer's.
	group := "lodge-apps"
	if o.root != "" {
		group = ""
	}
	wg.Go(func() { inf.Run(ctx) })
	if infLn, err := inference.Listen(o.layout("/run/lodge/inference.sock"), group); err != nil {
		log.Error("no inference socket", "err", err)
	} else {
		wg.Go(func() {
			if err := inf.Serve(ctx, infLn); err != nil {
				errs <- fmt.Errorf("inference socket: %w", err)
			}
		})
		log.Info("inference socket ready", "path", o.layout("/run/lodge/inference.sock"))
	}

	if lodgeNode != nil {
		if err := upNodes(ctx, lodgeNode, contentNode); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		names = gateway.TailnetNames(lodgeNode.DNSName(), contentNode.DNSName())
		mcpBase.Store(names.LodgeOrigin)
	}
	g := gateway.New(gateway.Config{Names: names, Root: o.root, Apps: reg, Allow: al, Tokens: tk, Sessions: ss, Log: log})
	gw.Store(g)
	wg.Go(func() { g.Run(ctx) })

	type listener struct {
		name string
		ln   net.Listener
		h    http.Handler
	}
	var lns []listener
	if lodgeNode != nil {
		lt, err := lodgeNode.ListenTLS()
		if err != nil {
			return err
		}
		ct, err := contentNode.ListenTLS()
		if err != nil {
			return err
		}
		lns = append(lns, listener{"lodge node", lt, g.Lodge(lodgeNode)}, listener{"content node", ct, g.Content(contentNode)})
	}
	if plainLodge != nil {
		lns = append(lns, listener{"plain lodge", plainLodge, g.Lodge(plainWhoIs())})
	}
	if plainContentLn != nil {
		lns = append(lns, listener{"plain content", plainContentLn, plainContent(g.Content(plainWhoIs()))})
	}
	for _, l := range lns {
		// Every listener caps its connections per peer and in all
		// (lodge-http.md, Connections).
		l.ln = gateway.Limit(l.ln, gateway.PerPeerConns, gateway.MaxConns)
		srv := gateway.Server(l.h)
		srv.ErrorLog = slog.NewLogLogger(log.Handler(), slog.LevelDebug)
		serversMu.Lock()
		servers = append(servers, srv)
		serversMu.Unlock()
		log.Info("listening", "listener", l.name, "addr", l.ln.Addr().String())
		wg.Go(func() {
			if err := srv.Serve(l.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s: %w", l.name, err)
			}
		})
	}
	if e2eBuild {
		log.Warn("development build: every loopback peer is e2e@example.com, and no tailnet is joined",
			"lodge", names.LodgeOrigin, "content", names.ContentOrigin)
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		return err
	}
}

// newNodes starts both tailnet nodes without waiting for them. Each reads
// its auth key on first start: TS_AUTHKEY, or TS_AUTHKEY_CONTENT for the
// content node when that is set.
func newNodes(o options, stateDir string, log *slog.Logger) (*tailnet.Node, *tailnet.Node, error) {
	key := os.Getenv("TS_AUTHKEY")
	contentKey := cmp.Or(os.Getenv("TS_AUTHKEY_CONTENT"), key)
	lodgeNode, err := tailnet.New(tailnet.Config{Hostname: o.node, Dir: filepath.Join(stateDir, "tailnet", o.node), AuthKey: key, Log: log})
	if err != nil {
		return nil, nil, err
	}
	contentNode, err := tailnet.New(tailnet.Config{Hostname: o.contentNode, Dir: filepath.Join(stateDir, "tailnet", o.contentNode), AuthKey: contentKey, Log: log})
	if err != nil {
		lodgeNode.Close()
		return nil, nil, err
	}
	return lodgeNode, contentNode, nil
}

// upNodes waits for both nodes to join the tailnet.
func upNodes(ctx context.Context, nodes ...*tailnet.Node) error {
	errs := make([]error, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		wg.Go(func() { errs[i] = n.Up(ctx) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// addrOf is a listener's address, or "" for none.
func addrOf(ln net.Listener) string {
	if ln == nil {
		return ""
	}
	return ln.Addr().String()
}
