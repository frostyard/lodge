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
	"github.com/frostyard/lodge/internal/codexauth"
	"github.com/frostyard/lodge/internal/control"
	"github.com/frostyard/lodge/internal/inference"
	"github.com/frostyard/lodge/internal/sessions"
	"github.com/frostyard/lodge/internal/tokens"
)

// Set by the Makefile's -ldflags.
var (
	version = "dev"
	commit  = "none"
)

const usage = `usage: lodgectl [--root DIR] [--json] COMMAND

  status                                   lodge's nodes, apps, sessions, providers and agent socket
  apps                                     each app's manifest, state and reason
  reload                                   reread the manifests and the configuration
  logins | nodes                           the allowed logins or nodes
  allow login LOGIN | allow node NODE      allow one (NODE: an ID, or a peer's name)
  disallow login LOGIN | disallow node NODE  remove one, closing its sessions
  tokens                                   the MCP tokens
  token issue NAME --app ID --scope SCOPE [--agent USER]
                                           print a new token on stdout, once; with
                                           --agent, for the agent socket and USER only
  token revoke NAME                        revoke one, closing its streams
  sessions                                 the live sessions
  session end ID                           close one
  usage [--since YYYY-MM-DD]               inference use per app and alias
  provider key NAME                        set an inference provider's key from stdin; empty removes it
  provider login NAME [--client-id ID] [--email EMAIL] [--port N] [--out FILE]
                                           on a computer with a browser: sign a codex provider in
                                           to your ChatGPT plan, and write its login file
  provider login NAME --copilot [--out FILE]
                                           on any computer: sign in to GitHub for a copilot
                                           provider, and write the GitHub token's file
  provider import NAME                     on lodge's host: import a login file from stdin
  provider models NAME                     the models a provider offers
  version                                  lodgectl's version

--root DIR (or LODGE_ROOT) finds the socket under DIR, for the development build.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer, getenv func(string) string) int {
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
	if len(args) >= 2 && args[0] == "provider" && args[1] == "login" {
		// The sign-in needs a browser, not lodge: it runs on the owner's
		// computer and writes a file for lodge's host.
		return login(args[2:], stdout, stderr, getenv)
	}
	cmd, err := parse(args, stdin)
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

func parse(args []string, stdin io.Reader) (command, error) {
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
		return command{}, errors.New("token issue NAME --app ID --scope SCOPE [--agent USER], or token revoke NAME")
	case "session":
		if len(args) == 3 && args[1] == "end" {
			return command{method: "lodge.endSession", params: map[string]string{"id": args[2]}, show: showDone("ended " + args[2])}, nil
		}
		return command{}, errors.New("session end ID")
	case "usage":
		return parseUsage(args[1:])
	case "provider":
		if len(args) == 3 && args[1] == "key" {
			// The key comes from stdin, never the command line, which
			// other users can see.
			b, err := io.ReadAll(io.LimitReader(stdin, 8<<10))
			if err != nil {
				return command{}, err
			}
			key := strings.TrimSpace(string(b))
			what := "key set for provider " + args[2]
			if key == "" {
				what = "key removed for provider " + args[2]
			}
			return command{method: "lodge.providerKey", params: map[string]string{"provider": args[2], "key": key}, show: showDone(what)}, nil
		}
		if len(args) == 3 && args[1] == "import" {
			var l codexauth.Login
			if err := json.UnmarshalRead(io.LimitReader(stdin, 1<<20), &l); err != nil {
				return command{}, fmt.Errorf("provider import NAME reads a login file from lodgectl provider login on stdin: %w", err)
			}
			return command{method: "lodge.providerImport", params: map[string]any{"provider": args[2], "login": l}, show: showImported(args[2])}, nil
		}
		if len(args) == 3 && args[1] == "models" {
			return command{method: "lodge.providerModels", params: map[string]string{"provider": args[2]}, show: showModels}, nil
		}
		return command{}, errors.New("provider key NAME (the key on stdin), provider import NAME (the login file on stdin), " +
			"provider models NAME, or provider login NAME")
	}
	return command{}, fmt.Errorf("no command %q", args[0])
}

func parseIssue(args []string) (command, error) {
	fs := flag.NewFlagSet("token issue", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	app := fs.String("app", "", "")
	scope := fs.String("scope", "", "")
	agent := fs.String("agent", "", "")
	const want = "token issue NAME --app ID --scope SCOPE [--agent USER]"
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
		return command{}, errors.New(want)
	}
	if name == "" || *app == "" || *scope == "" {
		return command{}, errors.New(want)
	}
	params := map[string]string{"name": name, "app": *app, "scope": *scope}
	if *agent != "" {
		params["agent"] = *agent
	}
	return command{method: "lodge.issueToken", params: params, show: showIssued}, nil
}

func parseUsage(args []string) (command, error) {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	since := fs.String("since", "", "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return command{}, errors.New("usage [--since YYYY-MM-DD]")
	}
	params := map[string]string{}
	if *since != "" {
		params["since"] = *since
	}
	return command{method: "lodge.usage", params: params, show: showUsage}, nil
}

func showUsage(o *out, raw []byte) error {
	var list []inference.Sum
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	var rows [][]string
	for _, u := range list {
		rows = append(rows, []string{u.App, u.Alias, fmt.Sprint(u.Requests), fmt.Sprint(u.Failures), fmt.Sprint(u.Input), fmt.Sprint(u.Output)})
	}
	table(o.stdout, "APP\tALIAS\tREQUESTS\tFAILURES\tINPUT TOKENS\tOUTPUT TOKENS", rows)
	return nil
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
// to Claude Code, never with --scope project, or, for an agent, where its
// agent uses it.
func showIssued(o *out, raw []byte) error {
	var is control.Issued
	if err := json.Unmarshal(raw, &is); err != nil {
		return err
	}
	fmt.Fprintln(o.stdout, is.Token)
	if is.Agent != "" {
		fmt.Fprintf(o.stderr, "Shown once. It works only on the agent socket, %s, from user %s: send it to %s as \"Authorization: Bearer <token>\".\n",
			is.Socket, is.Agent, is.URL)
		return nil
	}
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
		rows = append(rows, []string{"node", n.Name, nodeState(n), detail})
	}
	for _, a := range st.Apps {
		rows = append(rows, []string{"app", a.ID, string(a.State), a.Reason})
	}
	for _, p := range st.Providers {
		state, detail := "available", fmt.Sprintf("%s, %d slots, %d running, %d waiting", p.Type, p.Slots, p.Running, p.Waiting)
		if !p.Available {
			state, detail = "unavailable", p.Type+": "+p.Reason
		}
		if p.Login != "" {
			detail += "; signed in as " + p.Login
		}
		rows = append(rows, []string{"provider", p.Name, state, detail})
	}
	if st.InferenceRefused != "" {
		rows = append(rows, []string{"inference", "config", "refused", st.InferenceRefused})
	}
	if as := st.AgentSocket; as != nil {
		state := map[bool]string{true: "listening", false: "down"}[as.Listening]
		detail := as.Path + ", agents: " + cmp(strings.Join(as.Agents, " "), "none")
		if len(as.Skipped) > 0 {
			detail += "; no such user: " + strings.Join(as.Skipped, " ")
		}
		rows = append(rows, []string{"agents", "socket", state, detail})
		if as.Refused != "" {
			rows = append(rows, []string{"agents", "config", "refused", as.Refused})
		}
	}
	rows = append(rows, []string{"sessions", fmt.Sprint(st.Sessions), "", ""})
	table(o.stdout, "KIND\tNAME\tSTATE\tDETAIL", rows)
	return nil
}

// nodeState writes a node's backend state for people.
func nodeState(n control.Node) string {
	switch n.State {
	case "Running":
		return "up"
	case "NeedsLogin":
		return "needs login"
	case "NeedsMachineAuth":
		return "needs approval"
	case "Starting", "NoState":
		return "starting"
	case "":
		return map[bool]string{true: "up", false: "down"}[n.Up]
	}
	return strings.ToLower(n.State)
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
		why := slices.DeleteFunc(append([]string{a.Reason}, a.Notes...), func(s string) bool { return s == "" })
		rows = append(rows, []string{a.ID, m.Name, m.Version, cmp(a.Key, "-"), string(a.State), strings.Join(flags, ","), strings.Join(why, "; ")})
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
		rows = append(rows, []string{t.Name, t.App, t.Scope, cmp(t.Agent, "-"), when(t.Created), when(t.LastUsed), when(t.Revoked)})
	}
	table(o.stdout, "NAME\tAPP\tSCOPE\tAGENT\tCREATED\tLAST USE\tREVOKED", rows)
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

func showImported(name string) func(*out, []byte) error {
	return func(o *out, raw []byte) error {
		var r struct {
			Account  string `json:"account"`
			ClientID string `json:"client_id"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return err
		}
		fmt.Fprintf(o.stdout, "provider %s is signed in as %s (client %s); delete the login file you imported\n", name, r.Account, r.ClientID)
		return nil
	}
}

func showModels(o *out, raw []byte) error {
	var list []inference.Model
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	var rows [][]string
	for _, m := range list {
		rows = append(rows, []string{m.ID, m.Name})
	}
	table(o.stdout, "MODEL\tNAME", rows)
	return nil
}
