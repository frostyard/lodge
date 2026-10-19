package lodgeapp

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// The install layout's paths (app-contract.md, Install layout), under
// Env.Root.
const (
	// AppsDir holds the app sockets, <id>.sock.
	AppsDir = "/run/lodge/apps"
	// InferenceSocket is lodge's inference socket, for app daemons.
	InferenceSocket = "/run/lodge/inference.sock"
	// LodgeUser and LodgeGroup are lodge's system user and its group,
	// which owns app sockets.
	LodgeUser  = "lodge"
	LodgeGroup = "lodge"
)

// Env is where an app's socket lives and whose connections it admits.
type Env struct {
	// Root moves every path of the install layout under it, for
	// development and tests (LODGE_ROOT). It is "" in production.
	Root string
	// LodgeUID is the only peer uid the app socket admits: user lodge's
	// in production, and under Root the uid in LODGE_UID, or the app's
	// own when that is unset.
	LodgeUID int
}

// LoadEnv reads LODGE_ROOT and LODGE_UID. Without LODGE_ROOT it looks up
// user lodge, and LODGE_UID is ignored.
func LoadEnv() (Env, error) {
	root := os.Getenv("LODGE_ROOT")
	if root == "" {
		u, err := user.Lookup(LodgeUser)
		if err != nil {
			return Env{}, fmt.Errorf("lodge's user: %w", err)
		}
		uid, err := strconv.Atoi(u.Uid)
		if err != nil {
			return Env{}, fmt.Errorf("lodge's uid %q: %w", u.Uid, err)
		}
		return Env{LodgeUID: uid}, nil
	}
	if !filepath.IsAbs(root) {
		return Env{}, errors.New("LODGE_ROOT must be an absolute path")
	}
	e := Env{Root: filepath.Clean(root), LodgeUID: os.Getuid()}
	if s := os.Getenv("LODGE_UID"); s != "" {
		uid, err := strconv.Atoi(s)
		if err != nil || uid < 0 {
			return Env{}, fmt.Errorf("LODGE_UID %q is not a uid", s)
		}
		e.LodgeUID = uid
	}
	return e, nil
}

// Path returns a path of the install layout, such as InferenceSocket,
// under Root.
func (e Env) Path(p string) string {
	if e.Root == "" {
		return p
	}
	return filepath.Join(e.Root, p)
}

// SocketPath returns app id's socket, /run/lodge/apps/<id>.sock under Root.
func (e Env) SocketPath(id string) string {
	return e.Path(filepath.Join(AppsDir, id+".sock"))
}
