// Command lodge-hearthapp runs hearth's example app, notelite, against lodge's
// development build: it installs the app under LODGE_ROOT and serves it on the
// app's socket, so that the MCP gateway and /app/<id>/connect/ have a real app
// on hearth to call (docs/specs/app-contract.md, Apps on hearth).
//
// It is not shipped.
//
//	LODGE_ROOT=$ROOT lodge-hearthapp [--db FILE]
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"syscall"

	"github.com/frostyard/lodge/internal/hearthtest"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "lodge-hearthapp:", err)
		os.Exit(1)
	}
}

func run() error {
	db := flag.String("db", "", "the app's SQLite file (default: a temporary one)")
	flag.Parse()
	root := os.Getenv("LODGE_ROOT")
	if root == "" {
		return fmt.Errorf("LODGE_ROOT is not set: run this beside lodge's development build")
	}
	name := os.Getenv("LODGE_APP_USER")
	if name == "" {
		u, err := user.Current()
		if err != nil {
			return fmt.Errorf("read the current user: %w", err)
		}
		name = u.Username
	}
	if err := hearthtest.Install(root, name); err != nil {
		return err
	}
	if *db == "" {
		dir, err := os.MkdirTemp("", "lodge-hearthapp.")
		if err != nil {
			return err
		}
		// The temporary database goes when the app stops.
		defer func() { _ = os.RemoveAll(dir) }()
		*db = filepath.Join(dir, "notes.db")
	}
	sock := filepath.Join(root, "run/lodge/apps", hearthtest.ID+".sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		return err
	}
	_ = os.Remove(sock)
	l, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Under a layout root the app runs as the developer, whose uid is lodge's
	// own there, so the peer check passes.
	app, err := hearthtest.Serve(ctx, l, os.Getuid(), *db)
	if err != nil {
		return err
	}
	fmt.Printf("lodge-hearthapp: %s on %s\n", hearthtest.ID, sock)
	<-ctx.Done()
	return app.Close()
}
