package apps

import (
	"errors"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/frostyard/lodge/internal/apptest"
)

// strict loads root's apps as a production lodge would check their
// sockets: only each manifest's user may serve one.
func strict(t *testing.T, root string) *Registry {
	t.Helper()
	r := New(root, nil)
	r.devUID = -1
	if err := r.Load(); err != nil {
		t.Fatal(err)
	}
	return r
}

// A socket owned or served by another user than the manifest's disables
// the app, and lodge never sends it a request (L3). (A socket systemd made
// reports root as its peer, which passes; a test cannot listen as root.)
func TestSocketPeerMustBeTheManifestUser(t *testing.T) {
	me, err := user.Current()
	if err != nil || !regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`).MatchString(me.Username) || me.Username == "root" {
		t.Skip("the test's user cannot be an app's")
	}
	other, err := user.Lookup("nobody")
	if err != nil || other.Uid == me.Uid {
		t.Skip("no user nobody")
	}
	root := t.TempDir()
	apptest.Install(t, root, "mine", map[string]any{"user": me.Username}, nil)
	apptest.Install(t, root, "theirs", map[string]any{"user": "nobody"}, nil)
	var mu sync.Mutex
	var asked []string
	for _, id := range []string{"mine", "theirs"} {
		apptest.Serve(t, root, id, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			asked = append(asked, id)
			mu.Unlock()
			apptest.Healthy("1")(w, r)
		}))
	}
	r := strict(t, root)
	r.Poll(t.Context())
	if a, _ := r.Get("mine"); a.State != Up {
		t.Errorf("an app served by its user is %s: %s", a.State, a.Reason)
	}
	a, _ := r.Get("theirs")
	if a.State != Disabled || !strings.Contains(a.Reason, "socket file is owned by uid "+me.Uid+", not by user nobody") {
		t.Errorf("an app served by another user is %s: %q", a.State, a.Reason)
	}
	mu.Lock()
	for _, id := range asked {
		if id == "theirs" {
			t.Error("lodge sent a request to a socket served by the wrong user")
		}
	}
	mu.Unlock()
	_, err = r.DialContext(t.Context(), "unix", "theirs:80")
	var pe *PeerError
	if !errors.As(err, &pe) {
		t.Errorf("dialing the wrong user's socket: %v, want a PeerError", err)
	}
	// Under a layout root, lodge's own uid serves any app (development).
	dev := New(root, nil)
	dev.Load()
	dev.Poll(t.Context())
	if a, _ := dev.Get("theirs"); a.State != Up {
		t.Errorf("under a layout root: %s %q", a.State, a.Reason)
	}
}

func TestManifestUserMustExist(t *testing.T) {
	root := t.TempDir()
	apptest.Install(t, root, "ghost", map[string]any{"user": "nosuchlodgeuser"}, nil)
	if a, _ := strict(t, root).Get("ghost"); a.Enabled() || !strings.Contains(a.Reason, "does not exist") {
		t.Errorf("an app whose user does not exist: %s %q", a.State, a.Reason)
	}
}

func TestCheckSocketDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "run/lodge/apps")
	if err := New(root, nil).CheckSocketDir(); err != nil {
		t.Errorf("a missing directory under a layout root: %v", err)
	}
	if err := checkSocketDir(dir, true); err == nil {
		t.Error("a missing directory in production was accepted")
	}
	os.MkdirAll(dir, 0o755)
	os.Chmod(dir, 0o755)
	if err := New(root, nil).CheckSocketDir(); err != nil {
		t.Errorf("a 0755 directory under a layout root: %v", err)
	}
	if os.Getuid() != 0 {
		if err := checkSocketDir(dir, true); err == nil || !strings.Contains(err.Error(), "not root") {
			t.Errorf("a directory not owned by root, in production: %v", err)
		}
	}
	for _, mode := range []os.FileMode{0o775, 0o757, 0o777} {
		os.Chmod(dir, mode)
		if err := checkSocketDir(dir, false); err == nil || !strings.Contains(err.Error(), "writable") {
			t.Errorf("mode %v: %v", mode, err)
		}
		// Under a layout root, the developer's umask is only warned about.
		if err := New(root, nil).CheckSocketDir(); err != nil {
			t.Errorf("mode %v under a layout root: %v", mode, err)
		}
	}
	os.Chmod(dir, 0o755)
	other := t.TempDir()
	os.MkdirAll(filepath.Join(other, "run/lodge"), 0o755)
	os.Symlink(dir, filepath.Join(other, "run/lodge/apps"))
	if err := checkSocketDir(filepath.Join(other, "run/lodge/apps"), false); err == nil {
		t.Error("a symbolic link was accepted")
	}
}
