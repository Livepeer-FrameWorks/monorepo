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
		"lookout":            "yugabyte/lookout-query-catalog",
	} {
		if !strings.Contains(string(makefile), marker) {
			t.Errorf("Yugabyte-backed service %s lacks generated-query verification marker %q", service, marker)
		}
	}
	if !strings.Contains(target, "run-yugabyte-contract-fixture.sh") || !strings.Contains(target, "verify-yugabyte-services-isolated") {
		t.Fatal("exhaustive Yugabyte verification must use bounded shared-engine fixtures")
	}
	if !strings.Contains(target, "verify-schema-yugabyte-schema-isolated") {
		t.Fatal("exhaustive Yugabyte schema verification must isolate database catalogs")
	}
	for _, service := range []string{"commodore", "purser", "navigator", "skipper", "quartermaster", "periscope-metering", "foghorn", "lookout", "bosun"} {
		if !strings.Contains(string(makefile), "verify-yugabyte-"+service+"-contracts") {
			t.Errorf("exhaustive Yugabyte services batch lacks %s contracts", service)
		}
	}
	if !strings.Contains(target, "verify-backup-restore-yugabyte") {
		t.Error("exhaustive Yugabyte verification lacks the backup and restore round trip")
	}
	if !strings.Contains(string(makefile), "YUGABYTE_SCHEMA_DATABASES := bosun commodore foghorn lookout navigator periscope purser quartermaster skipper") {
		t.Fatal("Yugabyte schema shard inventory must cover every service database")
	}
	if !strings.Contains(target, "for group in compat completion preflight; do") ||
		!strings.Contains(target, `coverage_name="schema-$$database"`) ||
		!strings.Contains(target, `coverage_name="$$coverage_name-$$group"`) ||
		!strings.Contains(target, `YUGABYTE_SCHEMA_COVERAGE_NAME="$$coverage_name"`) {
		t.Fatal("each Yugabyte schema shard and contract group must emit a distinct coverage profile")
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
		"make verify-yugabyte-service SERVICE=lookout",
		"make verify-yugabyte-service SERVICE=bosun",
	} {
		if !strings.Contains(job, command) {
			t.Errorf("database-yugabyte CI job lacks scoped command %q", command)
		}
	}
	if !strings.Contains(job, "path: coverage/contracts/yugabyte/") || !strings.Contains(job, "directory: coverage/contracts/yugabyte") {
		t.Fatal("database-yugabyte CI job must archive and upload every Yugabyte coverage profile")
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
		"verify-yugabyte-commodore-contracts-c",
		"verify-yugabyte-purser-contracts",
		"verify-yugabyte-navigator-contracts",
		"verify-yugabyte-skipper-contracts",
		"verify-yugabyte-quartermaster-contracts",
		"verify-yugabyte-periscope-metering-contracts",
		"verify-yugabyte-foghorn-contracts-a",
		"verify-yugabyte-foghorn-contracts-b",
		"verify-yugabyte-lookout-contracts",
		"verify-yugabyte-bosun-contracts",
		"verify-backup-restore-yugabyte",
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

// Derived from the Makefile: scoped contracts must be reachable from CI and write profiles inside its upload directory.
func TestYugabyteContractInventoryIsComplete(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	targets := parseMakeTargets(makefile)
	job := ciJob(t, readRepoFile(t, ".github/workflows/ci.yml"), "database-yugabyte", "codecov-notify")

	serviceTarget, ok := targets["verify-yugabyte-service"]
	if !ok || len(serviceTarget.recipe) == 0 {
		t.Fatal("verify-yugabyte-service target not found")
	}
	caseValues := regexp.MustCompile(`case "\$\(SERVICE\)" in ([a-z|-]+)\)`).FindStringSubmatch(serviceTarget.recipe[0].text)
	if caseValues == nil {
		t.Fatal("verify-yugabyte-service does not validate SERVICE against a case list")
	}
	services := map[string]bool{}
	for _, service := range strings.Split(caseValues[1], "|") {
		services[service] = true
	}

	exhaustive := reachableMakeTargets(t, targets, "verify-yugabyte-services-isolated", nil)
	contractTarget := regexp.MustCompile(`^verify-yugabyte-([a-z-]+)-contracts$`)
	for name := range targets {
		match := contractTarget.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		if !services[match[1]] {
			t.Errorf("%s exists but verify-yugabyte-service does not accept SERVICE=%s", name, match[1])
		}
		if !exhaustive[name] {
			t.Errorf("%s exists but verify-yugabyte-services-isolated does not run it", name)
		}
	}
	scopedTargets := map[string]bool{}
	for _, match := range regexp.MustCompile(`run: make verify-yugabyte-service SERVICE=([a-z-]+)\n`).FindAllStringSubmatch(job, -1) {
		for name := range reachableMakeTargets(t, targets, "verify-yugabyte-service", map[string]string{"SERVICE": match[1]}) {
			scopedTargets[name] = true
		}
	}
	for _, service := range sortedKeys(services) {
		reached := reachableMakeTargets(t, targets, "verify-yugabyte-service", map[string]string{"SERVICE": service})
		legs := 0
		for name := range reached {
			if strings.HasPrefix(name, "verify-yugabyte-"+service+"-contracts") {
				legs++
				if !scopedTargets[name] {
					t.Errorf("database-yugabyte CI scoped steps do not reach %s", name)
				}
			}
		}
		if legs == 0 {
			t.Errorf("verify-yugabyte-service SERVICE=%s runs no verify-yugabyte-%s-contracts target", service, service)
		}
	}

	profiles := ciContractProfiles(t, makefile, job)
	if !strings.Contains(job, "path: coverage/contracts/yugabyte/") || !strings.Contains(job, "directory: coverage/contracts/yugabyte") {
		t.Fatal("database-yugabyte CI job must archive and upload its Yugabyte coverage directory")
	}
	for _, profile := range sortedKeys(profiles) {
		if !strings.HasPrefix(profile, "yugabyte/") {
			t.Errorf("database-yugabyte CI job writes non-Yugabyte profile %q", profile)
		}
	}
	for _, profile := range []string{"yugabyte/bosun-ledger", "yugabyte/quartermaster-capabilities", "yugabyte/cli-backup-restore", "yugabyte/schema-bosun", "yugabyte/schema-purser-completion", "yugabyte/schema-purser-preflight"} {
		if !profiles[profile] {
			t.Errorf("database-yugabyte CI job no longer runs the %q contract", profile)
		}
	}
	for _, database := range makeVariableWords(makefile, "YUGABYTE_SCHEMA_DATABASES") {
		for _, suffix := range []string{"", "-completion", "-preflight"} {
			profile := "yugabyte/schema-" + database + suffix
			if !profiles[profile] {
				t.Errorf("database-yugabyte CI job no longer runs the %q contract", profile)
			}
		}
	}
}
