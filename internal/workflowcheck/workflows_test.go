package workflowcheck

import (
	"go/build"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

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
	linux := build.Default
	linux.GOOS = "linux"
	linux.GOARCH = "amd64"
	linux.CgoEnabled = false
	windows := build.Default
	windows.GOOS = "windows"
	windows.GOARCH = "amd64"
	windows.CgoEnabled = false

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
		linuxMatch, err := linux.MatchFile(filepath.Dir(path), d.Name())
		if err != nil {
			return err
		}
		windowsMatch, err := windows.MatchFile(filepath.Dir(path), d.Name())
		if err != nil {
			return err
		}
		if !linuxMatch && windowsMatch {
			out = append(out, filepath.Clean(path))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// isGoTestAll reports whether command is "go test" of ./... with only flags
// that keep every test running: -v, -race, -failfast, -json, -cover,
// -count=N (N at least 1), -timeout D, -p N and -parallel N. Any other flag
// (-run, -skip, -short, -list, -count=0, -c, -exec, ...) makes it false, so
// a step that names go test but runs no test of the kit does not count.
func isGoTestAll(command string) bool {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) < 3 || fields[0] != "go" || fields[1] != "test" || fields[len(fields)-1] != "./..." {
		return false
	}
	flags := fields[2 : len(fields)-1]
	for i := 0; i < len(flags); i++ {
		name, value, hasValue := strings.Cut(strings.TrimLeft(flags[i], "-"), "=")
		if !strings.HasPrefix(flags[i], "-") {
			return false
		}
		switch name {
		case "v", "race", "failfast", "json", "cover":
			if hasValue && value != "true" {
				return false
			}
		case "count", "timeout", "p", "parallel":
			if !hasValue {
				if i+1 >= len(flags) {
					return false
				}
				i++
				value = flags[i]
			}
			if name == "timeout" {
				if d, err := time.ParseDuration(value); err != nil || d < 0 {
					return false
				}
				continue
			}
			if n, err := strconv.Atoi(value); err != nil || n < 1 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func runsOnWindows(job *yaml.Node) bool {
	runsOn := get(job, "runs-on")
	if runsOn == nil {
		return false
	}
	if runsOn.Kind == yaml.ScalarNode {
		return strings.HasPrefix(runsOn.Value, "windows")
	}
	if runsOn.Kind == yaml.SequenceNode && len(runsOn.Content) > 0 {
		first := runsOn.Content[0]
		return first.Kind == yaml.ScalarNode && strings.HasPrefix(first.Value, "windows")
	}
	return false
}

// runsGoTestOnWindows reports whether a Windows job tests dir.
func runsGoTestOnWindows(root *yaml.Node, dir string) bool {
	jobs := get(root, "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return false
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		job := jobs.Content[i+1]
		if !runsOnWindows(job) {
			continue
		}

		// The working directory: the step's, else the job's default, else
		// the workflow's default.
		defaultDir := ""
		for _, d := range []*yaml.Node{root, job} {
			if w := get(get(get(d, "defaults"), "run"), "working-directory"); w != nil && w.Kind == yaml.ScalarNode {
				defaultDir = w.Value
			}
		}

		steps := get(job, "steps")
		if steps == nil || steps.Kind != yaml.SequenceNode {
			continue
		}
		for _, step := range steps.Content {
			run := get(step, "run")
			if run == nil || run.Kind != yaml.ScalarNode {
				continue
			}
			command := strings.TrimSpace(run.Value)

			working := get(step, "working-directory")
			effectiveDir := defaultDir
			if working != nil && working.Kind == yaml.ScalarNode {
				effectiveDir = working.Value
			}
			if effectiveDir == dir && isGoTestAll(command) {
				return true
			}
			if effectiveDir == "" {
				prefix := "cd " + dir + " && "
				if strings.HasPrefix(command, prefix) {
					if isGoTestAll(strings.TrimSpace(strings.TrimPrefix(command, prefix))) {
						return true
					}
				}
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

func TestOffLinuxTests(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a_test.go":         "//go:build !linux\n\npackage sample\n",
		"b_windows_test.go": "package sample\n",
		"c_darwin_test.go":  "//go:build !linux\n\npackage sample\n",
		"d_test.go":         "//go:build !linux && go1.21\n\npackage sample\n",
		"e_linux_test.go":   "package sample\n",
		"f_test.go":         "package sample\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got := offLinuxTests(t, dir)
	gotNames := make(map[string]bool, len(got))
	for _, path := range got {
		gotNames[filepath.Base(path)] = true
	}
	want := []string{"a_test.go", "b_windows_test.go", "d_test.go"}
	if len(gotNames) != len(want) {
		t.Errorf("offLinuxTests() = %v, want %v", got, want)
		return
	}
	for _, name := range want {
		if !gotNames[name] {
			t.Errorf("offLinuxTests() = %v, want %v", got, want)
			return
		}
	}
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
		{
			name: "Go test specific package",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test ./manifest\n",
			want: false,
		},
		{
			name: "Go test -list",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test -list . ./...\n",
			want: false,
		},
		{
			name: "Go test -count=0",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test -count=0 ./...\n",
			want: false,
		},
		{
			name: "Go test -count 0",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test -count 0 ./...\n",
			want: false,
		},
		{
			name: "Go test -c",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test -c ./...\n",
			want: false,
		},
		{
			name: "Go test -short",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test -short ./...\n",
			want: false,
		},
		{
			name: "Go test -race -count=1 -timeout 10m",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test -race -count=1 -timeout 10m ./...\n",
			want: true,
		},
		{
			name: "Workflow default working directory",
			yaml: "defaults:\n  run:\n    working-directory: kit\njobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - run: go test ./...\n",
			want: true,
		},
		{
			name: "Go test with run filter",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test -run '^$' ./...\n",
			want: false,
		},
		{
			name: "Echo containing go test",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: echo go test ./...\n",
			want: false,
		},
		{
			name: "Cd another directory",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - run: cd kit-other && go test ./...\n",
			want: false,
		},
		{
			name: "Another working directory",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: other\n        run: go test ./...\n",
			want: false,
		},
		{
			name: "Runs-on sequence",
			yaml: "jobs:\n  kit-windows:\n    runs-on: [windows-2025]\n    steps:\n      - working-directory: kit\n        run: go test ./...\n",
			want: true,
		},
		{
			name: "Default run working directory",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    defaults:\n      run:\n        working-directory: kit\n    steps:\n      - run: go test ./...\n",
			want: true,
		},
		{
			name: "Cd kit working directory",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - run: cd kit && go test ./...\n",
			want: true,
		},
		{
			name: "Count flag",
			yaml: "jobs:\n  kit-windows:\n    runs-on: windows-2025\n    steps:\n      - working-directory: kit\n        run: go test -count=1 ./...\n",
			want: true,
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
