package provisioner

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestMediaAuthorityRegressionsAreCIGates(t *testing.T) {
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
	if !strings.Contains(string(workflow), "run: make verify-schema-migrations-core") {
		t.Fatal("CI does not invoke the PostgreSQL contract gate")
	}
	recipe := regexp.MustCompile(`(?m)^verify-schema-migrations-core:[^\n]*\n(?:\t[^\n]*\n)*`).FindString(string(makefile))
	if !strings.Contains(recipe, "\t@$(MAKE) --no-print-directory verify-placement-db\n") {
		t.Fatal("PostgreSQL CI omits placement and authority contracts")
	}
	placementRecipe := regexp.MustCompile(`(?m)^verify-placement-db:[^\n]*\n(?:\t[^\n]*\n)*`).FindString(string(makefile))
	if !strings.Contains(placementRecipe, "$(COMMODORE_MEDIA_AUTHORITY_REALPG_TESTS)") {
		t.Fatal("placement gate omits authority regressions")
	}
	selectors := regexp.MustCompile(`(?m)^COMMODORE_MEDIA_AUTHORITY_REAL(?:PG|YB)_TESTS(?:_[CD])? := [^\n]*`).FindAllString(string(makefile), -1)
	joined := strings.Join(selectors, "\n")
	for _, pg := range regexp.MustCompile(`Test[A-Za-z0-9_]+_RealPG`).FindAllString(joined, -1) {
		yb := strings.TrimSuffix(pg, "_RealPG") + "_RealYugabyte"
		if !strings.Contains(joined, yb) {
			t.Errorf("PostgreSQL authority contract has no Yugabyte gate: %s", pg)
		}
	}
	filter := regexp.MustCompile(`(?m)^            yugabyte_commodore:\n(?:              - [^\n]*\n)*`).FindString(string(workflow))
	for _, path := range []string{"api_control/internal/**", "pkg/mediaauthority/**", "pkg/placement/**", "pkg/auth/**", "pkg/proto/**"} {
		if !strings.Contains(filter, "'"+path+"'") {
			t.Errorf("Yugabyte CI misses compiler dependency path %s", path)
		}
	}
	postgresFilter := regexp.MustCompile(`(?m)^            database:\n(?:              - [^\n]*\n)*`).FindString(string(workflow))
	for _, path := range []string{"api_control/**", "pkg/mediaauthority/**", "pkg/placement/**", "pkg/auth/**", "pkg/proto/**"} {
		if !strings.Contains(postgresFilter, "'"+path+"'") {
			t.Errorf("PostgreSQL CI misses authority dependency path %s", path)
		}
	}
	for _, name := range []string{"go", "database", "yugabyte_core"} {
		filter := regexp.MustCompile(`(?m)^            ` + name + `:\n(?:              - [^\n]*\n)*`).FindString(string(workflow))
		if !strings.Contains(filter, "'.github/workflows/**'") {
			t.Errorf("workflow-only edits skip %s gates", name)
		}
	}
	for _, path := range []string{"coverage/contracts/", "coverage/contracts/yugabyte/"} {
		if !strings.Contains(string(workflow), "path: "+path+"\n          if-no-files-found: error") {
			t.Errorf("missing contract coverage artifacts are not fatal: %s", path)
		}
	}
}

func TestContractCoverageRequiresRecords(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	wrapper := filepath.Clean(filepath.Join(filepath.Dir(source), "../../../scripts/run-go-contract-test.sh"))
	for _, tt := range []struct {
		name, profile string
		wantOK        bool
	}{
		{"valid", "mode: atomic\nexample/file.go:1.1,2.2 1 0\n", true},
		{"missing", "missing", false},
		{"empty", "", false},
		{"header only", "mode: atomic\n", false},
		{"malformed", "mode: atomic\nnot a record\n", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := "#!/bin/sh\nfor arg in \"$@\"; do\n case \"$arg\" in -coverprofile=*) profile=${arg#-coverprofile=};; esac\ndone\nif [ \"$TEST_PROFILE\" != missing ]; then printf '%s' \"$TEST_PROFILE\" > \"$profile\"; fi\n"
			if err := os.WriteFile(filepath.Join(dir, "go"), []byte(stub), 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "bash", wrapper, dir, "test/profile", "./...")
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "CONTRACT_COVERAGE_DIR="+filepath.Join(dir, "coverage"), "TEST_PROFILE="+tt.profile)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tt.wantOK {
				t.Fatalf("ok=%v, want %v: %s", err == nil, tt.wantOK, out)
			}
		})
	}
}
