// Command lodgectl manages a running lodge over its control socket,
// /run/lodge/control.sock (docs/specs/lodge-http.md, The control socket and
// lodgectl). The socket admits root and lodge's own user, so it runs under
// sudo: sudo lodgectl allow login alice@example.com.
package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/frostyard/lodge/internal/allow"
	"github.com/frostyard/lodge/internal/apps"
	"github.com/frostyard/lodge/internal/control"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/tokens"
)

// Set by the Makefile's -ldflags.
var (
	version = "dev"
	commit  = "none"
)

const usage = `usage: lodgectl [--root DIR] [--json] COMMAND

  status                                   lodge's nodes, apps and sessions
  apps                                     each app's manifest, state and reason
  reload                                   reread the manifests
  logins | nodes                           the allowed logins or nodes
  allow login LOGIN | allow node NODE      allow one (NODE: an ID, or a peer's name)
  disallow login LOGIN | disallow node NODE  remove one, closing its sessions
  tokens                                   the MCP tokens
  token issue NAME --app ID --scope SCOPE  print a new token on stdout, once
  token revoke NAME                        revoke one, closing its streams
  sessions                                 the live sessions
  session end ID                           close one
  version                                  lodgectl's version

--root DIR (or LODGE_ROOT) finds the socket under DIR, for the development build.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("lodgectl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	root := fs.String("root", getenv("LODGE_ROOT"), "the layout root")
	asJSON := fs.Bool("json", false, "print the answer as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	args = fs.Args()
	if len(args) == 0 {
		fs.Usage()
		return 2
	}
	if args[0] == "version" {
		fmt.Fprintf(stdout, "lodgectl %s (%s)\n", version, commit)
		return 0
	}
	cmd, err := parse(args)
	if err != nil {
		fmt.Fprintf(stderr, "lodgectl: %v\n\n%s", err, usage)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sock := filepath.Join(cmp(*root, "/"), "run/lodge/control.sock")
	c, err := control.Dial(ctx, sock)
	if err != nil {
		fmt.Fprintf(stderr, "lodgectl: cannot reach lodge at %s: %v (run lodgectl as root: sudo lodgectl …)\n", sock, err)
		return 1
	}
	defer c.Close()
	if err := cmd.do(ctx, c, &out{stdout: stdout, stderr: stderr, json: *asJSON}); err != nil {
		fmt.Fprintf(stderr, "lodgectl: %v\n", err)
		return 1
	}
	return 0
}

func cmp(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type out struct {
	stdout, stderr io.Writer
	json           bool
}

// command is one parsed command line.
type command struct {
	method string
	params any
	// show prints the result; nil prints nothing but JSON when asked.
	show func(o *out, result []byte) error
}

func (cmd command) do(ctx context.Context, c *control.Client, o *out) error {
	var raw jsontext.Value
	if err := c.Call(ctx, cmd.method, cmd.params, &raw); err != nil {
		return err
	}
	if o.json {
		_, err := fmt.Fprintf(o.stdout, "%s\n", raw)
		return err
	}
	if cmd.show == nil {
		return nil
	}
	return cmd.show(o, raw)
}

func parse(args []string) (command, error) {
	need := func(n int) error {
		if len(args) != n {
			return fmt.Errorf("%s takes no arguments", args[0])
		}
		return nil
	}
	switch args[0] {
	case "status":
		return command{method: "lodge.status", show: showStatus}, need(1)
	case "apps":
		return command{method: "lodge.apps", show: showApps}, need(1)
	case "reload":
		return command{method: "lodge.reload", show: showApps}, need(1)
	case "logins":
		return command{method: "lodge.logins", show: showLogins}, need(1)
	case "nodes":
		return command{method: "lodge.nodes", show: showNodes}, need(1)
	case "tokens":
		return command{method: "lodge.tokens", show: showTokens}, need(1)
	case "sessions":
		return command{method: "lodge.sessions", show: showSessions}, need(1)
	case "allow", "disallow":
		if len(args) != 3 || (args[1] != "login" && args[1] != "node") {
			return command{}, fmt.Errorf("%s login LOGIN, or %s node NODE", args[0], args[0])
		}
		method := map[string]string{"allow login": "lodge.allowLogin", "allow node": "lodge.allowNode",
			"disallow login": "lodge.disallowLogin", "disallow node": "lodge.disallowNode"}[args[0]+" "+args[1]]
		return command{method: method, params: map[string]string{args[1]: args[2]}, show: showDone(args[0] + " " + args[1] + " " + args[2])}, nil
	case "token":
		if len(args) >= 2 && args[1] == "issue" {
			return parseIssue(args[2:])
		}
		if len(args) == 3 && args[1] == "revoke" {
			return command{method: "lodge.revokeToken", params: map[string]string{"name": args[2]}, show: showDone("revoked " + args[2])}, nil
		}
		return command{}, errors.New("token issue NAME --app ID --scope SCOPE, or token revoke NAME")
	case "session":
		if len(args) == 3 && args[1] == "end" {
			return command{method: "lodge.endSession", params: map[string]string{"id": args[2]}, show: showDone("ended " + args[2])}, nil
		}
		return command{}, errors.New("session end ID")
	case "usage":
		return command{method: "lodge.usage"}, need(1)
	}
	return command{}, fmt.Errorf("no command %q", args[0])
}

func parseIssue(args []string) (command, error) {
	fs := flag.NewFlagSet("token issue", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	app := fs.String("app", "", "")
	scope := fs.String("scope", "", "")
	var name string
	// The name may come before or after the flags.
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return command{}, err
	}
	if name == "" && fs.NArg() == 1 {
		name = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return command{}, errors.New("token issue NAME --app ID --scope SCOPE")
	}
	if name == "" || *app == "" || *scope == "" {
		return command{}, errors.New("token issue NAME --app ID --scope SCOPE")
	}
	return command{method: "lodge.issueToken", params: map[string]string{"name": name, "app": *app, "scope": *scope}, show: showIssued}, nil
}

func showDone(what string) func(*out, []byte) error {
	return func(o *out, raw []byte) error {
		var c control.Closed
		_ = json.Unmarshal(raw, &c)
		if c.Closed > 0 {
			fmt.Fprintf(o.stdout, "%s (closed %d)\n", what, c.Closed)
		} else {
			fmt.Fprintln(o.stdout, what)
		}
		return nil
	}
}

// showIssued prints the token alone on stdout, and on stderr how to add it
// to Claude Code, never with --scope project.
func showIssued(o *out, raw []byte) error {
	var is control.Issued
	if err := json.Unmarshal(raw, &is); err != nil {
		return err
	}
	fmt.Fprintln(o.stdout, is.Token)
	fmt.Fprintf(o.stderr, "Shown once. Add it to Claude Code with:\n  claude mcp add --transport http %s %s --header \"Authorization: Bearer <token>\"\n", is.App, is.URL)
	return nil
}

func table(w io.Writer, header string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// here is the machine's time zone, for printing times.
func here() *time.Location { return time.Now().Location() }

func when(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.In(here()).Format("2006-01-02 15:04")
}

func showStatus(o *out, raw []byte) error {
	var st control.Status
	if err := json.Unmarshal(raw, &st); err != nil {
		return err
	}
	var rows [][]string
	for _, n := range st.Nodes {
		detail := n.DNSName
		if n.LoginURL != "" {
			detail = "log in at " + n.LoginURL
		}
		rows = append(rows, []string{"node", n.Name, cmp(n.State, map[bool]string{true: "up", false: "down"}[n.Up]), detail})
	}
	for _, a := range st.Apps {
		rows = append(rows, []string{"app", a.ID, string(a.State), a.Reason})
	}
	rows = append(rows, []string{"sessions", fmt.Sprint(st.Sessions), "", ""})
	table(o.stdout, "KIND\tNAME\tSTATE\tDETAIL", rows)
	return nil
}

func showApps(o *out, raw []byte) error {
	var list []apps.App
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	var rows [][]string
	for _, a := range list {
		m := a.Manifest
		var flags []string
		for f, on := range map[string]bool{"content": m.Content, "contentLinks": m.ContentLinks, "device": m.Device, "mcp": m.MCP, "remoteImages": m.RemoteImages, "companion": m.Companion} {
			if on {
				flags = append(flags, f)
			}
		}
		slices.Sort(flags)
		rows = append(rows, []string{a.ID, m.Name, m.Version, cmp(a.Key, "-"), string(a.State), strings.Join(flags, ","), a.Reason})
	}
	table(o.stdout, "ID\tNAME\tVERSION\tKEY\tSTATE\tFLAGS\tREASON", rows)
	return nil
}

func showLogins(o *out, raw []byte) error {
	var list []allow.Login
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	var rows [][]string
	for _, l := range list {
		rows = append(rows, []string{l.Login, when(l.Added)})
	}
	table(o.stdout, "LOGIN\tADDED", rows)
	return nil
}

func showNodes(o *out, raw []byte) error {
	var list []allow.Node
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	var rows [][]string
	for _, n := range list {
		rows = append(rows, []string{n.ID, cmp(n.Name, "-"), when(n.Added)})
	}
	table(o.stdout, "ID\tNAME\tADDED", rows)
	return nil
}

func showTokens(o *out, raw []byte) error {
	var list []tokens.Token
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	var rows [][]string
	for _, t := range list {
		rows = append(rows, []string{t.Name, t.App, t.Scope, when(t.Created), when(t.LastUsed), when(t.Revoked)})
	}
	table(o.stdout, "NAME\tAPP\tSCOPE\tCREATED\tLAST USE\tREVOKED", rows)
	return nil
}

func showSessions(o *out, raw []byte) error {
	var list []sessions.Info
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	var rows [][]string
	for _, s := range list {
		rows = append(rows, []string{s.ID, s.App, s.Login, s.NodeName, when(s.Started), s.SeenAt.In(here()).Format("15:04:05")})
	}
	table(o.stdout, "ID\tAPP\tLOGIN\tNODE\tSTARTED\tSEEN", rows)
	return nil
}
