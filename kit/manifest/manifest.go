// Package manifest is an app's app.json, which lodge reads from
// /usr/share/lodge/apps/<id>/ (app-contract.md, The manifest). lodge loads
// manifests with it, and apps can check the one they install.
package manifest

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Contract is the contract major this kit speaks.
const Contract = 1

// Reserved are the ids no app may take: they are lodge's own first path
// segments.
var reserved = []string{"app", "device", "gateway", "lodge", "mcp", "nats", "oauth"}

var (
	idPattern     = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	userPattern   = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	keyPattern    = regexp.MustCompile(`^[a-z]$`)
	hearthPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}\.json$`)
)

// Manifest is app.json. A missing bool is false.
type Manifest struct {
	// Contract is the contract major the app speaks.
	Contract int `json:"contract"`
	// ID is the app's URL segment, and its directory's name.
	ID string `json:"id"`
	// Name is shown in the rail and the switcher.
	Name string `json:"name"`
	// Icon is a relative path to an SVG file inside web/.
	Icon string `json:"icon"`
	// Version is the app's version.
	Version string `json:"version"`
	// User is the system user the daemon runs as.
	User string `json:"user"`
	// Key, when set, opens the app after the switcher's shortcut.
	Key string `json:"key,omitzero"`
	// Content: the app serves untrusted bytes through lodge-content.
	Content bool `json:"content,omitzero"`
	// ContentLinks: the app's content may open links in a new window, so
	// lodge's content policy adds allow-popups and
	// allow-popups-to-escape-sandbox to its sandbox.
	ContentLinks bool `json:"contentLinks,omitzero"`
	// Device: the app has routes it authenticates itself.
	Device bool `json:"device,omitzero"`
	// MCP: the app serves MCP.
	MCP bool `json:"mcp,omitzero"`
	// RemoteImages: the app's frame may load any https: image.
	RemoteImages bool `json:"remoteImages,omitzero"`
	// Companion: the app is the companion, opened in a panel beside the
	// current app and told its context (ADR-0013). lodge makes at most one
	// app the companion: when two manifests set it, neither is.
	Companion bool `json:"companion,omitzero"`
	// Hearth is the file name of the app's hearth manifest in its
	// directory: the capabilities and selectors the consent page shows
	// (docs/specs/oauth.md, The app's manifest).
	Hearth string `json:"hearth,omitzero"`
	// Tools is the file name of the app's tool catalog in its directory: 1
	// MCP tool for each agent operation of the hearth manifest
	// (ADR-0024).
	Tools string `json:"tools,omitzero"`
}

// ValidID reports whether id may name an app: ^[a-z][a-z0-9]{0,15}$, and
// not app, device, gateway, lodge, mcp, nats or oauth.
func ValidID(id string) bool {
	return idPattern.MatchString(id) && !slices.Contains(reserved, id)
}

// Parse decodes app.json and validates it. Fields it does not know are
// ignored, so a minor addition does not break an older lodge.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m, json.RejectUnknownMembers(false)); err != nil {
		return Manifest{}, fmt.Errorf("app.json: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// Validate checks every field's constraint. It does not check the contract
// major, which lodge compares with the majors it speaks.
func (m Manifest) Validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if m.Contract < 1 {
		bad("contract %d is not a contract major", m.Contract)
	}
	if !ValidID(m.ID) {
		bad("id %q must match ^[a-z][a-z0-9]{0,15}$ and not be app, device, gateway, lodge, mcp, nats or oauth", m.ID)
	}
	if n := utf8.RuneCountInString(m.Name); n < 1 || n > 32 || strings.TrimSpace(m.Name) == "" {
		bad("name must be 1 to 32 characters")
	}
	if err := checkIcon(m.Icon); err != nil {
		errs = append(errs, err)
	}
	if !visibleASCII(m.Version, 64) {
		bad("version must be 1 to 64 visible ASCII characters")
	}
	if !userPattern.MatchString(m.User) || m.User == "root" || m.User == "lodge" {
		bad("user %q must match ^[a-z_][a-z0-9_-]{0,31}$ and be neither root nor lodge", m.User)
	}
	if m.Key != "" && !keyPattern.MatchString(m.Key) {
		bad("key %q must be one letter, a to z", m.Key)
	}
	if m.Hearth != "" && !ValidHearthFile(m.Hearth) {
		bad("hearth %q must match ^[a-z0-9][a-z0-9._-]{0,63}\\.json$ and not be app.json", m.Hearth)
	}
	if m.Tools != "" {
		switch {
		case !ValidHearthFile(m.Tools):
			bad("tools %q must match ^[a-z0-9][a-z0-9._-]{0,63}\\.json$ and not be app.json", m.Tools)
		case m.Tools == m.Hearth:
			bad("tools %q is its hearth manifest's file", m.Tools)
		}
		if m.Hearth == "" {
			bad("tools needs hearth")
		}
	}
	return errors.Join(errs...)
}

// ValidHearthFile reports whether name may name a file beside app.json in
// an app's directory: its hearth manifest, or its tool catalog. It is a
// .json name that is not app.json itself.
func ValidHearthFile(name string) bool {
	return hearthPattern.MatchString(name) && name != "app.json"
}

// checkIcon admits a relative, clean path to an .svg file.
func checkIcon(icon string) error {
	switch {
	case icon == "":
		return errors.New("icon is required")
	case strings.HasPrefix(icon, "/") || strings.Contains(icon, `\`) ||
		path.Clean(icon) != icon || icon == ".." || strings.HasPrefix(icon, "../"):
		return fmt.Errorf("icon %q must be a relative path inside web/", icon)
	case !strings.HasSuffix(strings.ToLower(icon), ".svg"):
		return fmt.Errorf("icon %q must be an SVG file", icon)
	}
	return nil
}

func visibleASCII(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := range len(s) {
		if s[i] < '!' || s[i] > '~' {
			return false
		}
	}
	return true
}

// Load reads and validates an installed app's directory, such as
// /usr/share/lodge/apps/notes: its app.json, whose id must equal the
// directory's name, and its web/index.html and icon, which must be regular
// files. lodge disables an app whose Load fails.
func Load(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, "app.json"))
	if err != nil {
		return Manifest{}, err
	}
	m, err := Parse(data)
	if err != nil {
		return Manifest{}, err
	}
	if base := filepath.Base(dir); m.ID != base {
		return Manifest{}, fmt.Errorf("id %q is not its directory's name, %q", m.ID, base)
	}
	web := filepath.Join(dir, "web")
	for _, f := range []string{"index.html", m.Icon} {
		fi, err := os.Lstat(filepath.Join(web, filepath.FromSlash(f)))
		if err != nil {
			return Manifest{}, fmt.Errorf("web/%s: %w", f, err)
		}
		if !fi.Mode().IsRegular() {
			return Manifest{}, fmt.Errorf("web/%s: %w", f, fs.ErrInvalid)
		}
	}
	return m, nil
}
