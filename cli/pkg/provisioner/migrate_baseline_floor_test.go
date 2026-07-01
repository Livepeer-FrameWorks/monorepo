package provisioner

import (
	"strings"
	"testing"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

// The durable baseline marker's floor literal (written into the baseline schema
// files) must stay in sync with schemaMigrationBaselineFloor, or a fresh cluster
// would be marked at the wrong floor and the guard would mis-skip/mis-check.
func TestBaselineMarkerFloorMatchesConst(t *testing.T) {
	files := []string{
		"schema/commodore.sql", "schema/foghorn.sql", "schema/navigator.sql",
		"schema/purser.sql", "schema/quartermaster.sql", "clickhouse/periscope.sql",
	}
	for _, f := range files {
		b, err := dbsql.Content.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		s := string(b)
		if !strings.Contains(s, "_schema_baseline") {
			t.Errorf("%s: missing _schema_baseline marker", f)
			continue
		}
		if !strings.Contains(s, "'"+schemaMigrationBaselineFloor+"'") {
			t.Errorf("%s: baseline marker floor != schemaMigrationBaselineFloor (%s)", f, schemaMigrationBaselineFloor)
		}
	}
}

// belowBaselineFloor decides which migrations are folded into the baseline schema
// and never offered. The boundary is strict less-than: the floor version itself
// ships normally.
func TestBelowBaselineFloor(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"v0.2.1", true},
		{"v0.2.65", true},
		{"v0.2.95", true},  // folded — prod has applied it
		{"v0.2.96", false}, // the floor itself is NOT folded — still offered to prod
		{"v0.2.97", false}, // offered to prod on next upgrade
		{"v0.3.0", false},
		{"v1.0.0", false},
	}
	for _, c := range cases {
		if got := belowBaselineFloor(Migration{Version: c.version}); got != c.want {
			t.Errorf("belowBaselineFloor(%s) = %v, want %v (floor=%s)", c.version, got, c.want, schemaMigrationBaselineFloor)
		}
	}
}

// BuildMigrationItems (Postgres) must never emit a migration below the baseline
// floor, even when the target-version cap is high enough to include it.
func TestBuildMigrationItemsExcludesBelowBaselineFloor(t *testing.T) {
	items, err := BuildMigrationItems([]string{"commodore"}, "expand", "v99.0.0")
	if err != nil {
		t.Fatalf("BuildMigrationItems: %v", err)
	}
	for _, it := range items {
		v := it["version"].(string)
		if compareSemver(v, schemaMigrationBaselineFloor) < 0 {
			t.Fatalf("pre-floor migration leaked into items: %s (floor=%s)", v, schemaMigrationBaselineFloor)
		}
	}
}

// BuildClickHouseMigrationItems must likewise never emit a pre-floor migration.
func TestBuildClickHouseMigrationItemsExcludesBelowBaselineFloor(t *testing.T) {
	items, err := BuildClickHouseMigrationItems([]string{"periscope"}, "contract", "v99.0.0")
	if err != nil {
		t.Fatalf("BuildClickHouseMigrationItems: %v", err)
	}
	for _, it := range items {
		v := it["version"].(string)
		if compareSemver(v, schemaMigrationBaselineFloor) < 0 {
			t.Fatalf("pre-floor migration leaked into items: %s (floor=%s)", v, schemaMigrationBaselineFloor)
		}
	}
}

// belowFloorItemsFromList returns only migrations strictly below the floor, with
// the logical-source→physical-target remap applied.
func TestBelowFloorItemsFromList(t *testing.T) {
	all := []Migration{
		{Database: "foghorn", Version: "v0.2.50", Phase: "expand", Sequence: 1, Filename: "001_a.sql", Checksum: "a"},
		{Database: "foghorn", Version: "v0.2.96", Phase: "expand", Sequence: 1, Filename: "001_b.sql", Checksum: "b"}, // at floor → not folded
		{Database: "other", Version: "v0.2.10", Phase: "expand", Sequence: 1, Filename: "001_c.sql", Checksum: "c"},   // unconfigured db
	}
	got := belowFloorItemsFromList(all, []SchemaDatabase{{Name: "foghorn_eu", SourceName: "foghorn"}})
	if len(got) != 1 {
		t.Fatalf("want 1 below-floor item, got %d: %v", len(got), got)
	}
	if got[0]["db"] != "foghorn_eu" || got[0]["version"] != "v0.2.50" {
		t.Fatalf("remap/version wrong: %v", got[0])
	}
}

// The below-floor gap = expected below-floor migrations absent from the ledger.
func TestBelowFloorGapDetectsMissing(t *testing.T) {
	expected := belowFloorItemsFromList(
		[]Migration{{Database: "periscope", Version: "v0.2.82", Phase: "contract", Sequence: 1, Filename: "001_swap.sql", Checksum: "x"}},
		[]SchemaDatabase{{Name: "periscope"}},
	)
	if got := diffExpectedAgainstLedger(expected, map[string][]LedgerEntry{"periscope": {}}); len(got) != 1 {
		t.Fatalf("empty ledger: want 1 missing, got %d", len(got))
	}
	applied := map[string][]LedgerEntry{"periscope": {{Version: "v0.2.82", Phase: "contract", Seq: 1, Checksum: "x"}}}
	if got := diffExpectedAgainstLedger(expected, applied); len(got) != 0 {
		t.Fatalf("applied ledger: want 0 missing, got %d: %v", len(got), got)
	}
}

// belowFloorGap is DURABLE-marker-based: a database with a baseline marker floor M
// has everything < M folded into its baseline (skip); a database without a marker is
// an existing in-place cluster whose ledger must actually contain the migrations.
func TestBelowFloorGapMarkerAware(t *testing.T) {
	expected := belowFloorItemsFromList(
		[]Migration{
			{Database: "commodore", Version: "v0.2.50", Phase: "expand", Sequence: 1, Filename: "001_a.sql", Checksum: "a"},
			{Database: "commodore", Version: "v0.2.80", Phase: "expand", Sequence: 1, Filename: "001_b.sql", Checksum: "b"},
		},
		[]SchemaDatabase{{Name: "commodore"}},
	)
	emptyLedger := map[string][]LedgerEntry{"commodore": {}}

	// Fresh cluster: marker at floor + empty ledger → everything below floor folded
	// → no gap. (Empty ledger alone is NOT proof — the marker is.)
	fresh := map[string]string{"commodore": schemaMigrationBaselineFloor}
	if got := belowFloorGap(expected, emptyLedger, fresh); len(got) != 0 {
		t.Fatalf("fresh (marker): want 0, got %d: %v", len(got), got)
	}

	// No marker + empty ledger (e.g. dropped ledger on a stale cluster): NOT skipped
	// → both below-floor migrations reported. This is the hole the marker closes.
	if got := belowFloorGap(expected, emptyLedger, nil); len(got) != 2 {
		t.Fatalf("no marker + empty ledger: want 2 (fail-closed), got %d: %v", len(got), got)
	}

	// Existing in-place cluster, no marker, complete ledger → no gap.
	complete := map[string][]LedgerEntry{"commodore": {
		{Version: "v0.2.50", Phase: "expand", Seq: 1, Checksum: "a"},
		{Version: "v0.2.80", Phase: "expand", Seq: 1, Checksum: "b"},
	}}
	if got := belowFloorGap(expected, complete, nil); len(got) != 0 {
		t.Fatalf("existing complete: want 0, got %d: %v", len(got), got)
	}

	// Stale existing cluster, no marker, missing v0.2.80 → flagged.
	stale := map[string][]LedgerEntry{"commodore": {{Version: "v0.2.50", Phase: "expand", Seq: 1, Checksum: "a"}}}
	if got := belowFloorGap(expected, stale, nil); len(got) != 1 || got[0].Version != "v0.2.80" {
		t.Fatalf("stale: want 1 gap at v0.2.80, got %d: %v", len(got), got)
	}

	// Born from an OLDER baseline (marker v0.2.60): < v0.2.60 folded (v0.2.50 skipped),
	// but [v0.2.60, floor) still checked — v0.2.80 missing from ledger → flagged.
	oldMarker := map[string]string{"commodore": "v0.2.60"}
	if got := belowFloorGap(expected, emptyLedger, oldMarker); len(got) != 1 || got[0].Version != "v0.2.80" {
		t.Fatalf("old-baseline marker: want 1 gap at v0.2.80, got %d: %v", len(got), got)
	}
}

// isClickHouseUnknownTable must swallow only a missing bookkeeping table, not an
// unrelated failure (missing database/user, auth) that should surface as an error.
func TestIsClickHouseUnknownTable(t *testing.T) {
	cases := []struct {
		stderr string
		want   bool
	}{
		{"Code: 60. DB::Exception: UNKNOWN_TABLE ...", true},
		{"Table periscope._migrations does not exist", true},
		{"Table periscope._schema_baseline does not exist", true},
		{"Database periscope does not exist", false}, // missing DB, not swallowed
		{"Authentication failed: password is incorrect", false},
		{"Connection refused", false},
	}
	for _, c := range cases {
		if got := isClickHouseUnknownTable(c.stderr); got != c.want {
			t.Errorf("isClickHouseUnknownTable(%q) = %v, want %v", c.stderr, got, c.want)
		}
	}
}

func TestParseClickHouseLedgerTSV(t *testing.T) {
	entries, err := parseClickHouseLedgerTSV("v0.2.65\texpand\t2\tdef456\nv0.2.82\tcontract\t1\tabc123\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if entries[1].Version != "v0.2.82" || entries[1].Phase != "contract" || entries[1].Seq != 1 || entries[1].Checksum != "abc123" {
		t.Fatalf("row parsed wrong: %+v", entries[1])
	}
}

func TestFormatBelowFloorRefusal(t *testing.T) {
	msg := FormatBelowFloorRefusal("clickhouse", []MigrationKey{
		{Database: "periscope", Version: "v0.2.82", Phase: "contract", Seq: 1, Filename: "001_swap.sql"},
	})
	for _, want := range []string{"clickhouse", schemaMigrationBaselineFloor, "periscope/v0.2.82/contract/001_swap.sql"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("refusal missing %q:\n%s", want, msg)
		}
	}
}
