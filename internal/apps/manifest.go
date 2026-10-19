package apps

import (
	"encoding/json/v2"
	"fmt"
	"regexp"
	"slices"
	"unicode/utf8"
)

// Contracts are the contract majors this lodge speaks.
var Contracts = []int{1}

// Manifest is an app's app.json (docs/specs/app-contract.md, The manifest).
// Fields lodge does not know are ignored.
type Manifest struct {
	Contract     int    `json:"contract"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	Icon         string `json:"icon"`
	Version      string `json:"version"`
	User         string `json:"user"`
	Key          string `json:"key,omitempty"`
	Content      bool   `json:"content,omitempty"`
	ContentLinks bool   `json:"contentLinks,omitempty"`
	Device       bool   `json:"device,omitempty"`
	MCP          bool   `json:"mcp,omitempty"`
	RemoteImages bool   `json:"remoteImages,omitempty"`
	Companion    bool   `json:"companion,omitempty"`
}

// Reserved are the first path segments no app may take as its ID.
var Reserved = []string{"app", "device", "lodge", "mcp"}

var (
	idRE      = regexp.MustCompile(`^[a-z][a-z0-9]{0,15}$`)
	userRE    = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	keyRE     = regexp.MustCompile(`^[a-z]$`)
	versionRE = regexp.MustCompile(`^[!-~]{1,64}$`)
	// A relative path whose every segment is a plain name.
	relPathRE = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$`)
)

// ValidID reports whether id has an app ID's form and is not reserved.
func ValidID(id string) bool {
	return idRE.MatchString(id) && !slices.Contains(Reserved, id)
}

// ParseManifest parses and checks one app.json, found in directory dir.
func ParseManifest(b []byte, dir string) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("app.json does not parse: %w", err)
	}
	if !slices.Contains(Contracts, m.Contract) {
		return m, fmt.Errorf("contract %d is not one this lodge speaks (%v)", m.Contract, Contracts)
	}
	switch {
	case !idRE.MatchString(m.ID):
		return m, fmt.Errorf("id %q is not of the form [a-z][a-z0-9]{0,15}", m.ID)
	case slices.Contains(Reserved, m.ID):
		return m, fmt.Errorf("id %q is reserved", m.ID)
	case m.ID != dir:
		return m, fmt.Errorf("id %q is not its directory's name %q", m.ID, dir)
	case m.Name == "" || utf8.RuneCountInString(m.Name) > 32 || !utf8.ValidString(m.Name):
		return m, fmt.Errorf("name must be 1 to 32 characters")
	case !relPathRE.MatchString(m.Icon) || len(m.Icon) < 5 || m.Icon[len(m.Icon)-4:] != ".svg":
		return m, fmt.Errorf("icon %q is not a relative path to an SVG file", m.Icon)
	case !versionRE.MatchString(m.Version):
		return m, fmt.Errorf("version must be 1 to 64 visible ASCII characters")
	case !userRE.MatchString(m.User) || m.User == "root" || m.User == "lodge":
		return m, fmt.Errorf("user %q is not an app's system user", m.User)
	case m.Key != "" && !keyRE.MatchString(m.Key):
		return m, fmt.Errorf("key %q is not one letter a to z", m.Key)
	}
	return m, nil
}
