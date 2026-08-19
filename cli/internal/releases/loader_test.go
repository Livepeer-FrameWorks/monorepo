package releases

import (
	"testing"
)

func TestShippedCatalogHasDatabaseOwnershipAndReleaseFloor(t *testing.T) {
	// The shipped catalog declares the v0.2.96 release floor (min CLI version) so the fetched release metadata can carry
	// it. Lookup is base-version normalized, so an rc/prerelease target resolves to the same declared release.
	if got := MinCLIVersionFor("v0.2.96"); got != "v0.2.96-rc1" {
		t.Errorf("MinCLIVersionFor(v0.2.96) = %q, want v0.2.96-rc1", got)
	}
	if got := MinCLIVersionFor("v0.2.96-rc1"); got != "v0.2.96-rc1" {
		t.Errorf("MinCLIVersionFor(v0.2.96-rc1) must resolve via base version, got %q", got)
	}
	if got := ServiceDatabase("purser"); got != "purser" {
		t.Errorf("purser database ownership = %q, want purser", got)
	}
	// Foghorn owns the foghorn database (migrations under migrations/foghorn, schema/foghorn.sql). It MUST be declared
	// so the single-service upgrade gate runs its migration check — otherwise a Foghorn deploy can precede its own
	// migrations (e.g. cell_storage_identity) and crash on startup.
	if got, ok := ServiceDatabaseLookup("foghorn"); !ok || got != "foghorn" {
		t.Errorf("foghorn database ownership = (%q, %v), want (foghorn, true)", got, ok)
	}
	// Navigator and Skipper are database-backed (schema/navigator.sql, schema/skipper.sql; topology declares an
	// InfraDatabase for both). They MUST be declared so the upgrade gate runs — an omission would silently skip it and
	// let them deploy ahead of their schema.
	if got, ok := ServiceDatabaseLookup("navigator"); !ok || got != "navigator" {
		t.Errorf("navigator database ownership = (%q, %v), want (navigator, true)", got, ok)
	}
	if got, ok := ServiceDatabaseLookup("skipper"); !ok || got != "skipper" {
		t.Errorf("skipper database ownership = (%q, %v), want (skipper, true)", got, ok)
	}
	// Decklog is genuinely DB-less (Kafka-only, no InfraDatabase) — it must NOT be declared, so the gate skips it.
	if db, ok := ServiceDatabaseLookup("decklog"); ok || db != "" {
		t.Errorf("decklog database ownership = (%q, %v), want empty/false (DB-less)", db, ok)
	}
	if got := Lookup("v0.5.0"); got != nil {
		t.Errorf("empty release list Lookup must return nil; got %+v", got)
	}
}

func TestLoadErrorIsNilForShippedCatalog(t *testing.T) {
	if err := LoadError(); err != nil {
		t.Fatalf("shipped catalog must parse cleanly: %v", err)
	}
}

// TestParseCatalog_FailsClosedOnInvalidYAML pins the central guarantee behind
// every migration gate: a corrupt catalog parses to a state that CARRIES the
// error and holds NO releases/ownership. Because the process-wide `embedded`
// catalog is this same immutable value and every accessor short-circuits on
// `embedded.err`, CatalogOrError/LoadError surface the error and Catalog/
// ReleasesBelow/Lookup read empty — so a gate fails closed instead of treating
// corruption as "nothing required". Parsing is pure, so this needs no global
// mutation seam.
func TestParseCatalog_FailsClosedOnInvalidYAML(t *testing.T) {
	cs := parseCatalog([]byte("releases: [ {version: v0.1.0"))
	if cs.err == nil {
		t.Fatal("invalid YAML must produce a parse error on the catalog state")
	}
	if cs.releases != nil || cs.serviceDatabases != nil || cs.transitions != nil {
		t.Fatalf("a corrupt parse must carry no releases/ownership/transitions, got %+v", cs)
	}
}

func TestParseCatalog_ValidYAMLSortsAndKeepsOwnership(t *testing.T) {
	cs := parseCatalog([]byte("service_databases:\n  purser: purser\nreleases:\n  - version: v0.2.0\n  - version: v0.1.0\n"))
	if cs.err != nil {
		t.Fatalf("valid YAML must parse cleanly: %v", cs.err)
	}
	if len(cs.releases) != 2 || cs.releases[0].Version != "v0.1.0" || cs.releases[1].Version != "v0.2.0" {
		t.Fatalf("releases must be sorted ascending by semver, got %+v", cs.releases)
	}
	if cs.serviceDatabases["purser"] != "purser" {
		t.Fatalf("service ownership must survive parse, got %+v", cs.serviceDatabases)
	}
}

// TestParseCatalog_RejectsSilentSafetyTypos pins the strict decode + semantic validation: a catalog that YAML alone
// would accept — a misspelled gate field, a malformed version, an unknown migration phase — must fail closed so a typo
// cannot silently remove a release gate or zero a version component.
func TestParseCatalog_RejectsSilentSafetyTypos(t *testing.T) {
	cases := []struct {
		name string
		yaml string
	}{
		{"misspelled rollback field", "releases:\n  - version: v0.2.0\n    rollback_disabld:\n      - chandler\n"},
		{"misspelled migrations field", "releases:\n  - version: v0.2.0\n    required_data_migations: []\n"},
		{"unknown top-level field", "service_datbases:\n  purser: purser\n"},
		{"malformed release version", "releases:\n  - version: v0.2.x\n"},
		{"duplicate release version", "releases:\n  - version: v0.2.0\n  - version: v0.2.0\n"},
		{"base-version collision rc and final", "releases:\n  - version: v1.0.0-rc1\n  - version: v1.0.0\n"},
		{"trailing yaml document", "releases:\n  - version: v0.2.0\n---\nreleases:\n  - version: v0.3.0\n"},
		{"invalid migration phase", "releases:\n  - version: v0.2.0\n    required_data_migrations:\n      - id: m1\n        service: purser\n        introduced_in: v0.2.0\n        required_before_phase: someday\n"},
		{"empty migration id", "releases:\n  - version: v0.2.0\n    required_data_migrations:\n      - id: \"\"\n        service: purser\n        introduced_in: v0.2.0\n        required_before_phase: postdeploy\n"},
		{"empty transition id", "release_transitions:\n  - id: \"\"\n    introduced_in: v0.2.0\n"},
		{"malformed transition version", "release_transitions:\n  - id: t1\n    introduced_in: nope\n"},
		{"empty ownership database", "service_databases:\n  purser: \"\"\n"},
		{"malformed min_cli_version", "releases:\n  - version: v0.2.0\n    min_cli_version: v0.2\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cs := parseCatalog([]byte(c.yaml))
			if cs.err == nil {
				t.Fatalf("%s must fail closed, but parsed clean: %+v", c.name, cs)
			}
			if cs.releases != nil || cs.serviceDatabases != nil || cs.transitions != nil {
				t.Fatalf("%s: a rejected catalog must carry no data, got %+v", c.name, cs)
			}
		})
	}
}

// TestParseCatalog_ShippedCatalogPassesStrictValidation guards that the checked-in catalog satisfies the strict decode
// and every semantic rule — the same path `embedded` takes at init.
func TestParseCatalog_ShippedCatalogPassesStrictValidation(t *testing.T) {
	if cs := parseCatalog(catalogYAML); cs.err != nil {
		t.Fatalf("shipped catalog must pass strict validation: %v", cs.err)
	}
}

func TestReleasesBelow_ShippedCatalog(t *testing.T) {
	// The shipped catalog declares only v0.2.96; nothing is below it, and the target's own base is excluded.
	if got := ReleasesBelow("v0.2.96"); len(got) != 0 {
		t.Fatalf("nothing is below the only shipped release; got %+v", got)
	}
	// An RC target excludes the declared final by BASE version too (v0.2.96-rc1 base == v0.2.96).
	if got := ReleasesBelow("v0.2.96-rc1"); len(got) != 0 {
		t.Fatalf("an RC target must exclude its declared final by base; got %+v", got)
	}
}

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.4.0", "v0.5.0", -1},
		{"v0.5.0", "v0.4.0", 1},
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.3-rc1", "v1.2.3", -1},
		{"v1.2.3", "v1.2.3-rc1", 1},
		{"v1.2.3-rc1", "v1.2.3-rc2", -1},
		{"v0.10.0", "v0.9.0", 1},
		{"v0.0.1", "v0.1.0", -1},
	}
	for _, c := range cases {
		if got := CompareSemver(c.a, c.b); got != c.want {
			t.Errorf("CompareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestReleasesBelow_MultiReleaseCatalog(t *testing.T) {
	// A synthetic multi-release catalog: today's single-entry catalog makes the gate loop run zero times, so exercise
	// the all-prior selection through the pure releasesBelow helper (no global swap). It must return EVERY release below
	// the target's base — independent of any "current" version — and exclude the target's own base (postdeploy runs
	// after the deploy). Input is ascending, as the embedded catalog is always sorted.
	catalog := []Release{
		{Version: "v0.3.0"}, {Version: "v0.4.0"}, {Version: "v0.5.0"}, {Version: "v0.6.0"},
	}

	versions := func(rs []Release) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = r.Version
		}
		return out
	}

	// Target in the middle: every prior release, none of v0.5.0/v0.6.0, and NOT v0.5.0 itself.
	if got := versions(releasesBelow(catalog, "v0.5.0")); len(got) != 2 || got[0] != "v0.3.0" || got[1] != "v0.4.0" {
		t.Fatalf("releasesBelow(v0.5.0) = %v, want [v0.3.0 v0.4.0]", got)
	}
	// Highest target: all four priors below it.
	if got := versions(releasesBelow(catalog, "v0.6.0")); len(got) != 3 {
		t.Fatalf("releasesBelow(v0.6.0) = %v, want the three prior releases", got)
	}
	// An RC of a middle release still excludes that release's declared final (by base) and returns only what's below it.
	if got := versions(releasesBelow(catalog, "v0.5.0-rc1")); len(got) != 2 || got[1] != "v0.4.0" {
		t.Fatalf("releasesBelow(v0.5.0-rc1) = %v, want [v0.3.0 v0.4.0] (RC base excludes v0.5.0)", got)
	}
	// A target below everything selects nothing.
	if got := releasesBelow(catalog, "v0.1.0"); len(got) != 0 {
		t.Fatalf("releasesBelow(v0.1.0) = %v, want empty", got)
	}
}

// TestRequiredTransitionIDsPublished confirms the embedded catalog's transition (introduced at v0.2.96) is published
// for v0.2.96+ and its canary, and absent for a lower target — the metadata the release pipeline writes into the
// fetched manifest for outdated-CLI fail-closed validation.
func TestRequiredTransitionIDsPublished(t *testing.T) {
	for _, target := range []string{"v0.2.96", "v0.2.96-rc1", "v0.3.0"} {
		ids, err := RequiredTransitionIDs(target)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		found := false
		for _, id := range ids {
			if id == "storage-descriptor-adoption" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: expected storage-descriptor-adoption in %v", target, ids)
		}
	}
	ids, err := RequiredTransitionIDs("v0.2.90")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == "storage-descriptor-adoption" {
			t.Fatalf("v0.2.90 must NOT require the v0.2.96 transition, got %v", ids)
		}
	}
}
