package packagecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const packaging = "../../packaging"

// sources makes stand-in binaries and a shell bundle.
func sources(t *testing.T, asset string) (bin, shell string) {
	t.Helper()
	dir := t.TempDir()
	bin, shell = filepath.Join(dir, "bin"), filepath.Join(dir, "shell")
	for _, d := range []string{bin, filepath.Join(shell, "assets")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range []string{"lodge", "lodgectl"} {
		if err := os.WriteFile(filepath.Join(bin, b), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"index.html": "<!doctype html>", "assets/" + asset: "x", "manifest.webmanifest": "{}"} {
		if err := os.WriteFile(filepath.Join(shell, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return bin, shell
}

func install(t *testing.T, dest, bin, shell string, vars ...string) string {
	t.Helper()
	args := append([]string{"DESTDIR=" + dest, "BIN_SRC=" + bin, "SHELL_SRC=" + shell, "ACTIVATE=no"}, vars...)
	out, err := exec.Command(filepath.Join(packaging, "install.sh"), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	return string(out)
}

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestInstallLayout(t *testing.T) {
	dest := t.TempDir()
	bin, shell := sources(t, "app-1.js")
	install(t, dest, bin, shell)
	want := map[string]os.FileMode{
		"usr/bin/lodge":                              0o755,
		"usr/bin/lodgectl":                           0o755,
		"usr/lib/systemd/system/lodge.service":       0o644,
		"usr/lib/sysusers.d/lodge.conf":              0o644,
		"usr/lib/tmpfiles.d/lodge.conf":              0o644,
		"usr/share/lodge/shell":                      0o755,
		"usr/share/lodge/shell/assets":               0o755,
		"usr/share/lodge/shell/index.html":           0o644,
		"usr/share/lodge/shell/assets/app-1.js":      0o644,
		"usr/share/lodge/shell/manifest.webmanifest": 0o644,
		"usr/share/lodge/apps":                       0o755,
		"etc/lodge":                                  0o755,
	}
	for p, m := range want {
		if got := mode(t, filepath.Join(dest, p)); got != m {
			t.Errorf("%s: mode %v, want %v", p, got, m)
		}
	}
	unit, _ := os.ReadFile(filepath.Join(dest, "usr/lib/systemd/system/lodge.service"))
	if !strings.Contains(string(unit), "\nExecStart=/usr/bin/lodge\n") {
		t.Error("the unit's ExecStart is not /usr/bin/lodge")
	}

	// A second install replaces the shell whole: old assets go.
	_, shell2 := sources(t, "app-2.js")
	install(t, dest, bin, shell2)
	if _, err := os.Stat(filepath.Join(dest, "usr/share/lodge/shell/assets/app-1.js")); err == nil {
		t.Error("an old shell asset survived a reinstall")
	}
	if _, err := os.Stat(filepath.Join(dest, "usr/share/lodge/shell/assets/app-2.js")); err != nil {
		t.Error("the new shell asset is missing")
	}
	entries, _ := os.ReadDir(filepath.Join(dest, "usr/share/lodge"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Errorf("a temporary %s was left behind", e.Name())
		}
	}
}

// Another PREFIX moves the binaries, rewrites ExecStart, and puts the
// host's units under /etc; the layout's own paths stay.
func TestInstallPrefix(t *testing.T) {
	dest := t.TempDir()
	bin, shell := sources(t, "a.js")
	install(t, dest, bin, shell, "PREFIX=/opt/lodge")
	for _, p := range []string{"opt/lodge/bin/lodge", "opt/lodge/bin/lodgectl", "etc/systemd/system/lodge.service",
		"etc/sysusers.d/lodge.conf", "etc/tmpfiles.d/lodge.conf", "usr/share/lodge/shell/index.html"} {
		if _, err := os.Stat(filepath.Join(dest, p)); err != nil {
			t.Errorf("%s is missing", p)
		}
	}
	unit, _ := os.ReadFile(filepath.Join(dest, "etc/systemd/system/lodge.service"))
	if !strings.Contains(string(unit), "\nExecStart=/opt/lodge/bin/lodge\n") {
		t.Errorf("ExecStart was not rewritten:\n%s", unit)
	}
}

func TestInstallRefusesMissingSources(t *testing.T) {
	bin, shell := sources(t, "a.js")
	os.Remove(filepath.Join(shell, "index.html"))
	out, err := exec.Command(filepath.Join(packaging, "install.sh"), "DESTDIR="+t.TempDir(), "BIN_SRC="+bin, "SHELL_SRC="+shell).CombinedOutput()
	if err == nil || !strings.Contains(string(out), "make shell-build") {
		t.Errorf("a missing shell: %v %s", err, out)
	}
}

// The contract's directories and modes (app-contract.md, Install layout),
// and no RuntimeDirectory, which would take the apps' sockets with it.
func TestUnitAndEntries(t *testing.T) {
	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(packaging, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	unit := read("systemd/lodge.service")
	for _, line := range []string{"User=lodge", "Group=lodge", "StateDirectory=lodge", "StateDirectoryMode=0700",
		"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes", "ReadWritePaths=/var/lib/lodge /run/lodge"} {
		if !strings.Contains(unit, "\n"+line+"\n") {
			t.Errorf("lodge.service lacks %s", line)
		}
	}
	if strings.Contains(unit, "\nRuntimeDirectory=") {
		t.Error("lodge.service sets RuntimeDirectory=, which removes /run/lodge/apps on stop")
	}
	tmp := read("tmpfiles.d/lodge.conf")
	for _, line := range []string{"d /run/lodge 0755 lodge lodge -", "d /run/lodge/apps 0755 root root -", "d /var/lib/lodge 0700 lodge lodge -"} {
		if !strings.Contains(tmp, line+"\n") {
			t.Errorf("tmpfiles.d lacks %q", line)
		}
	}
	users := read("sysusers.d/lodge.conf")
	for _, line := range []string{"u lodge - ", "g lodge-apps -"} {
		if !strings.Contains(users, "\n"+line) {
			t.Errorf("sysusers.d lacks %q", line)
		}
	}
	sock := read("app-template/example.socket")
	for _, line := range []string{"SocketGroup=lodge", "SocketMode=0660", "ListenStream=/run/lodge/apps/example.sock", "FileDescriptorName=example"} {
		if !strings.Contains(sock, "\n"+line+"\n") {
			t.Errorf("the app socket template lacks %s", line)
		}
	}
}

// systemd-analyze verify parses the units, where it is installed.
func TestUnitsVerify(t *testing.T) {
	analyze, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("no systemd-analyze")
	}
	dir := t.TempDir()
	for src, dst := range map[string]string{"systemd/lodge.service": "lodge.service", "app-template/example.socket": "example.socket",
		"app-template/example.service": "example.service"} {
		b, err := os.ReadFile(filepath.Join(packaging, src))
		if err != nil {
			t.Fatal(err)
		}
		// verify wants each ExecStart to exist.
		lines := strings.Split(string(b), "\n")
		for i, l := range lines {
			if strings.HasPrefix(l, "ExecStart=") {
				lines[i] = "ExecStart=/bin/sh -c true"
			}
		}
		if err := os.WriteFile(filepath.Join(dir, dst), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(analyze, "verify", "./lodge.service", "./example.socket", "./example.service")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("systemd-analyze verify: %v\n%s", err, out)
	}
}
