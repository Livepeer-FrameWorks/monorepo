package provisioner

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestYugabyteVerificationCoversEveryServiceQueryCatalog(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	target := string(makefile)
	start := strings.Index(target, "verify-schema-yugabyte:")
	if start < 0 {
		t.Fatal("verify-schema-yugabyte target not found")
	}
	end := strings.Index(target[start:], "\nverify-yugabyte-ha:")
	if end < 0 {
		t.Fatal("verify-schema-yugabyte target terminator not found")
	}
	target = target[start : start+end]
	for service, marker := range map[string]string{
		"commodore":          "yugabyte/commodore-query-catalog",
		"purser":             "yugabyte/purser-query-catalog",
		"navigator":          "yugabyte/navigator-query-catalog",
		"skipper":            "yugabyte/skipper-query-catalog",
		"quartermaster":      "yugabyte/quartermaster-query-catalog",
		"periscope-metering": "yugabyte/periscope-metering",
		"foghorn":            "yugabyte/foghorn-query-catalog",
	} {
		if !strings.Contains(target, marker) {
			t.Errorf("Yugabyte-backed service %s lacks generated-query verification marker %q", service, marker)
		}
	}
	if !strings.Contains(target, "run-yugabyte-contract-fixture.sh") || !strings.Contains(target, "verify-yugabyte-services-isolated") {
		t.Fatal("exhaustive Yugabyte verification must use bounded shared-engine fixtures")
	}
}

func TestYugabyteCIJobChecksOutTagHistory(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	workflow, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(workflow)
	start := strings.Index(contents, "  database-yugabyte:")
	if start < 0 {
		t.Fatal("database-yugabyte CI job not found")
	}
	end := strings.Index(contents[start:], "\n  codecov-notify:")
	if end < 0 {
		t.Fatal("database-yugabyte CI job terminator not found")
	}
	job := contents[start : start+end]
	checkout := strings.Index(job, "- uses: actions/checkout@")
	setupGo := strings.Index(job, "- uses: actions/setup-go@")
	if checkout < 0 || setupGo < 0 || checkout >= setupGo {
		t.Fatal("database-yugabyte checkout/setup-go steps not found in order")
	}
	if !strings.Contains(job[checkout:setupGo], "fetch-depth: 0") {
		t.Fatal("database-yugabyte checkout must fetch tag history for tagged-upgrade proof")
	}
	for _, command := range []string{
		"make verify-schema-yugabyte",
		"make verify-yugabyte-service SERVICE=commodore",
		"make verify-yugabyte-service SERVICE=purser",
		"make verify-yugabyte-service SERVICE=navigator",
		"make verify-yugabyte-service SERVICE=skipper",
		"make verify-yugabyte-service SERVICE=quartermaster",
		"make verify-yugabyte-service SERVICE=periscope-metering",
		"make verify-yugabyte-service SERVICE=foghorn",
	} {
		if !strings.Contains(job, command) {
			t.Errorf("database-yugabyte CI job lacks scoped command %q", command)
		}
	}
	for _, profile := range []string{
		"schema.out",
		"commodore-query-catalog.out",
		"purser-query-catalog.out",
		"navigator-query-catalog.out",
		"navigator-store.out",
		"skipper-query-catalog.out",
		"quartermaster-query-catalog.out",
		"periscope-metering.out",
		"foghorn-query-catalog-a.out",
		"foghorn-query-catalog-b.out",
		"ha.out",
	} {
		if !strings.Contains(job, "coverage/contracts/yugabyte/"+profile) {
			t.Errorf("database-yugabyte CI job does not upload coverage profile %q", profile)
		}
	}
}

func TestYugabyteDatabaseFoghornScopesBothFixtureLegs(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", ".."))
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	contents := string(makefile)
	start := strings.Index(contents, "verify-yugabyte-database:")
	if start < 0 {
		t.Fatal("verify-yugabyte-database target not found")
	}
	end := strings.Index(contents[start:], "\nverify-yugabyte-ha:")
	if end < 0 {
		t.Fatal("verify-yugabyte-database target terminator not found")
	}
	target := contents[start : start+end]
	if count := strings.Count(target, "FRAMEWORKS_YUGABYTE_DATABASES=foghorn"); count != 2 {
		t.Fatalf("foghorn database target has %d scoped fixture legs, want 2", count)
	}
}
