package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const notes = `{ "contract": 1, "id": "notes", "name": "Notes", "icon": "icon.svg",
  "version": "0.5.0", "user": "frostnotes", "key": "n",
  "content": true, "device": true, "mcp": true, "remoteImages": true,
  "someday": {"a": 1} }`

func TestParseTheContractsExample(t *testing.T) {
	m, err := Parse([]byte(notes))
	if err != nil {
		t.Fatal(err)
	}
	want := Manifest{Contract: 1, ID: "notes", Name: "Notes", Icon: "icon.svg", Version: "0.5.0",
		User: "frostnotes", Key: "n", Content: true, Device: true, MCP: true, RemoteImages: true}
	if m != want {
		t.Errorf("Parse = %+v, want %+v", m, want)
	}
	m, err = Parse([]byte(`{"contract":1,"id":"mail","name":"Mail","icon":"i/mail.svg","version":"1","user":"frostmail","content":true,"contentLinks":true}`))
	if err != nil || !m.ContentLinks || m.Key != "" || m.Companion {
		t.Errorf("Parse with contentLinks = %+v, %v", m, err)
	}
}

func TestValidateRefusesEachConstraint(t *testing.T) {
	ok := Manifest{Contract: 1, ID: "notes", Name: "Notes", Icon: "icon.svg", Version: "0.5.0", User: "frostnotes"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*Manifest){
		"contract 0":     func(m *Manifest) { m.Contract = 0 },
		"id uppercase":   func(m *Manifest) { m.ID = "Notes" },
		"id too long":    func(m *Manifest) { m.ID = "a234567890123456x" },
		"id digit first": func(m *Manifest) { m.ID = "1notes" },
		"id reserved":    func(m *Manifest) { m.ID = "lodge" },
		"id app":         func(m *Manifest) { m.ID = "app" },
		"no name":        func(m *Manifest) { m.Name = "" },
		"blank name":     func(m *Manifest) { m.Name = "  " },
		"long name":      func(m *Manifest) { m.Name = strings.Repeat("n", 33) },
		"no icon":        func(m *Manifest) { m.Icon = "" },
		"absolute icon":  func(m *Manifest) { m.Icon = "/etc/icon.svg" },
		"escaping icon":  func(m *Manifest) { m.Icon = "../icon.svg" },
		"unclean icon":   func(m *Manifest) { m.Icon = "a/../icon.svg" },
		"png icon":       func(m *Manifest) { m.Icon = "icon.png" },
		"no version":     func(m *Manifest) { m.Version = "" },
		"spaced version": func(m *Manifest) { m.Version = "1 2" },
		"long version":   func(m *Manifest) { m.Version = strings.Repeat("1", 65) },
		"root user":      func(m *Manifest) { m.User = "root" },
		"lodge user":     func(m *Manifest) { m.User = "lodge" },
		"bad user":       func(m *Manifest) { m.User = "Frost" },
		"two-letter key": func(m *Manifest) { m.Key = "nn" },
		"uppercase key":  func(m *Manifest) { m.Key = "N" },
	} {
		m := ok
		edit(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: %+v validated", name, m)
		}
	}
	for _, bad := range []string{`{`, `[]`, `{"contract":"1"}`, `{"id":"a","id":"b"}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%s): no error", bad)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "notes")
	write := func(name, data string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("app.json", notes)
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "index.html") {
		t.Errorf("Load without web/index.html = %v", err)
	}
	write("web/index.html", "<!doctype html>")
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "icon.svg") {
		t.Errorf("Load without the icon = %v", err)
	}
	write("web/icon.svg", "<svg/>")
	if m, err := Load(dir); err != nil || m.ID != "notes" {
		t.Errorf("Load = %+v, %v", m, err)
	}
	other := filepath.Join(filepath.Dir(dir), "mail")
	if err := os.Rename(dir, other); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(other); err == nil || !strings.Contains(err.Error(), "directory's name") {
		t.Errorf("Load from another directory = %v", err)
	}
}
