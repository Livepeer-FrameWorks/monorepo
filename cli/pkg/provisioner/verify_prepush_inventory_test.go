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

// prepushStageOfCIJob names the verify-prepush stage that runs each job of
// .github/workflows/ci.yml on one machine.
var prepushStageOfCIJob = map[string]string{
	"go-build":                   "verify-go-build",
	"go-lint":                    "verify-go-lint",
	"frontend-lint":              "verify-frontend-lint",
	"frontend-test":              "verify-frontend-test",
	"marketing-browser":          "verify-marketing-browser",
	"frontend-build":             "verify-frontend-build",
	"feature-registry":           "verify-feature-registry",
	"pricing-catalog":            "verify-pricing-catalog",
	"generated-contracts":        "verify-generated-contracts",
	"sdk":                        "verify-sdks",
	"compose-profiles":           "verify-compose-profiles",
	"migration-release-state":    "verify-migration-release-state",
	"database-yugabyte":          "verify-yugabyte-contracts",
	"database-yugabyte-services": "verify-yugabyte-contracts",
	"database-explain-audit":     "verify-yugabyte-explain-audit",
}

// CI jobs with no stage: the path filter, Go test (make test runs it) and the
// jobs that only report other jobs' results.
var ciJobsWithoutPrepushStage = []string{"changes", "go-test", "codecov-notify", "ci-passed"}

// Every CI job has a verify-prepush stage, and a stage runs each pnpm step of
// its job, so a push cannot fail a job that nothing local ran.
func TestEveryCIJobHasAPrepushStage(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}

	stagesDef := regexp.MustCompile(`(?m)^VERIFY_PREPUSH_STAGES := ((?:[^\n]*\\\n)*[^\n]*)$`).FindSubmatch(makefile)
	if stagesDef == nil {
		t.Fatal("Makefile defines no VERIFY_PREPUSH_STAGES")
	}
	var stages []string
	for _, spec := range strings.Fields(strings.ReplaceAll(string(stagesDef[1]), "\\\n", " ")) {
		stages = append(stages, strings.SplitN(spec, "+", 2)[0])
	}

	jobsSection := string(workflow[strings.Index(string(workflow), "\njobs:\n"):])
	jobHeaders := regexp.MustCompile(`(?m)^  ([a-z0-9_-]+):$`).FindAllStringSubmatchIndex(jobsSection, -1)
	if len(jobHeaders) == 0 {
		t.Fatal("ci.yml has no jobs")
	}
	for i, header := range jobHeaders {
		job := jobsSection[header[2]:header[3]]
		end := len(jobsSection)
		if i+1 < len(jobHeaders) {
			end = jobHeaders[i+1][0]
		}
		if slices.Contains(ciJobsWithoutPrepushStage, job) {
			continue
		}
		stage, ok := prepushStageOfCIJob[job]
		if !ok {
			t.Errorf("CI job %s has no verify-prepush stage", job)
			continue
		}
		if !slices.Contains(stages, stage) {
			t.Errorf("CI job %s: stage %s is not in VERIFY_PREPUSH_STAGES %v", job, stage, stages)
			continue
		}
		recipe := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(stage) + `:[^\n]*\n(?:\t[^\n]*\n)*`).FindString(string(makefile))
		if recipe == "" {
			t.Errorf("CI job %s: Makefile has no %s target", job, stage)
			continue
		}
		for _, step := range regexp.MustCompile(`(?m)^\s+(?:- )?run: (pnpm [^\n]*)$`).FindAllStringSubmatch(jobsSection[header[0]:end], -1) {
			command := strings.ReplaceAll(step[1], " --with-deps", "")
			if !strings.Contains(recipe, "\t"+command+"\n") {
				t.Errorf("CI job %s runs %q, which %s does not", job, step[1], stage)
			}
		}
	}
}
