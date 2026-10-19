package repocheck

import (
	"bytes"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	// A concrete tailnet or device name. Placeholders such as
	// <tailnet>.ts.net and lodge.<tailnet>.ts.net do not match.
	tailnetName = regexp.MustCompile(`(?i)\b[a-z0-9-]+\.ts\.net\b`)
	// A host name on a home network.
	lanName = regexp.MustCompile(`(?i)\b[a-z0-9-]+\.(?:local|lan|home\.arpa|internal)\b`)
	ipv4    = regexp.MustCompile(`\b\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}\b`)
	// Addresses safe to write down: loopback, unspecified, and RFC 5737's
	// documentation ranges.
	publicExamples = []netip.Prefix{
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
	}
)

func exampleAddress(s string) bool {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return true // 999.1.1.1 is no address
	}
	if a.IsLoopback() || a.IsUnspecified() {
		return true
	}
	for _, p := range publicExamples {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// The repository names none of the owner's hosts: no tailnet or device
// names, no home-network names and no addresses but loopback and the
// documentation ranges (AGENTS.md, Repository boundary). Docs write
// LODGE, CONTENT, <tailnet>.ts.net and MODEL_HOST instead.
func TestNoPrivateHosts(t *testing.T) {
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || d.Name() == "go.sum" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			return nil // binary
		}
		files++
		for i, line := range strings.Split(string(data), "\n") {
			where := path + ":" + strconv.Itoa(i+1)
			for _, m := range tailnetName.FindAllString(line, -1) {
				t.Errorf("%s: %q names a tailnet host; write <tailnet>.ts.net, LODGE or CONTENT", where, m)
			}
			for _, m := range lanName.FindAllString(line, -1) {
				t.Errorf("%s: %q names a home-network host; write a placeholder such as MODEL_HOST", where, m)
			}
			for _, m := range ipv4.FindAllString(line, -1) {
				if !exampleAddress(m) {
					t.Errorf("%s: %s is a real address; use loopback or a documentation range (192.0.2.0/24)", where, m)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("no files checked")
	}
}

func TestBoundaryPatterns(t *testing.T) {
	for line, want := range map[string]bool{
		"https://lodge.<tailnet>.ts.net/notes":                             false,
		"`*.<tailnet>.ts.net` shares one site":                             false,
		"ts.net is on the Public Suffix List":                              false,
		"https://lodge." + "tail0000" + ".ts.net/":                         true,
		"http://MODEL_HOST/v1":                                             false,
		"http://" + "model" + ".local:8080/v1":                             true,
		"~/.local/share/mise":                                              false,
		"127.0.0.1:8443 and 192.0.2.10":                                    false,
		"http://" + strings.Join([]string{"10", "0", "0", "7"}, ".") + "/": true,
		"golangci-lint 2.13.1 and go1.27.1":                                false,
	} {
		got := tailnetName.MatchString(line) || lanName.MatchString(line)
		for _, m := range ipv4.FindAllString(line, -1) {
			got = got || !exampleAddress(m)
		}
		if got != want {
			t.Errorf("flagged(%q) = %v, want %v", line, got, want)
		}
	}
}
