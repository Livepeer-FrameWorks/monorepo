package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func readCIWorkflow(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	workflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(workflow)
}

// ci-passed is the required check, so a job missing from its needs can fail
// without failing CI.
func TestCIPassedNeedsEveryJob(t *testing.T) {
	workflow := readCIWorkflow(t)
	jobs := workflow[strings.Index(workflow, "\njobs:\n"):]
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z0-9_-]+):$`).FindAllStringSubmatch(jobs, -1) {
		names = append(names, m[1])
	}
	needs := regexp.MustCompile(`(?ms)^  ci-passed:\n.*?    needs:\n\s+\[(.*?)\]`).FindStringSubmatch(jobs)
	if needs == nil {
		t.Fatal("ci-passed has no needs list")
	}
	var needed []string
	for _, n := range strings.Split(needs[1], ",") {
		if n = strings.TrimSpace(n); n != "" {
			needed = append(needed, n)
		}
	}
	// codecov-notify only forwards coverage and never fails a change.
	for _, name := range names {
		if name == "ci-passed" || name == "codecov-notify" {
			continue
		}
		if !slices.Contains(needed, name) {
			t.Errorf("ci-passed does not need job %s", name)
		}
	}
}

// Frontend lint runs pnpm lint and Prettier over the whole tree, so every file
// type they read must start it.
func TestFrontendLintRunsForEveryLintedFileType(t *testing.T) {
	workflow := readCIWorkflow(t)
	filter := func(name string) string {
		return regexp.MustCompile(`(?m)^            ` + name + `:\n(?:            #[^\n]*\n)*(?:              - [^\n]*\n)*`).FindString(workflow)
	}
	frontend, format := filter("frontend"), filter("format")
	if !strings.Contains(workflow, "if: needs.changes.outputs.frontend == 'true' || needs.changes.outputs.format == 'true' ||") {
		t.Fatal("Frontend lint does not run on the format filter")
	}
	for _, ext := range []string{"ts", "tsx", "mts", "cts", "js", "jsx", "mjs", "cjs", "svelte", "astro", "json", "css", "html", "gql", "graphql"} {
		if !strings.Contains(frontend, "'**/*."+ext+"'") {
			t.Errorf("frontend filter misses **/*.%s", ext)
		}
	}
	for _, ext := range []string{"md", "mdx", "yml", "yaml"} {
		if !strings.Contains(frontend+format, "'**/*."+ext+"'") {
			t.Errorf("Frontend lint filters miss **/*.%s", ext)
		}
	}
}
