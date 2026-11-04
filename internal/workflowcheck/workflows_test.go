package workflowcheck

import (
	"bufio"
	"go/build/constraint"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var (
	commitAction = regexp.MustCompile(`^[^@]+@[0-9a-f]{40}$`)
	dockerAction = regexp.MustCompile(`^docker://[^@]+@sha256:[0-9a-f]{64}$`)
)

// immutable reports whether an action reference names one version for
// good: a local action, a commit, or an image digest.
func immutable(ref string) bool {
	return strings.HasPrefix(ref, "./") || commitAction.MatchString(ref) || dockerAction.MatchString(ref)
}

func TestPinnedReferencesAreImmutable(t *testing.T) {
	sha, digest := strings.Repeat("a", 40), strings.Repeat("b", 64)
	for ref, want := range map[string]bool{
		"actions/checkout@" + sha:                   true,
		"owner/repo/.github/actions/publish@" + sha: true,
		"docker://alpine@sha256:" + digest:          true,
		"./.github/actions/check":                   true,
		"actions/checkout@v7":                       false,
		"owner/repo/.github/actions/publish@main":   false,
		"actions/checkout@deadbeef":                 false,
		"actions/checkout@${{inputs.ref}}":          false,
		"docker://alpine:3.22":                      false,
		"actions/checkout@" + strings.ToUpper(sha):  false,
	} {
		if got := immutable(ref); got != want {
			t.Errorf("immutable(%q) = %v, want %v", ref, got, want)
		}
	}
}

// workflows parses every workflow in .github/workflows.
func workflows(t *testing.T) map[string]*yaml.Node {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.y*ml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no workflows found")
	}
	out := map[string]*yaml.Node{}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var doc yaml.Node
		if err := yaml.Unmarshal(data, &doc); err != nil || len(doc.Content) == 0 {
			t.Fatalf("parse %s: %v", p, err)
		}
		out[filepath.Base(p)] = doc.Content[0]
	}
	return out
}

// get returns the value of key in a mapping node, or nil.
func get(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// walk calls f for every mapping node under n.
func walk(n *yaml.Node, f func(*yaml.Node)) {
	if n.Kind == yaml.MappingNode {
		f(n)
	}
	for _, c := range n.Content {
		walk(c, f)
	}
}

// offLinuxTests returns test files selected for Windows but not Linux.
func offLinuxTests(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "node_modules", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		scan := bufio.NewScanner(f)
		for scan.Scan() {
			line := strings.TrimSpace(scan.Text())
			if !strings.HasPrefix(line, "//go:build ") {
				continue
			}
			expr, err := constraint.Parse(line)
			if err != nil {
				return err
			}
			if !builds(expr, "linux") && builds(expr, "windows") {
				out = append(out, filepath.Clean(path))
			}
			break
		}
		if err := scan.Err(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// builds reports whether a file with the build constraint expr builds for
// goos. Only the system's tags count; any other tag is unset.
func builds(expr constraint.Expr, goos string) bool {
	return expr.Eval(func(tag string) bool {
		return tag == goos || (tag == "unix" && goos != "windows")
	})
}

// runsGoTestOnWindows reports whether a Windows job tests dir.
func runsGoTestOnWindows(root *yaml.Node, dir string) bool {
	jobs := get(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		job := jobs.Content[i+1]
		runsOn := get(job, "runs-on")
		if runsOn == nil || runsOn.Kind != yaml.ScalarNode || !strings.HasPrefix(runsOn.Value, "windows") {
			continue
		}
		steps := get(job, "steps")
		if steps == nil || steps.Kind != yaml.SequenceNode {
			continue
		}
		for _, step := range steps.Content {
			run := get(step, "run")
			if run == nil || run.Kind != yaml.ScalarNode || !strings.Contains(run.Value, "go test") {
				continue
			}
			working := get(step, "working-directory")
			if working != nil && working.Kind == yaml.ScalarNode && working.Value == dir {
				return true
			}
			if strings.Contains(run.Value, "cd "+dir) {
				return true
			}
		}
	}
	return false
}

// The kit's !linux tests (frostmail runs the kit on Windows) compile and
// run only in a Windows job: a Linux runner never builds them.
func TestOffLinuxTestsRunInCI(t *testing.T) {
	files := offLinuxTests(t, "../../kit")
	if len(files) == 0 {
		t.Fatal("the kit has no off-Linux tests, so remove this check if that is now true")
	}
	for _, root := range workflows(t) {
		if runsGoTestOnWindows(root, "kit") {
			return
		}
	}
	t.Errorf("off-Linux kit tests %v never run in CI: add a Windows job that runs go test ./... with working-directory kit", files)
}

func TestRunsGoTestOnWindows(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{
			name: "Windows kit working directory",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test ./...\n",
			want: true,
		},
		{
			name: "Ubuntu kit working directory",
			yaml: "jobs:\n  kit-linux:\n    runs-on: ubuntu-24.04\n    steps:\n      - working-directory: kit\n        run: go test ./...\n",
			want: false,
		},
		{
			name: "Windows vet only",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go vet ./...\n",
			want: false,
		},
		{
			name: "Windows test without kit directory",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - run: go test ./...\n",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var doc yaml.Node
			if err := yaml.Unmarshal([]byte(tt.yaml), &doc); err != nil {
				t.Fatal(err)
			}
			if got := runsGoTestOnWindows(doc.Content[0], "kit"); got != tt.want {
				t.Errorf("runsGoTestOnWindows() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Every external action is pinned to a full commit SHA (the version a
// trailing comment names is for people; the SHA is what runs), every
// workflow starts from declared permissions, and every checkout drops the
// token it would otherwise leave in .git/config.
func TestWorkflowsFollowADR0021(t *testing.T) {
	uses := 0
	for name, root := range workflows(t) {
		if get(root, "permissions") == nil {
			t.Errorf("%s: no top-level permissions (start from permissions: {})", name)
		}
		walk(root, func(m *yaml.Node) {
			ref := get(m, "uses")
			if ref == nil {
				return
			}
			uses++
			if ref.Kind != yaml.ScalarNode || !immutable(ref.Value) {
				t.Errorf("%s:%d: %q must name a full commit SHA or image digest", name, ref.Line, ref.Value)
			}
			if strings.HasPrefix(ref.Value, "actions/checkout@") {
				if pc := get(get(m, "with"), "persist-credentials"); pc == nil || pc.Value != "false" {
					t.Errorf("%s:%d: checkout must set persist-credentials: false", name, ref.Line)
				}
			}
		})
	}
	if uses == 0 {
		t.Fatal("no action references found")
	}
}
