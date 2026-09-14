package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
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
	if !strings.Contains(target, "verify-schema-yugabyte-schema-isolated") {
		t.Fatal("exhaustive Yugabyte schema verification must isolate database catalogs")
	}
	if !strings.Contains(string(makefile), "YUGABYTE_SCHEMA_DATABASES := commodore foghorn navigator periscope purser quartermaster skipper") {
		t.Fatal("Yugabyte schema shard inventory must cover every service database")
	}
	if !strings.Contains(target, "YUGABYTE_SCHEMA_COVERAGE_NAME=\"schema-$$database\"") {
		t.Fatal("each Yugabyte schema shard must emit a distinct coverage profile")
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
		"schema-selection.out",
		"schema-commodore.out",
		"schema-foghorn.out",
		"schema-navigator.out",
		"schema-periscope.out",
		"schema-purser.out",
		"schema-quartermaster.out",
		"schema-skipper.out",
		"commodore-query-catalog.out",
		"commodore-placement-a.out",
		"commodore-placement-b.out",
		"purser-query-catalog.out",
		"navigator-query-catalog.out",
		"navigator-store.out",
		"skipper-query-catalog.out",
		"quartermaster-query-catalog.out",
		"quartermaster-consent-management.out",
		"periscope-metering.out",
		"foghorn-query-catalog-a.out",
		"foghorn-query-catalog-b.out",
		"ha.out",
	} {
		if !strings.Contains(job, "coverage/contracts/yugabyte/"+profile) {
			t.Errorf("database-yugabyte CI job does not upload coverage profile %q", profile)
		}
	}
	makefile, err := os.ReadFile(filepath.Join(repoRoot, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	profilePattern := regexp.MustCompile(`\$\(CONTRACT_GO_TEST\)\s+\S+\s+yugabyte/([a-z0-9-]+)`)
	for _, targetName := range []string{
		"verify-schema-yugabyte-selection-contracts",
		"verify-yugabyte-commodore-contracts-a",
		"verify-yugabyte-commodore-contracts-b",
		"verify-yugabyte-purser-contracts",
		"verify-yugabyte-navigator-contracts",
		"verify-yugabyte-skipper-contracts",
		"verify-yugabyte-quartermaster-contracts",
		"verify-yugabyte-periscope-metering-contracts",
		"verify-yugabyte-foghorn-contracts-a",
		"verify-yugabyte-foghorn-contracts-b",
		"verify-yugabyte-ha",
	} {
		marker := "\n" + targetName + ":"
		start := strings.Index(string(makefile), marker)
		if start < 0 {
			t.Errorf("Makefile coverage target %q not found", targetName)
			continue
		}
		body := string(makefile)[start+len(marker):]
		if end := strings.Index(body, "\n\n"); end >= 0 {
			body = body[:end]
		}
		matches := profilePattern.FindAllStringSubmatch(body, -1)
		if len(matches) == 0 {
			t.Errorf("Makefile coverage target %q emits no Yugabyte profile", targetName)
			continue
		}
		for _, match := range matches {
			profile := match[1] + ".out"
			if !strings.Contains(job, "coverage/contracts/yugabyte/"+profile) {
				t.Errorf("database-yugabyte CI job omits %q profile %q", targetName, profile)
			}
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
