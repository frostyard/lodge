package workflowcheck

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	provisionNodeRuntimeLine = regexp.MustCompile(`(?m)^[ \t]*NODE_VERSION[ \t]*=[ \t]*([0-9]+\.[0-9]+\.[0-9]+)[ \t]*\r?$`)
	workflowNodeRuntimeLine  = regexp.MustCompile(`(?m)^[ \t]*NODE_VERSION:[ \t]*"([0-9]+\.[0-9]+\.[0-9]+)"[ \t]*\r?$`)
	looseNodeRuntimeLine     = regexp.MustCompile(`(?m)^[ \t]*NODE_VERSION[ \t]*[:=][^\r\n]*\r?$`)
	exactNodeTypesVersion    = regexp.MustCompile(`^([0-9]+)\.[0-9]+\.[0-9]+$`)
)

// parseNodeRuntimeVersionLines returns exact runtime versions that pattern
// finds in a source file.
func parseNodeRuntimeVersionLines(text string, pattern *regexp.Regexp) []string {
	matches := pattern.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 1 {
			out = append(out, match[0])
		} else {
			out = append(out, match[1])
		}
	}
	return out
}

// nodeRuntimeAssignmentProblems reports each NODE_VERSION assignment line
// that the source's strict pattern cannot read, so no runtime source is
// skipped in silence.
func nodeRuntimeAssignmentProblems(file, text string, strict *regexp.Regexp) []string {
	var problems []string
	for i, line := range strings.Split(text, "\n") {
		if looseNodeRuntimeLine.MatchString(line) && !strict.MatchString(line) {
			problems = append(problems, fmt.Sprintf("%s:%d: NODE_VERSION assignment does not use exact X.Y.Z syntax", file, i+1))
		}
	}
	return problems
}

// nodeTypesProblems compares package.json @types/node pins with the runtime
// major. The map is package file to @types/node version.
func nodeTypesProblems(runtimeMajor string, pkgs map[string]string) []string {
	files := make([]string, 0, len(pkgs))
	for file := range pkgs {
		files = append(files, file)
	}
	slices.Sort(files)

	var problems []string
	for _, file := range files {
		version := pkgs[file]
		match := exactNodeTypesVersion.FindStringSubmatch(version)
		if match == nil {
			problems = append(problems, fmt.Sprintf("%s: @types/node version %q is not exact X.Y.Z (runtime major %s)", file, version, runtimeMajor))
			continue
		}
		if match[1] != runtimeMajor {
			problems = append(problems, fmt.Sprintf("%s: @types/node version %q has major %s, want runtime major %s", file, version, match[1], runtimeMajor))
		}
	}
	return problems
}

type nodeTypesPackage struct {
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// TestNodeTypesFollowsNodeRuntime guards the Node type/runtime contract:
// @types/node must describe the Node that runs the tests and the build, so the
// type check cannot accept an API that Node 24 lacks; a Node major moves by
// hand with its types.
func TestNodeTypesFollowsNodeRuntime(t *testing.T) {
	root := filepath.Join("..", "..")
	sources := make(map[string]string)

	provision := filepath.Join(root, "dev", "nsl", "provision.sh")
	data, err := os.ReadFile(provision)
	if err != nil {
		t.Fatal(err)
	}
	provisionText := string(data)
	for _, problem := range nodeRuntimeAssignmentProblems("dev/nsl/provision.sh", provisionText, provisionNodeRuntimeLine) {
		t.Error(problem)
	}
	provisionVersions := parseNodeRuntimeVersionLines(provisionText, provisionNodeRuntimeLine)
	if len(provisionVersions) != 1 {
		t.Errorf("dev/nsl/provision.sh: must give exactly 1 Node runtime version; found %d", len(provisionVersions))
	}
	for _, version := range provisionVersions {
		major, _, _ := strings.Cut(version, ".")
		sources["dev/nsl/provision.sh"] = major
	}

	workflows, err := filepath.Glob(filepath.Join(root, ".github", "workflows", "*.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, workflow := range workflows {
		data, err := os.ReadFile(workflow)
		if err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(root, workflow)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		workflowText := string(data)
		for _, problem := range nodeRuntimeAssignmentProblems(rel, workflowText, workflowNodeRuntimeLine) {
			t.Error(problem)
		}
		versions := parseNodeRuntimeVersionLines(workflowText, workflowNodeRuntimeLine)
		for _, version := range versions {
			major, _, _ := strings.Cut(version, ".")
			if previous, exists := sources[rel]; exists && previous != major {
				t.Errorf("%s uses Node runtime major %s after another runtime source used major %s", rel, major, previous)
			}
			sources[rel] = major
		}
	}

	if len(sources) == 0 {
		t.Fatal("no Node runtime version found in dev/nsl/provision.sh or .github/workflows/*.yml")
	}

	sourceFiles := make([]string, 0, len(sources))
	for file := range sources {
		sourceFiles = append(sourceFiles, file)
	}
	slices.Sort(sourceFiles)

	for i, file := range sourceFiles {
		for _, other := range sourceFiles[i+1:] {
			if sources[file] != sources[other] {
				t.Errorf("Node runtime major differs: %s has major %s; %s has major %s", file, sources[file], other, sources[other])
			}
		}
	}

	runtime := sources[sourceFiles[0]]
	pkgs := make(map[string]string)
	checked := 0
	for _, file := range repoFiles(t) {
		if path.Base(file) != "package.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		var pkg nodeTypesPackage
		if err := json.Unmarshal(data, &pkg); err != nil {
			t.Errorf("%s: invalid package.json: %v", file, err)
			continue
		}
		dependencyVersion, inDependencies := pkg.Dependencies["@types/node"]
		devVersion, inDevDependencies := pkg.DevDependencies["@types/node"]
		switch {
		case inDependencies && inDevDependencies:
			checked++
			if dependencyVersion == devVersion {
				pkgs[file] = dependencyVersion
			} else {
				pkgs[file+" (dependencies)"] = dependencyVersion
				pkgs[file+" (devDependencies)"] = devVersion
			}
		case inDependencies:
			checked++
			pkgs[file] = dependencyVersion
		case inDevDependencies:
			checked++
			pkgs[file] = devVersion
		}
	}

	if checked == 0 {
		t.Fatal("0 package.json files have @types/node; the Node types guard cannot pass with an empty input")
	}

	for _, problem := range nodeTypesProblems(runtime, pkgs) {
		t.Errorf("%s (runtime sources: %s)", problem, strings.Join(sourceFiles, ", "))
	}
	t.Logf("%d runtime sources read; %d package.json files with @types/node checked", len(sourceFiles), checked)
}

// TestNodeTypesProblems checks exact and mismatched @types/node versions.
func TestNodeTypesProblems(t *testing.T) {
	for _, tc := range []struct {
		name       string
		runtime    string
		pkgs       map[string]string
		want       int
		wantSubstr string
	}{
		{name: "match", runtime: "24", pkgs: map[string]string{"ui/package.json": "24.19.1"}, want: 0},
		{name: "Node 26 types", runtime: "24", pkgs: map[string]string{"ui/package.json": "26.6.6"}, want: 1, wantSubstr: "26.6.6"},
		{name: "range", runtime: "24", pkgs: map[string]string{"ui/package.json": "^24.0.0"}, want: 1},
		{name: "empty", runtime: "24", pkgs: map[string]string{}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nodeTypesProblems(tc.runtime, tc.pkgs)
			if len(got) != tc.want {
				t.Errorf("nodeTypesProblems(%q, %q) = %v, want %d problems", tc.runtime, tc.pkgs, got, tc.want)
			}
			if tc.wantSubstr != "" {
				if len(got) != 1 || !strings.Contains(got[0], tc.wantSubstr) {
					t.Errorf("nodeTypesProblems(%q, %q) = %v, want problem containing %q", tc.runtime, tc.pkgs, got, tc.wantSubstr)
				}
			}
		})
	}
}

// TestNodeRuntimeAssignmentProblems checks that an assignment the strict
// pattern cannot read is reported with its line.
func TestNodeRuntimeAssignmentProblems(t *testing.T) {
	for _, tc := range []struct {
		name   string
		text   string
		strict *regexp.Regexp
		want   []string
	}{
		{name: "exact shell", text: "x=1\nNODE_VERSION=24.21.0\n", strict: provisionNodeRuntimeLine, want: nil},
		{name: "quoted shell", text: "x=1\nNODE_VERSION=\"24.21.0\"\n", strict: provisionNodeRuntimeLine, want: []string{"f:2: NODE_VERSION assignment does not use exact X.Y.Z syntax"}},
		{name: "exact YAML", text: "env:\n  NODE_VERSION: \"24.21.0\"\n", strict: workflowNodeRuntimeLine, want: nil},
		{name: "unquoted YAML", text: "env:\n  NODE_VERSION: 24.21.0\n", strict: workflowNodeRuntimeLine, want: []string{"f:2: NODE_VERSION assignment does not use exact X.Y.Z syntax"}},
		{name: "reference only", text: "node-version: ${{ env.NODE_VERSION }}\n", strict: workflowNodeRuntimeLine, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nodeRuntimeAssignmentProblems("f", tc.text, tc.strict); !slices.Equal(got, tc.want) {
				t.Errorf("nodeRuntimeAssignmentProblems() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNodeTypesParseNodeRuntimeVersionLines checks the two source line formats.
func TestNodeTypesParseNodeRuntimeVersionLines(t *testing.T) {
	for _, tc := range []struct {
		name    string
		text    string
		pattern *regexp.Regexp
		want    []string
	}{
		{name: "provision", text: "NODE_VERSION=24.21.0\n", pattern: provisionNodeRuntimeLine, want: []string{"24.21.0"}},
		{name: "workflow", text: "  NODE_VERSION: \"24.21.0\"\n", pattern: workflowNodeRuntimeLine, want: []string{"24.21.0"}},
		{name: "none", text: "NODE_VERSION=latest\n", pattern: provisionNodeRuntimeLine, want: []string{}},
		{name: "loose quoted shell", text: "NODE_VERSION=\"24.21.0\"\n", pattern: looseNodeRuntimeLine, want: []string{"NODE_VERSION=\"24.21.0\""}},
		{name: "loose quoted shell strict", text: "NODE_VERSION=\"24.21.0\"\n", pattern: provisionNodeRuntimeLine, want: []string{}},
		{name: "loose unquoted YAML", text: "  NODE_VERSION: 24.21.0\n", pattern: looseNodeRuntimeLine, want: []string{"  NODE_VERSION: 24.21.0"}},
		{name: "loose unquoted YAML strict", text: "  NODE_VERSION: 24.21.0\n", pattern: workflowNodeRuntimeLine, want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseNodeRuntimeVersionLines(tc.text, tc.pattern)
			if !slices.Equal(got, tc.want) {
				t.Errorf("parseNodeRuntimeVersionLines() = %v, want %v", got, tc.want)
			}
		})
	}
}
