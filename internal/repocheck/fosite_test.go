package repocheck

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fositeImportAllowed reports whether a Go file at rel may import path:
// only internal/oauth imports github.com/ory/fosite, in any subpackage, so
// that the rest of lodge depends on no authorization server's types.
func fositeImportAllowed(rel, path string) bool {
	if path != "github.com/ory/fosite" && !strings.HasPrefix(path, "github.com/ory/fosite/") {
		return true
	}
	return strings.HasPrefix(filepath.ToSlash(rel), "internal/oauth/")
}

func TestOnlyOAuthImportsFosite(t *testing.T) {
	fset := token.NewFileSet()
	files, allowed := 0, 0
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
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
			if !fositeImportAllowed(rel, p) {
				t.Errorf("%s imports %s; only internal/oauth may import github.com/ory/fosite", filepath.ToSlash(rel), p)
			}
			if fositeImportAllowed(rel, p) && p != "" && strings.HasPrefix(p, "github.com/ory/fosite") {
				allowed++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files == 0 {
		t.Fatal("found no Go files")
	}
	if allowed == 0 {
		t.Error("internal/oauth imports no github.com/ory/fosite, so this rule checks nothing")
	}
}

func TestFositeImportRule(t *testing.T) {
	for _, c := range []struct {
		rel, path string
		want      bool
	}{
		{"internal/oauth/oauth.go", "github.com/ory/fosite", true},
		{"internal/oauth/oauth.go", "github.com/ory/fosite/handler/oauth2", true},
		{"internal/oauth/zoom_test.go", "github.com/ory/fosite/compose", true},
		{"internal/gateway/oauth.go", "github.com/ory/fosite", false},
		{"internal/gateway/oauth.go", "github.com/ory/fosite/compose", false},
		{"cmd/lodge/main.go", "github.com/ory/fosite", false},
		{"internal/oauthx/oauth.go", "github.com/ory/fosite", false},
		{"internal/oauth/oauth.go", "github.com/ory/x/errorsx", true},
		{"internal/oauth/oauth.go", "net/http", true},
	} {
		if got := fositeImportAllowed(c.rel, c.path); got != c.want {
			t.Errorf("fositeImportAllowed(%q, %q) = %v, want %v", c.rel, c.path, got, c.want)
		}
	}
}
