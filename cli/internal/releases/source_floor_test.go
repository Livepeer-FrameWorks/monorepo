package releases

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

const sourceFloorFixture = `
releases:
  - version: v0.3.10
  - version: v0.3.11
  - version: v0.3.12
    min_source_version: v0.3.11
  - version: v0.3.13
  - version: v0.3.14
    min_source_version: v0.3.13
`

func TestMinSourceVersionCarriesForward(t *testing.T) {
	cs := parseCatalog([]byte(sourceFloorFixture))
	if cs.err != nil {
		t.Fatalf("fixture must parse: %v", cs.err)
	}
	cases := []struct {
		version   string
		wantFloor string
		wantKnown bool
	}{
		{"v0.3.10", "", true},
		{"v0.3.11", "", true},
		{"v0.3.12", "v0.3.11", true},
		{"v0.3.12-rc1", "v0.3.11", true},
		{"v0.3.13", "v0.3.11", true},
		{"v0.3.14", "v0.3.13", true},
		{"v0.3.15", "", false},
		{"v0.3.9", "", true},
	}
	for _, tc := range cases {
		floor, known := cs.minSourceVersionFor(tc.version)
		if floor != tc.wantFloor || known != tc.wantKnown {
			t.Errorf("minSourceVersionFor(%s) = (%q, %v), want (%q, %v)", tc.version, floor, known, tc.wantFloor, tc.wantKnown)
		}
	}
	if !cs.sourceFloorsDeclared() {
		t.Fatal("fixture declares floors; sourceFloorsDeclared() = false")
	}
	if parseCatalog([]byte("releases:\n  - version: v0.3.10\n")).sourceFloorsDeclared() {
		t.Fatal("a catalog without min_source_version reports floors declared")
	}
}

func TestMinSourceVersionValidation(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "malformed",
			yaml:    "releases:\n  - version: v0.3.10\n  - version: v0.3.11\n    min_source_version: 0.3.10\n",
			wantErr: "min_source_version",
		},
		{
			name:    "not below its own release",
			yaml:    "releases:\n  - version: v0.3.10\n  - version: v0.3.11\n    min_source_version: v0.3.11\n",
			wantErr: "below its own version",
		},
		{
			name:    "not a declared release",
			yaml:    "releases:\n  - version: v0.3.8\n  - version: v0.3.10\n  - version: v0.3.11\n    min_source_version: v0.3.9\n",
			wantErr: "not a declared release",
		},
		{
			// A floor must name a declared release below its own, so it can never sit above the predecessor: an
			// undeclared version between the predecessor and the release would strand every source.
			name:    "between the predecessor and the release",
			yaml:    "releases:\n  - version: v0.3.9\n  - version: v0.3.10\n  - version: v0.3.12\n    min_source_version: v0.3.11\n",
			wantErr: "not a declared release",
		},
		{
			name:    "does not raise the carried floor",
			yaml:    "releases:\n  - version: v0.3.9\n  - version: v0.3.10\n  - version: v0.3.11\n    min_source_version: v0.3.10\n  - version: v0.3.12\n    min_source_version: v0.3.9\n",
			wantErr: "does not raise",
		},
		{
			name:    "prerelease spelling",
			yaml:    "releases:\n  - version: v0.3.10\n  - version: v0.3.11\n    min_source_version: v0.3.10-rc1\n",
			wantErr: "plain vX.Y.Z",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs := parseCatalog([]byte(tc.yaml))
			if cs.err == nil || !strings.Contains(cs.err.Error(), tc.wantErr) {
				t.Fatalf("parse error = %v, want it to mention %q", cs.err, tc.wantErr)
			}
		})
	}

	predecessor := "releases:\n  - version: v0.3.9\n  - version: v0.3.10\n  - version: v0.3.11\n    min_source_version: v0.3.9\n  - version: v0.3.12\n    min_source_version: v0.3.10\n"
	if cs := parseCatalog([]byte(predecessor)); cs.err != nil {
		t.Fatalf("floors at or below the predecessor must parse: %v", cs.err)
	}
	skip := "releases:\n  - version: v0.3.10\n  - version: v0.3.11\n  - version: v0.3.12\n    min_source_version: v0.3.10\n"
	if cs := parseCatalog([]byte(skip)); cs.err != nil {
		t.Fatalf("a floor below the predecessor must parse: %v", cs.err)
	}
}

// TestReadySinceRemovedOnceSourceFloorReachesIt is the forcing function for the readiness flip. servicedefs.ReadySince
// keeps version-blind consumers (Quartermaster's poller, Navigator's monitors, registration, the doctor) on /health
// while a binary without /ready may still run. Once the newest catalog release requires a source at or above
// ReadySince, no such binary can run beside that release, and the field must be removed so those consumers follow
// ReadyPath. ReadySince must also name a declared release.
func TestReadySinceRemovedOnceSourceFloorReachesIt(t *testing.T) {
	catalog, err := CatalogOrError()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(catalog) == 0 {
		t.Fatal("shipped catalog declares no releases")
	}
	newest := catalog[len(catalog)-1].Version
	floor, _ := MinSourceVersionFor(newest)
	for _, problem := range readySinceProblems(servicedefs.Services, newest, floor, func(v string) bool { return Lookup(v) != nil }) {
		t.Error(problem)
	}

	// The same check against the catalog the next release declares: v0.3.12 with min_source_version v0.3.11 flags
	// every service whose ReadySince is v0.3.11 and leaves Chandler alone.
	cs := parseCatalog([]byte(sourceFloorFixture))
	nextFloor, _ := cs.minSourceVersionFor("v0.3.12")
	services := map[string]servicedefs.Service{
		"bridge":   {ID: "bridge", HealthPath: "/health", ReadyPath: "/ready", ReadySince: "v0.3.11"},
		"chandler": {ID: "chandler", HealthPath: "/health", ReadyPath: "/ready"},
	}
	problems := readySinceProblems(services, "v0.3.12", nextFloor, func(string) bool { return true })
	if len(problems) != 1 || !strings.Contains(problems[0], "bridge") {
		t.Fatalf("problems against the v0.3.12 fixture = %v, want exactly bridge flagged", problems)
	}
}

func readySinceProblems(services map[string]servicedefs.Service, newest, floor string, declared func(string) bool) []string {
	var problems []string
	for id, svc := range services {
		if svc.ReadySince == "" {
			continue
		}
		if !declared(svc.ReadySince) {
			problems = append(problems, id+" ReadySince "+svc.ReadySince+" is not a declared catalog release")
		}
		if floor != "" && CompareSemver(floor, svc.ReadySince) >= 0 {
			problems = append(problems, id+" ReadySince "+svc.ReadySince+" is at or below min_source_version "+floor+" of "+newest+
				": remove ReadySince so version-blind consumers probe "+svc.ReadyPath)
		}
	}
	return problems
}
