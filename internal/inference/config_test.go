package inference

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const validConfig = `{"other": 1, "inference": {
  "providers": {
    "home": {"type": "openai", "baseURL": "http://192.0.2.10:8080/v1", "slots": 4, "home": true},
    "cloud": {"type": "openai", "baseURL": "https://api.example.com/v1", "slots": 8},
    "copilot": {"type": "copilot", "slots": 4}
  },
  "aliases": {
    "local": {"provider": "home", "model": "MODEL"},
    "fast": {"provider": "home", "model": "MODEL"},
    "smart": {"provider": "cloud", "model": "big-model"}
  },
  "apps": {"mail": ["local", "fast"]}
}}`

func TestLoadConfig(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 3 || c.Aliases["smart"].Model != "big-model" {
		t.Errorf("config = %+v", c)
	}
	for _, empty := range []string{filepath.Join(t.TempDir(), "missing.json"), writeConfig(t, `{"other": 1}`)} {
		c, err := LoadConfig(empty)
		if err != nil || len(c.Aliases) != 0 {
			t.Errorf("%s: %+v %v, want an empty configuration", empty, c, err)
		}
	}
}

func TestConfigRules(t *testing.T) {
	for name, c := range map[string]struct {
		body, want string
	}{
		"local off home": {`{"inference": {"providers": {"cloud": {"type": "openai", "baseURL": "https://api.example.com/v1", "slots": 1}},
			"aliases": {"local": {"provider": "cloud", "model": "m"}}}}`, "own hardware"},
		"unknown alias":    {`{"inference": {"providers": {}, "aliases": {"huge": {"provider": "x", "model": "m"}}}}`, "is not one of"},
		"unknown provider": {`{"inference": {"providers": {}, "aliases": {"fast": {"provider": "x", "model": "m"}}}}`, "no provider"},
		"bad type":         {`{"inference": {"providers": {"x": {"type": "ollama"}}, "aliases": {}}}`, "is not openai"},
		"no /v1":           {`{"inference": {"providers": {"x": {"type": "openai", "baseURL": "http://192.0.2.1:8080", "slots": 1}}, "aliases": {}}}`, "ending in /v1"},
		"ftp":              {`{"inference": {"providers": {"x": {"type": "openai", "baseURL": "ftp://192.0.2.1/v1", "slots": 1}}, "aliases": {}}}`, "ending in /v1"},
		"no slots":         {`{"inference": {"providers": {"x": {"type": "openai", "baseURL": "http://192.0.2.1/v1"}}, "aliases": {}}}`, "slots"},
		"65 slots":         {`{"inference": {"providers": {"x": {"type": "openai", "baseURL": "http://192.0.2.1/v1", "slots": 65}}, "aliases": {}}}`, "slots"},
		"bad name":         {`{"inference": {"providers": {"X y": {"type": "copilot"}}, "aliases": {}}}`, "provider name"},
		"bad model":        {`{"inference": {"providers": {"x": {"type": "copilot"}}, "aliases": {"fast": {"provider": "x", "model": "a b"}}}}`, "model"},
		"app alias":        {`{"inference": {"providers": {}, "aliases": {}, "apps": {"mail": ["fast"]}}}`, "no alias"},
		"unknown member":   {`{"inference": {"providers": {}, "aliases": {}, "keys": {}}}`, "unknown"},
		"a key in config":  {`{"inference": {"providers": {"x": {"type": "copilot", "key": "sk-1"}}, "aliases": {}}}`, "unknown"},
		"not json":         {`{`, "config.json"},
		"copilot at home":  {`{"inference": {"providers": {"x": {"type": "copilot", "slots": 1, "home": true}}, "aliases": {}}}`, "not on the owner's own hardware"},
		"codex at home":    {`{"inference": {"providers": {"x": {"type": "codex", "slots": 1, "home": true}}, "aliases": {}}}`, "not on the owner's own hardware"},
		"copilot no slots": {`{"inference": {"providers": {"x": {"type": "copilot"}}, "aliases": {}}}`, "slots"},
		"copilot slash":    {`{"inference": {"providers": {"x": {"type": "copilot", "baseURL": "https://api.example.com/", "slots": 1}}, "aliases": {}}}`, "trailing slash"},
	} {
		_, err := LoadConfig(writeConfig(t, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error with %q", name, err, c.want)
		}
	}
	// A copilot provider's baseURL needs no /v1.
	if _, err := LoadConfig(writeConfig(t, `{"inference": {"providers": {"x": {"type": "copilot", "baseURL": "https://api.example.com", "slots": 2}}, "aliases": {}}}`)); err != nil {
		t.Errorf("a copilot baseURL: %v", err)
	}
	// At the limits: 1 and 64 slots.
	for _, slots := range []string{"1", "64"} {
		body := `{"inference": {"providers": {"x": {"type": "openai", "baseURL": "http://192.0.2.1/v1", "slots": ` + slots + `}}, "aliases": {}}}`
		if _, err := LoadConfig(writeConfig(t, body)); err != nil {
			t.Errorf("%s slots: %v", slots, err)
		}
	}
}

func TestAllowed(t *testing.T) {
	c, err := LoadConfig(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []struct {
		app, alias string
		want       bool
	}{
		{"mail", "local", true}, {"mail", "fast", true}, {"mail", "smart", false},
		{"notes", "smart", true}, {"notes", "local", true}, {"notes", "huge", false},
	} {
		if got := c.Allowed(x.app, x.alias); got != x.want {
			t.Errorf("Allowed(%s, %s) = %v", x.app, x.alias, got)
		}
	}
}
