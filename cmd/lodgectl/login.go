package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"

	"github.com/frostyard/lodge/internal/codexauth"
)

// login signs a codex provider in to the owner's ChatGPT plan on this
// computer, and writes the login file that lodge's host imports
// (docs/specs/inference.md, The codex provider; OpenAI's self-hosted VMs).
func login(args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	fs := flag.NewFlagSet("provider login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	clientID := fs.String("client-id", "", "sign in again to this registration (lodgectl status names it)")
	email := fs.String("email", "", "the ChatGPT account's email, as a hint")
	port := fs.Int("port", 0, "the browser's callback port on 127.0.0.1 [1455, or any free one]")
	outPath := fs.String("out", "", "the login file to write [lodge-NAME-login.json]")
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		fmt.Fprintln(stderr, "lodgectl: provider login NAME [--client-id ID] [--email EMAIL] [--port N] [--out FILE]")
		return 2
	}
	name := args[0]
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return 2
	}
	path := cmp(*outPath, "lodge-"+name+"-login.json")
	dir := getenv("XDG_CONFIG_HOME")
	if dir == "" {
		dir = filepath.Join(getenv("HOME"), ".config")
	}
	host, err := codexauth.HostID(filepath.Join(dir, "lodge"))
	if err != nil {
		fmt.Fprintf(stderr, "lodgectl: this computer's host id: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	l, err := codexauth.SignIn(ctx, codexauth.SignInOptions{Endpoints: codexauth.OpenAI, HostID: host, ClientID: *clientID,
		Email: *email, Port: *port, Open: openBrowser, Out: stderr})
	if err != nil {
		fmt.Fprintf(stderr, "lodgectl: %v\n", err)
		return 1
	}
	if err := writeLogin(path, l); err != nil {
		fmt.Fprintf(stderr, "lodgectl: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, `Signed in as %s (client %s). %s holds the sign-in's tokens.
On lodge's host, import it, then delete it here and there:

  sudo lodgectl provider import %s < %s
`, cmp(l.Email, l.Account), l.ClientID, path, name, filepath.Base(path))
	return 0
}

// writeLogin writes the login file, readable by its owner only.
func writeLogin(path string, l codexauth.Login) error {
	b, err := json.Marshal(l, json.Deterministic(true))
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("the login file: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return fmt.Errorf("the login file: %w", err)
	}
	_, werr := f.Write(append(b, '\n'))
	return errors.Join(werr, f.Close())
}

// openBrowser opens u in the desktop's browser, when there is one; the
// URL is printed anyway.
func openBrowser(u string) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	if p, err := exec.LookPath(name); err == nil {
		cmd := exec.Command(p, u)
		if cmd.Start() == nil {
			go func() { _ = cmd.Wait() }()
		}
	}
}
