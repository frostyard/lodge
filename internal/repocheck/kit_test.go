package repocheck

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// kitImportAllowed reports whether a file of kit/ may import path: never
// tailscale.com, and nothing of lodge's outside the kit module, so that
// apps importing the kit pull in neither Tailscale nor lodge's internals.
func kitImportAllowed(path string) bool {
	switch {
	case path == "tailscale.com" || strings.HasPrefix(path, "tailscale.com/"):
		return false
	case path == "github.com/frostyard/lodge/kit" || strings.HasPrefix(path, "github.com/frostyard/lodge/kit/"):
		return true
	case path == "github.com/frostyard/lodge" || strings.HasPrefix(path, "github.com/frostyard/lodge/"):
		return false
	}
	return true
}

func TestKitImportsNeitherTailscaleNorLodge(t *testing.T) {
	kit := filepath.Join(root, "kit")
	fset := token.NewFileSet()
	files := 0
	err := filepath.WalkDir(kit, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		files++
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if !kitImportAllowed(p) {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s imports %s; kit/ never imports tailscale.com or lodge outside kit/", rel, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("found no Go files under kit/")
	}
	mod, err := os.ReadFile(filepath.Join(kit, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mod), "tailscale.com") {
		t.Error("kit/go.mod requires tailscale.com")
	}
}

func TestKitImportRule(t *testing.T) {
	for path, want := range map[string]bool{
		"tailscale.com":       false,
		"tailscale.com/tsnet": false,
		"github.com/frostyard/lodge/internal/gateway": false,
		"github.com/frostyard/lodge":                  false,
		"github.com/frostyard/lodgeish":               true,
		"github.com/frostyard/lodge/kit":              true,
		"github.com/frostyard/lodge/kit/lodgeapp":     true,
		"github.com/coder/websocket":                  true,
		"net/http":                                    true,
	} {
		if got := kitImportAllowed(path); got != want {
			t.Errorf("kitImportAllowed(%q) = %v, want %v", path, got, want)
		}
	}
}
