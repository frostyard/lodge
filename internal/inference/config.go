package inference

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
)

// Aliases are the aliases of contract 1.
var Aliases = []string{"local", "fast", "smart"}

// Provider types (docs/specs/inference.md, Configuration).
const (
	TypeOpenAI  = "openai"
	TypeCopilot = "copilot"
	TypeCodex   = "codex"
)

// ProviderConfig is one provider of the configuration.
type ProviderConfig struct {
	Type    string `json:"type"`
	BaseURL string `json:"baseURL,omitzero"`
	Slots   int    `json:"slots,omitzero"`
	Home    bool   `json:"home,omitzero"`
}

// AliasConfig maps an alias to a provider's model.
type AliasConfig struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Config is the inference member of lodge's configuration.
type Config struct {
	Providers map[string]ProviderConfig `json:"providers"`
	Aliases   map[string]AliasConfig    `json:"aliases"`
	// Apps narrows the aliases an app may use; an app absent here may use
	// every alias.
	Apps map[string][]string `json:"apps,omitzero"`
}

var (
	providerNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	modelRE        = regexp.MustCompile(`^[!-~]{1,256}$`)
)

// LoadConfig reads the inference member of the configuration file at path.
// A missing file, or one without the member, is an empty configuration.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var file struct {
		Inference jsontext.Value `json:"inference"`
	}
	if err := json.Unmarshal(b, &file); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(file.Inference) == 0 {
		return Config{}, nil
	}
	var c Config
	if err := json.Unmarshal(file.Inference, &c, json.RejectUnknownMembers(true)); err != nil {
		return Config{}, fmt.Errorf("%s: inference: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("%s: inference: %w", path, err)
	}
	return c, nil
}

// Validate checks every rule of the configuration.
func (c Config) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	for name, p := range c.Providers {
		if !providerNameRE.MatchString(name) {
			bad("provider name %q must match [a-z][a-z0-9-]{0,31}", name)
		}
		switch p.Type {
		case TypeOpenAI:
			u, err := url.Parse(p.BaseURL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || !strings.HasSuffix(u.Path, "/v1") ||
				u.RawQuery != "" || u.Fragment != "" || u.User != nil {
				bad("provider %s: baseURL %q must be an http or https URL ending in /v1", name, p.BaseURL)
			}
			if p.Slots < 1 || p.Slots > 64 {
				bad("provider %s: slots must be 1 to 64", name)
			}
		case TypeCopilot, TypeCodex:
		default:
			bad("provider %s: type %q is not openai, copilot or codex", name, p.Type)
		}
	}
	for alias, a := range c.Aliases {
		if !slices.Contains(Aliases, alias) {
			bad("alias %q is not one of %v", alias, Aliases)
		}
		p, ok := c.Providers[a.Provider]
		switch {
		case !ok:
			bad("alias %s: no provider %q", alias, a.Provider)
		case alias == "local" && !p.Home:
			bad("alias local must map to a provider on the owner's own hardware (\"home\": true), not %s", a.Provider)
		}
		if !modelRE.MatchString(a.Model) {
			bad("alias %s: model must be 1 to 256 visible ASCII characters", alias)
		}
	}
	for app, list := range c.Apps {
		for _, alias := range list {
			if _, ok := c.Aliases[alias]; !ok {
				bad("apps.%s: no alias %q", app, alias)
			}
		}
	}
	return errors.Join(errs...)
}

// Allowed reports whether app may use alias.
func (c Config) Allowed(app, alias string) bool {
	if _, ok := c.Aliases[alias]; !ok {
		return false
	}
	list, narrowed := c.Apps[app]
	return !narrowed || slices.Contains(list, alias)
}
