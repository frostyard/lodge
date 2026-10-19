package scaffoldcheck

import (
	"encoding/json/v2"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// root is the repository's root, from this package's directory.
var root = filepath.Join("..", "..")

func skill(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join(root, ".agents", "skills", "new-lodge-app"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// render runs the skill's render.sh, and returns its output and error.
func render(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command("sh", append([]string{filepath.Join(skill(t), "render.sh")}, args...)...).CombinedOutput()
	return string(out), err
}

// gocmd runs go in dir, offline: the rendered app builds on this
// checkout's kit, from the module cache the kit's own gates filled.
func gocmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=-mod=mod", "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var placeholder = regexp.MustCompile(`__(ID|NAME|USER|KEY|MODULE|LODGE_REF)__`)

// The scaffold renders an app that builds on the kit and passes the
// contract's socket rules: the rendered app's own tests run
// kittest.CheckRules against its daemon.
func TestTheScaffoldPassesTheSocketRules(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go on PATH")
	}
	kit, err := filepath.Abs(filepath.Join(root, "kit"))
	if err != nil {
		t.Fatal(err)
	}
	probe := exec.Command("go", "mod", "download")
	probe.Dir = kit
	probe.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOPROXY=off")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("the module cache lacks the kit's dependencies (make verify fills it): %s", out)
	}

	app := filepath.Join(t.TempDir(), "tasks")
	if out, err := render(t, app, "tasks", "Tasks", "", "t"); err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	err = filepath.WalkDir(app, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(app, path)
		if strings.HasSuffix(rel, ".tmpl") || placeholder.MatchString(rel) || placeholder.MatchString(read(t, path)) {
			t.Errorf("%s is not filled in", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	gocmd(t, app, "mod", "edit", "-replace=github.com/frostyard/lodge/kit="+kit)
	gocmd(t, app, "mod", "tidy")
	gocmd(t, app, "vet", "./...")
	gocmd(t, app, "test", "-count=1", "./...")
	gocmd(t, app, "build", "-o", "build/", "./cmd/...")

	t.Run("the web bundle", func(t *testing.T) {
		for path, want := range map[string]string{
			"web/package.json":        `"@frostyard/lodge-client": "github:frostyard/lodge#5ee27a15e2fb0cc3dab3ddd489e1062b2d39c09f&path:client"`,
			"web/vite.config.ts":      `base: "/app/tasks/"`,
			"web/src/main.ts":         `connectLodge({ app: "tasks" })`,
			"web/index.html":          `<script type="module" src="/src/main.ts"></script>`,
			"web/public/icon.svg":     `<svg`,
			"web/pnpm-workspace.yaml": `storeDir:`,
			"Makefile":                `--outDir ../build/web`,
		} {
			if got := read(t, filepath.Join(app, path)); !strings.Contains(got, want) {
				t.Errorf("%s lacks %s", path, want)
			}
		}
	})

	t.Run("the units", func(t *testing.T) {
		for path, want := range map[string][]string{
			"packaging/lodge/frosttasks.socket": {"ListenStream=/run/lodge/apps/tasks.sock", "SocketUser=frosttasks",
				"SocketGroup=lodge", "SocketMode=0660", "FileDescriptorName=tasks"},
			"packaging/lodge/frosttasks.service": {"User=frosttasks", "SupplementaryGroups=lodge-apps",
				"ExecStart=/usr/local/bin/tasksd", "RestrictAddressFamilies=AF_UNIX"},
			"packaging/lodge/sysusers.d-frosttasks.conf": {"u frosttasks ", "m frosttasks lodge-apps"},
		} {
			got := read(t, filepath.Join(app, path))
			for _, w := range want {
				if !strings.Contains(got, w) {
					t.Errorf("%s lacks %s", path, w)
				}
			}
		}
	})

	t.Run("staging", func(t *testing.T) { stage(t, app) })
}

// stage.sh stages the app into a lodge root other apps share, and removes
// nothing of theirs.
func stage(t *testing.T, app string) {
	dest := t.TempDir()
	theirs := []string{"usr/share/lodge/apps/mail/app.json", "usr/local/bin/maild", "usr/share/lodge/apps/tasksx/keep"}
	for _, f := range append(theirs, "usr/share/lodge/apps/tasks/web/stale.js") {
		p := filepath.Join(dest, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("theirs"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(app, "build", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "build", "web", "index.html"), []byte("<!doctype html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	stageSh := filepath.Join(app, "packaging", "lodge", "stage.sh")
	run := func(args ...string) (string, error) {
		cmd := exec.Command(stageSh, args...)
		cmd.Dir = app
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := run(dest, "1.2.3", "build"); err != nil {
		t.Fatalf("stage.sh: %v\n%s", err, out)
	}
	for _, f := range []string{"usr/local/bin/tasksd", "usr/share/lodge/apps/tasks/web/index.html",
		"usr/lib/systemd/system/frosttasks.socket", "usr/lib/systemd/system/frosttasks.service",
		"usr/lib/sysusers.d/frosttasks.conf"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("staged no %s: %v", f, err)
		}
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(dest, "usr/share/lodge/apps/tasks/app.json"))), &m); err != nil ||
		m["version"] != "1.2.3" || m["id"] != "tasks" || m["key"] != "t" {
		t.Errorf("the staged manifest = %v, %v", m, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "usr/share/lodge/apps/tasks/web/stale.js")); err == nil {
		t.Error("a stale file of the app's own web build stayed")
	}
	for _, f := range theirs {
		if got := read(t, filepath.Join(dest, f)); got != "theirs" {
			t.Errorf("staging changed another app's %s", f)
		}
	}
	for _, args := range [][]string{{"/", "1.2.3"}, {dest + "/..//" + filepath.Base(dest) + "/../../../../../../../../", "1"}, {dest, "1 2"}, {dest, "1;rm"}} {
		if out, err := run(append(args, "build")...); err == nil {
			t.Errorf("stage.sh %v succeeded: %s", args, out)
		}
	}
}

// render.sh refuses what a manifest refuses, and never overwrites.
func TestRenderRefuses(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "full")
	if err := os.MkdirAll(full, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(full, "mine"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{filepath.Join(dir, "a"), "Tasks", "Tasks"},
		{filepath.Join(dir, "b"), "lodge", "Lodge"},
		{filepath.Join(dir, "c"), "a234567890123456x", "Long"},
		{filepath.Join(dir, "d"), "tasks", "Tasks & co"},
		{filepath.Join(dir, "e"), "tasks", ""},
		{filepath.Join(dir, "f"), "tasks", "Tasks", "root"},
		{filepath.Join(dir, "g"), "tasks", "Tasks", "frosttasks", "tt"},
		{filepath.Join(dir, "h"), "tasks", "Tasks", "frosttasks", "t", "example.com/a b"},
		{filepath.Join(dir, "i"), "tasks", "Tasks", "frosttasks", "t", "example.com/tasks", "main"},
		{full, "tasks", "Tasks"},
	} {
		if out, err := render(t, args...); err == nil {
			t.Errorf("render %v succeeded: %s", args[1:], out)
		}
	}
	if _, err := os.Stat(filepath.Join(full, "go.mod")); err == nil {
		t.Error("render wrote into a directory that was not empty")
	}
}

// Without a key, the manifest has none, and is still JSON.
func TestRenderWithoutAKey(t *testing.T) {
	app := filepath.Join(t.TempDir(), "notes2")
	if out, err := render(t, app, "notes2", "Notes Two", "frostnotes2", "", "example.com/notes2"); err != nil {
		t.Fatalf("render: %v\n%s", err, out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(read(t, filepath.Join(app, "packaging/lodge/app.json"))), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["key"]; ok || m["user"] != "frostnotes2" || m["name"] != "Notes Two" {
		t.Errorf("app.json = %v", m)
	}
	if got := read(t, filepath.Join(app, "go.mod")); !strings.HasPrefix(got, "module example.com/notes2\n") {
		t.Errorf("go.mod = %q", got)
	}
	if _, err := os.Stat(filepath.Join(app, "cmd", "notes2d", "main.go")); err != nil {
		t.Errorf("no cmd/notes2d: %v", err)
	}
}
