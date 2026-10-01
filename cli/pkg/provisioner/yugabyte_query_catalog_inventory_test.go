package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
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
	if !strings.Contains(target, "run-yugabyte-contract-fixture.sh") || !strings.Contains(target, "verify-yugabyte-contract-lanes") {
		t.Fatal("exhaustive Yugabyte verification must run its contracts in bounded per-lane engine fixtures")
	}
	if !strings.Contains(target, "verify-schema-yugabyte-database-contracts") {
		t.Fatal("exhaustive Yugabyte verification must run every database's schema contract groups")
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
	if !strings.Contains(target, `coverage="schema-$$database"`) ||
		!strings.Contains(target, `coverage="schema-$$database-completion"`) ||
		!strings.Contains(target, `coverage="schema-$$database-preflight"`) ||
		!strings.Contains(target, `"yugabyte/$$coverage"`) {
		t.Fatal("each Yugabyte schema database and contract group must emit a distinct coverage profile")
	}
	var units []string
	for _, database := range makeVariableWords(string(makefile), "YUGABYTE_SCHEMA_DATABASES") {
		for _, group := range makeVariableWords(string(makefile), "YUGABYTE_SCHEMA_GROUPS") {
			units = append(units, "verify-schema-yugabyte-unit-"+database+"-"+group)
		}
	}
	declared := makeVariableWords(string(makefile), "YUGABYTE_SCHEMA_UNIT_TARGETS")
	sort.Strings(units)
	sort.Strings(declared)
	if strings.Join(units, " ") != strings.Join(declared, " ") {
		t.Fatalf("YUGABYTE_SCHEMA_UNIT_TARGETS must name every database and group once:\n got %v\nwant %v", declared, units)
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
	matrixJob := ciJob(t, contents, "database-yugabyte", "database-yugabyte-services")
	servicesJob := ciJob(t, contents, "database-yugabyte-services", "codecov-notify")
	for name, job := range map[string]string{"database-yugabyte": matrixJob, "database-yugabyte-services": servicesJob} {
		checkout := strings.Index(job, "- uses: actions/checkout@")
		setupGo := strings.Index(job, "- uses: actions/setup-go@")
		if checkout < 0 || setupGo < 0 || checkout >= setupGo {
			t.Fatalf("%s checkout/setup-go steps not found in order", name)
		}
		if !strings.Contains(job[checkout:setupGo], "fetch-depth: 0") {
			t.Fatalf("%s checkout must fetch tag history for tagged-upgrade proof", name)
		}
		if !strings.Contains(job, "path: coverage/contracts/yugabyte/") || !strings.Contains(job, "directory: coverage/contracts/yugabyte") {
			t.Fatalf("%s CI job must archive and upload every Yugabyte coverage profile", name)
		}
	}
	for _, command := range []string{
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
		if !strings.Contains(servicesJob, command) {
			t.Errorf("database-yugabyte-services CI job lacks scoped command %q", command)
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

// TestYugabyteDatabaseRunsEveryGroupAndServiceLeg proves verify-yugabyte-database runs a database's three schema
// contract groups and all of its service's contract legs.
func TestYugabyteDatabaseRunsEveryGroupAndServiceLeg(t *testing.T) {
	targets := parseMakeTargets(readRepoFile(t, "Makefile"))
	reached := reachableMakeTargets(t, targets, "verify-yugabyte-database", map[string]string{"DATABASE": "foghorn", "YUGABYTE_DATABASE_SERVICE": "foghorn"})
	for _, name := range []string{
		"verify-schema-yugabyte-unit-foghorn-compat",
		"verify-schema-yugabyte-unit-foghorn-completion",
		"verify-schema-yugabyte-unit-foghorn-preflight",
		"verify-yugabyte-foghorn-contracts-a",
		"verify-yugabyte-foghorn-contracts-b",
		"verify-yugabyte-foghorn-authority-contracts",
	} {
		if !reached[name] {
			t.Errorf("verify-yugabyte-database DATABASE=foghorn does not run %s", name)
		}
	}
}

// Derived from the Makefile: scoped contracts must be reachable from CI and write profiles inside its upload directory.
func TestYugabyteContractInventoryIsComplete(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	targets := parseMakeTargets(makefile)
	workflow := readRepoFile(t, ".github/workflows/ci.yml")
	job := ciJob(t, workflow, "database-yugabyte-services", "codecov-notify")

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

	exhaustive := reachableMakeTargets(t, targets, "verify-yugabyte-contract-lanes", nil)
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
			t.Errorf("%s exists but verify-yugabyte-contract-lanes does not run it", name)
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
					t.Errorf("database-yugabyte-services CI steps do not reach %s", name)
				}
			}
		}
		if legs == 0 {
			t.Errorf("verify-yugabyte-service SERVICE=%s runs no verify-yugabyte-%s-contracts target", service, service)
		}
	}

	for _, profile := range sortedKeys(ciContractProfiles(t, makefile, job)) {
		if !strings.HasPrefix(profile, "yugabyte/") {
			t.Errorf("database-yugabyte-services CI job writes non-Yugabyte profile %q", profile)
		}
	}

	// Each matrix job uploads its coverage directory with if-no-files-found: error, so each target writes a profile,
	// and the matrix together writes what verify-yugabyte-contracts, the verify-prepush stage, writes on one machine.
	var matrix []ciMakeInvocation
	for _, target := range ciYugabyteMatrixTargets(t, workflow, makefile) {
		if len(makeContractProfiles(t, makefile, []ciMakeInvocation{{target: target}})) == 0 {
			t.Errorf("database-yugabyte matrix target %s writes no contract coverage profile", target)
		}
		matrix = append(matrix, ciMakeInvocation{target: target})
	}
	profiles := makeContractProfiles(t, makefile, matrix)
	prepush := makeContractProfiles(t, makefile, []ciMakeInvocation{{target: "verify-yugabyte-contracts"}})
	if got, want := strings.Join(sortedKeys(profiles), " "), strings.Join(sortedKeys(prepush), " "); got != want {
		t.Errorf("database-yugabyte matrix writes profiles\n%s\nverify-yugabyte-contracts writes\n%s", got, want)
	}
	if !strings.Contains(makefile, "verify-yugabyte-contracts+verify-yugabyte-contracts-deps") {
		t.Error("verify-prepush must run verify-yugabyte-contracts")
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

// TestYugabyteLanesRunEveryContractOnce proves the exhaustive Yugabyte lanes run every schema contract unit, every
// engine contract, and every service's contracts, each in exactly one lane.
func TestYugabyteLanesRunEveryContractOnce(t *testing.T) {
	makefile := readRepoFile(t, "Makefile")
	targets := parseMakeTargets(makefile)
	seen := map[string]int{}
	for _, lane := range makeVariableWords(makefile, "YUGABYTE_LANES") {
		target, ok := targets[lane]
		if !ok {
			t.Fatalf("lane %s has no rule", lane)
		}
		for _, line := range target.recipe {
			for _, invocation := range makeSubInvocation.FindAllStringSubmatch(line.text, -1) {
				for _, field := range strings.Fields(invocation[1]) {
					if !strings.HasPrefix(field, "-") {
						seen[field]++
					}
				}
			}
		}
	}
	want := makeVariableWords(makefile, "YUGABYTE_SCHEMA_UNIT_TARGETS")
	for _, line := range strings.Split(makefile, "\n") {
		for _, variable := range []string{"SCHEMA_VERIFY_YUGABYTE_ENGINE_TESTS := ", "MIGRATION_PRECHECK_REALYB_TESTS := "} {
			value, ok := strings.CutPrefix(line, variable)
			if !ok {
				continue
			}
			for _, test := range strings.Split(value, "|") {
				if test != "" && !strings.Contains(test, "$") {
					want = append(want, "verify-schema-yugabyte-engine-"+test)
				}
			}
		}
	}
	for _, service := range []string{"commodore", "foghorn", "purser", "quartermaster", "navigator", "lookout", "bosun", "skipper", "periscope-metering"} {
		want = append(want, "verify-yugabyte-"+service+"-contracts")
	}
	// Every service whose database is declared colocated also runs on distributed databases, as existing production
	// databases stay until they are relaid out. foghorn-authority runs inside the foghorn aggregate.
	colocated := map[string]bool{}
	for _, service := range makeVariableWords(makefile, "YUGABYTE_COLOCATED_SERVICES") {
		colocated[service] = true
	}
	for _, database := range makeVariableWords(makefile, "YUGABYTE_SCHEMA_DATABASES") {
		layout, err := LoadDatabaseLayout(database)
		if err != nil {
			t.Fatal(err)
		}
		service := database
		if database == "periscope" {
			service = "periscope-metering"
		}
		if layout.Colocated() != colocated[service] {
			t.Errorf("YUGABYTE_COLOCATED_SERVICES lists %s = %v, but its database layout is colocated = %v", service, colocated[service], layout.Colocated())
		}
		if layout.Colocated() {
			want = append(want, "verify-yugabyte-distributed-"+service)
		}
	}
	if !colocated["foghorn-authority"] {
		t.Error("YUGABYTE_COLOCATED_SERVICES must list foghorn-authority, whose contracts use the foghorn database")
	}
	for _, name := range want {
		if seen[name] != 1 {
			t.Errorf("%s runs in %d Yugabyte lanes, want exactly one", name, seen[name])
		}
	}
}
