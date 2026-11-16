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

// sdkImportAllowed reports whether a Go file at rel may import path: only
// internal/mcpgw imports the Go MCP SDK, and only in a file that is not a test,
// so that the rest of lodge depends on no MCP server's types
// (docs/specs/lodge-http.md, Rules; ADR-0024).
func sdkImportAllowed(rel, path string) bool {
	if path != "github.com/modelcontextprotocol/go-sdk" && !strings.HasPrefix(path, "github.com/modelcontextprotocol/go-sdk/") {
		return true
	}
	rel = filepath.ToSlash(rel)
	if strings.HasSuffix(rel, "_test.go") {
		// A test of the gateway proves what a session sees with an MCP
		// client, and the gateway itself holds no SDK type.
		return true
	}
	return strings.HasPrefix(rel, "internal/mcpgw/")
}

// hearthImportAllowed reports whether a Go file at rel may import path: only
// the copy of hearth's example app, which lodge's tests and its development
// build run, imports hearth's code. lodge reads hearth's files itself, and
// depends on none of its packages (ADR-0024; internal/hearthtest/notelite).
func hearthImportAllowed(rel, path string) bool {
	if path != "github.com/frostyard/hearth" && !strings.HasPrefix(path, "github.com/frostyard/hearth/") {
		return true
	}
	rel = filepath.ToSlash(rel)
	return strings.HasPrefix(rel, "internal/hearthtest/") || strings.HasPrefix(rel, "dev/hearthapp/")
}

func TestOnlyMCPGWImportsTheMCPSDK(t *testing.T) {
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
			if !sdkImportAllowed(rel, p) {
				t.Errorf("%s imports %s; only internal/mcpgw may import github.com/modelcontextprotocol/go-sdk", filepath.ToSlash(rel), p)
			}
			if !strings.HasSuffix(filepath.ToSlash(rel), "_test.go") && strings.HasPrefix(p, "github.com/modelcontextprotocol/go-sdk") {
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
		t.Error("internal/mcpgw imports no github.com/modelcontextprotocol/go-sdk, so this rule checks nothing")
	}
}

func TestOnlyTheHearthExampleImportsHearth(t *testing.T) {
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
			if !hearthImportAllowed(rel, p) {
				t.Errorf("%s imports %s; only internal/hearthtest and dev/hearthapp may import github.com/frostyard/hearth",
					filepath.ToSlash(rel), p)
			}
			if hearthImportAllowed(rel, p) && strings.HasPrefix(p, "github.com/frostyard/hearth") {
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
		t.Error("internal/hearthtest imports no github.com/frostyard/hearth, so this rule checks nothing")
	}
}

func TestImportRules(t *testing.T) {
	for _, c := range []struct {
		rel, path string
		sdk, hear bool
	}{
		{"internal/mcpgw/mcpgw.go", "github.com/modelcontextprotocol/go-sdk/mcp", true, true},
		{"internal/mcpgw/mcpgw.go", "github.com/modelcontextprotocol/go-sdk/auth", true, true},
		{"internal/gateway/connect.go", "github.com/modelcontextprotocol/go-sdk/mcp", false, true},
		{"internal/gateway/gateway.go", "github.com/modelcontextprotocol/go-sdk/auth", false, true},
		{"cmd/lodge/main.go", "github.com/modelcontextprotocol/go-sdk/mcp", false, true},
		{"internal/mcpgw/mcpgw.go", "github.com/modelcontextprotocol/go-sdkx/mcp", true, true},
		{"internal/mcpgw/mcpgw.go", "github.com/frostyard/hearth/authz", true, false},
		{"internal/hearthtest/hearthtest.go", "github.com/frostyard/hearth/lodge", true, true},
		{"internal/hearthtest/notelite/notesv1/notes.pb.go", "github.com/frostyard/hearth/gen/go/frostyard/hearth/v1", true, true},
		{"dev/hearthapp/main.go", "github.com/frostyard/hearth/authz", true, true},
		{"internal/gateway/connect.go", "github.com/frostyard/hearth/authz", true, false},
		{"internal/hearthtest/hearthtest.go", "github.com/frostyard/hearthx/mcpserver", true, true},
		{"internal/mcpgw/mcpgw.go", "net/http", true, true},
	} {
		if got := sdkImportAllowed(c.rel, c.path); got != c.sdk {
			t.Errorf("sdkImportAllowed(%q, %q) = %v, want %v", c.rel, c.path, got, c.sdk)
		}
		if got := hearthImportAllowed(c.rel, c.path); got != c.hear {
			t.Errorf("hearthImportAllowed(%q, %q) = %v, want %v", c.rel, c.path, got, c.hear)
		}
	}
}
