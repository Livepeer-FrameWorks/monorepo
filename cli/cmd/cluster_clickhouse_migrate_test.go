package cmd

import (
	"errors"
	"io"
	"strings"
	"testing"

	"frameworks/cli/pkg/provisioner"

	"github.com/spf13/cobra"
)

// finishCutover must: stop destination AND source refresh views before the final sync (source-quiesce + retry clean
// slate), RE-VERIFY parity after the sync, start destination views only on a clean verify, roll back a partial
// destination-view startup, and restore the source views on any post-quiesce failure (rollback usability). On success
// the source stays quiesced.
func TestFinishCutover_sourceQuiesceAndRetrySafety(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	m := &chMigrateCtx{db: "periscope"}
	mvs := provisioner.PeriscopeMigrationCatalog.RefreshableMVs
	if len(mvs) < 2 {
		t.Skipf("need >=2 refreshable MVs to exercise partial-start rollback, have %d", len(mvs))
	}

	// build wires recording controls; startFailAt>0 fails the Nth destination-view start.
	var order []string
	build := func(syncErr, verifyErr error, startFailAt int) (func(*cobra.Command, *chMigrateCtx) error, func(*cobra.Command, *chMigrateCtx) error, cutoverViewControls) {
		order = nil
		started := 0
		sync := func(*cobra.Command, *chMigrateCtx) error { order = append(order, "sync"); return syncErr }
		verify := func(*cobra.Command, *chMigrateCtx) error { order = append(order, "verify"); return verifyErr }
		v := cutoverViewControls{
			stopDestAll:   func() error { order = append(order, "stopDest"); return nil },
			stopSourceAll: func() error { order = append(order, "stopSource"); return nil },
			startSourceAll: func() error {
				order = append(order, "restoreSource")
				return nil
			},
			startDestOne: func(mv string) error {
				started++
				order = append(order, "startDest")
				if startFailAt > 0 && started == startFailAt {
					return errors.New("start fail")
				}
				return nil
			},
			assertQuiesced: func() error { order = append(order, "assertQuiesced"); return nil },
			waitQuiesced:   func() error { order = append(order, "waitQuiesced"); return nil },
		}
		return sync, verify, v
	}
	idx := func(s string) int {
		for i, o := range order {
			if o == s {
				return i
			}
		}
		return -1
	}
	count := func(s string) int {
		n := 0
		for _, o := range order {
			if o == s {
				n++
			}
		}
		return n
	}

	// SUCCESS: source + dest stopped before sync; all dest views started; source NOT restored.
	s, vf, vc := build(nil, nil, 0)
	if err := m.finishCutover(cmd, s, vf, vc); err != nil {
		t.Fatalf("clean cutover: %v", err)
	}
	if idx("stopSource") > idx("sync") || idx("stopDest") > idx("sync") {
		t.Fatalf("both source and destination views must be stopped BEFORE the final sync, order: %v", order)
	}
	if count("restoreSource") != 0 {
		t.Fatalf("source views must NOT be restored on success (source is decommissioned), order: %v", order)
	}
	if count("startDest") != len(mvs) {
		t.Fatalf("expected all %d destination views started, got %d", len(mvs), count("startDest"))
	}

	// VERIFY FAILS: no destination view starts; source restored.
	s, vf, vc = build(nil, errors.New("parity mismatch"), 0)
	if err := m.finishCutover(cmd, s, vf, vc); err == nil {
		t.Fatal("a post-sync verification failure must abort cutover")
	}
	if count("startDest") != 0 {
		t.Fatalf("no destination view may start after a verification failure, order: %v", order)
	}
	if count("restoreSource") != 1 {
		t.Fatalf("source views must be restored on a post-quiesce abort, order: %v", order)
	}

	// PARTIAL DESTINATION-START FAILURE: roll back (a second stopDest after startDest) and restore source.
	s, vf, vc = build(nil, nil, 2)
	if err := m.finishCutover(cmd, s, vf, vc); err == nil {
		t.Fatal("a partial destination-view startup failure must abort cutover")
	}
	if count("stopDest") < 2 {
		t.Fatalf("partial destination-view startup must roll back (stop all destination views again), order: %v", order)
	}
	if count("restoreSource") != 1 {
		t.Fatalf("source views must be restored on a partial-start abort, order: %v", order)
	}

	// SOURCE-STOP FAILURE: a partial source-stop must restore the source views BEFORE returning, and must not run the
	// final sync.
	order = nil
	restored := false
	failSource := cutoverViewControls{
		stopDestAll:    func() error { order = append(order, "stopDest"); return nil },
		stopSourceAll:  func() error { order = append(order, "stopSource"); return errors.New("source view 10 stop failed") },
		startSourceAll: func() error { restored = true; order = append(order, "restoreSource"); return nil },
		startDestOne:   func(mv string) error { order = append(order, "startDest"); return nil },
		assertQuiesced: func() error { return nil },
		waitQuiesced:   func() error { return nil },
	}
	okSync := func(*cobra.Command, *chMigrateCtx) error { order = append(order, "sync"); return nil }
	okVer := func(*cobra.Command, *chMigrateCtx) error { order = append(order, "verify"); return nil }
	if err := m.finishCutover(cmd, okSync, okVer, failSource); err == nil {
		t.Fatal("a source-stop failure must abort cutover")
	}
	if !restored {
		t.Fatalf("a partial source-stop must restore the source views before returning, order: %v", order)
	}
	if count("sync") != 0 {
		t.Fatalf("the final sync must not run after a source-stop failure, order: %v", order)
	}

	// quiesceCase drives finishCutover with waitQuiesced/assertQuiesced controlled per-call and asserts abort + restore.
	quiesceCase := func(name string, wait error, asserts []error, wantSync, wantVerify int) {
		order = nil
		ai := 0
		vc := cutoverViewControls{
			stopDestAll:    func() error { order = append(order, "stopDest"); return nil },
			stopSourceAll:  func() error { order = append(order, "stopSource"); return nil },
			startSourceAll: func() error { order = append(order, "restoreSource"); return nil },
			startDestOne:   func(mv string) error { order = append(order, "startDest"); return nil },
			waitQuiesced:   func() error { order = append(order, "waitQuiesced"); return wait },
			assertQuiesced: func() error {
				order = append(order, "assertQuiesced")
				e := asserts[ai]
				ai++
				return e
			},
		}
		if err := m.finishCutover(cmd, okSync, okVer, vc); err == nil {
			t.Fatalf("%s: must abort", name)
		}
		if count("sync") != wantSync {
			t.Fatalf("%s: sync count = %d, want %d, order: %v", name, count("sync"), wantSync, order)
		}
		if count("verify") != wantVerify {
			t.Fatalf("%s: verify count = %d, want %d, order: %v", name, count("verify"), wantVerify, order)
		}
		if count("startDest") != 0 {
			t.Fatalf("%s: no destination view may start, order: %v", name, order)
		}
		if count("restoreSource") != 1 {
			t.Fatalf("%s: source must be restored, order: %v", name, order)
		}
	}
	// Bounded-wait never settles → abort before the sync.
	quiesceCase("wait-never-quiesces", errors.New("timeout"), []error{nil, nil}, 0, 0)
	// PRE-verify assert fails (a view resumed during the sync) → abort after sync, before verify.
	quiesceCase("resumed-during-sync", nil, []error{errors.New("resumed"), nil}, 1, 0)
	// POST-verify assert fails (a view resumed during the non-atomic verify) → abort after verify, before start.
	quiesceCase("resumed-during-verify", nil, []error{nil, errors.New("resumed")}, 1, 1)
}

// validateQuiescedRefreshViews holds a positive predicate: every returned row must be "Disabled" and every required
// view must be present. Any non-Disabled/unknown status or a missing required view fails closed; extra Disabled rows
// (an uncatalogued view the old source legitimately carries) are accepted, and duplicates are not separately rejected.
func TestValidateQuiescedRefreshViews(t *testing.T) {
	mvs := provisioner.PeriscopeMigrationCatalog.RefreshableMVs
	if len(mvs) < 2 {
		t.Skipf("need >=2 refreshable MVs, have %d", len(mvs))
	}
	allDisabled := func() []chViewStatus {
		rows := make([]chViewStatus, len(mvs))
		for i, v := range mvs {
			rows[i] = chViewStatus{view: v, status: "Disabled"}
		}
		return rows
	}
	// Every catalogued view present + Disabled → pass.
	if err := validateQuiescedRefreshViews("dst", mvs, allDisabled()); err != nil {
		t.Fatalf("exact Disabled set must pass, got: %v", err)
	}
	// A missing REQUIRED view (e.g. the destination baseline is incomplete) → fail.
	if err := validateQuiescedRefreshViews("dst", mvs, allDisabled()[:len(mvs)-1]); err == nil {
		t.Fatal("a missing required view must fail closed")
	}
	// WaitingForDependencies is NOT Disabled → fail (a deny-list would have accepted it).
	waiting := allDisabled()
	waiting[0].status = "WaitingForDependencies"
	if err := validateQuiescedRefreshViews("dst", mvs, waiting); err == nil {
		t.Fatal("WaitingForDependencies must fail closed")
	}
	// Unknown/future status → fail.
	unknown := allDisabled()
	unknown[0].status = "SomeNewState"
	if err := validateQuiescedRefreshViews("dst", mvs, unknown); err == nil {
		t.Fatal("an unknown status must fail closed")
	}
	// Empty status (malformed row) → fail.
	empty := allDisabled()
	empty[0].status = ""
	if err := validateQuiescedRefreshViews("dst", mvs, empty); err == nil {
		t.Fatal("an empty status must fail closed")
	}
	// An UNCATALOGUED, RUNNING refreshable view (the source-side hazard: a stale old-schema view still refreshing) →
	// fail. This is the state production can now actually supply, since the query no longer pre-filters by catalog.
	uncataloguedRunning := append(allDisabled(), chViewStatus{view: "stale_uncatalogued_mv", status: "Running"})
	if err := validateQuiescedRefreshViews("source", nil, uncataloguedRunning); err == nil {
		t.Fatal("an uncatalogued RUNNING view must fail closed")
	}
	// An UNCATALOGUED but Disabled view is harmless (the old source may legitimately carry different Disabled views) →
	// pass when nothing is required.
	uncataloguedDisabled := append(allDisabled(), chViewStatus{view: "old_schema_mv", status: "Disabled"})
	if err := validateQuiescedRefreshViews("source", nil, uncataloguedDisabled); err != nil {
		t.Fatalf("an uncatalogued Disabled view must be harmless, got: %v", err)
	}
}

// bestEffortEachView must attempt EVERY catalogued view even when one fails, and aggregate the errors (never stop at the
// first failure leaving the rest untouched).
func TestBestEffortEachView(t *testing.T) {
	mvs := provisioner.PeriscopeMigrationCatalog.RefreshableMVs
	if len(mvs) < 3 {
		t.Skipf("need >=3 refreshable MVs, have %d", len(mvs))
	}
	var attempted []string
	err := bestEffortEachView(func(mv string) error {
		attempted = append(attempted, mv)
		if len(attempted) == 2 {
			return errors.New("boom")
		}
		return nil
	})
	if err == nil {
		t.Fatal("a failed view must surface an error")
	}
	if len(attempted) != len(mvs) {
		t.Fatalf("best-effort must attempt every view (%d), attempted %d", len(mvs), len(attempted))
	}
	if err := bestEffortEachView(func(string) error { return nil }); err != nil {
		t.Fatalf("all-success must aggregate to nil, got %v", err)
	}
}

// buildTableCopyPlan is the cross-version copy/verify core: it must handle a source-absent (new-only) table, exclude
// added destination-only and dropped source-only columns, CAST a type-evolved column to the destination type, and
// finalize AggregateFunction columns for parity.
func TestBuildTableCopyPlan_sourceAbsent(t *testing.T) {
	// artifact_node_copy_events exists only on the destination (new baseline); the source has no such table.
	plan, err := buildTableCopyPlan("artifact_node_copy_events",
		[]chColumn{{name: "event_id", typ: "UUID"}, {name: "node_id", typ: "String"}}, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.sourceExists {
		t.Fatalf("a table absent on the source must yield sourceExists=false, got %+v", plan)
	}
}

func TestBuildTableCopyPlan_sharedColumnsAddDropAndTypeEvolution(t *testing.T) {
	// Destination (new baseline): `ts` evolved DateTime -> DateTime64(3); `has_local_copy` is new (dest-only);
	// `unique_users_state` is an aggregate; `retired` was dropped (present only on the source).
	dst := []chColumn{
		{name: "ts", typ: "DateTime64(3)"},
		{name: "tenant_id", typ: "UUID"},
		{name: "has_local_copy", typ: "UInt8"},
		{name: "unique_users_state", typ: "AggregateFunction(uniqCombined, UInt64)"},
	}
	src := []chColumn{
		{name: "ts", typ: "DateTime"},
		{name: "tenant_id", typ: "UUID"},
		{name: "unique_users_state", typ: "AggregateFunction(uniqCombined, UInt64)"},
		{name: "retired", typ: "String"},
	}
	plan, err := buildTableCopyPlan("stream_event_log", dst, src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !plan.sourceExists {
		t.Fatal("sourceExists must be true")
	}
	wantInsert := []string{"`ts`", "`tenant_id`", "`unique_users_state`"}
	if strings.Join(plan.insertCols, ",") != strings.Join(wantInsert, ",") {
		t.Fatalf("insertCols = %v, want %v (dest-only and source-only columns excluded)", plan.insertCols, wantInsert)
	}
	wantSelect := "CAST(`ts` AS DateTime64(3)), `tenant_id`, `unique_users_state`"
	if strings.Join(plan.selectExprs, ", ") != wantSelect {
		t.Fatalf("selectExprs = %q, want %q (type-evolved column cast to destination type)", strings.Join(plan.selectExprs, ", "), wantSelect)
	}
	// Verify parity: destination hashes the aggregate finalized; the source normalizes the evolved column to the
	// destination type AND finalizes the aggregate, so a matched copy fingerprints equal.
	wantDstHash := "`ts`, `tenant_id`, finalizeAggregation(`unique_users_state`)"
	wantSrcHash := "CAST(`ts` AS DateTime64(3)), `tenant_id`, finalizeAggregation(`unique_users_state`)"
	if plan.dstHashArgs != wantDstHash {
		t.Fatalf("dstHashArgs = %q, want %q", plan.dstHashArgs, wantDstHash)
	}
	if plan.srcHashArgs != wantSrcHash {
		t.Fatalf("srcHashArgs = %q, want %q", plan.srcHashArgs, wantSrcHash)
	}
}

func TestBuildTableCopyPlan_noCommonColumns(t *testing.T) {
	if _, err := buildTableCopyPlan("t", []chColumn{{name: "a", typ: "UInt8"}}, []chColumn{{name: "b", typ: "UInt8"}}); err == nil {
		t.Fatal("a table with no shared columns must be an error (cannot copy cross-version)")
	}
}

// chClientScript must stage SQL to a temp file and run it via --queries-file —
// never --query — so the source credentials embedded in remote(...) never reach
// the destination's process argv, and must clean up via trap on EXIT.
func TestChClientScript_stagesQueriesFileNotArgv(t *testing.T) {
	m := &chMigrateCtx{db: "periscope", dstPort: 9000, user: "frameworks", pass: "s3cret"}
	sql := "SELECT * FROM remote('old:9000', periscope, t, 'frameworks', 's3cret')"
	script := m.chClientScript(sql)

	if strings.Contains(script, "--query '") || strings.Contains(script, "--query \"") {
		t.Fatalf("script must not pass SQL via --query (argv leak):\n%s", script)
	}
	if !strings.Contains(script, "--queries-file") {
		t.Fatalf("script must run via --queries-file:\n%s", script)
	}
	if !strings.Contains(script, "trap 'rm -f \"$f\"' EXIT") {
		t.Fatalf("script must clean up the temp file on EXIT:\n%s", script)
	}
	// The SQL (and thus the source creds) belongs in the heredoc body, not argv.
	if !strings.Contains(script, sql) {
		t.Fatalf("SQL should be written into the staged file:\n%s", script)
	}
	if !strings.Contains(script, "CLICKHOUSE_PASSWORD=") {
		t.Fatalf("destination password must ride the env, not argv:\n%s", script)
	}
}

// With no destination password, the env prefix must be omitted entirely.
func TestChClientScript_noPasswordNoEnvPrefix(t *testing.T) {
	m := &chMigrateCtx{db: "periscope", dstPort: 9000, user: "default"}
	if strings.Contains(m.chClientScript("SELECT 1"), "CLICKHOUSE_PASSWORD=") {
		t.Fatalf("no env prefix expected when password is empty")
	}
}

// destinationOnlyPartitions drives sync's stale-partition reconciliation: it must return exactly the destination
// partitions with no surviving source counterpart, so a partition that TTL-expired on the source after an earlier copy
// is dropped and verify can converge.
func TestDestinationOnlyPartitions(t *testing.T) {
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}
	cases := []struct {
		name             string
		src, dst, expect []string
	}{
		// The blocker scenario: the source fully expired a partition (empty source), the earlier-copied destination
		// partition survives — it MUST be dropped so the destination converges to the (now empty) source.
		{"empty source, stale destination", nil, []string{"202401", "202402"}, []string{"202401", "202402"}},
		// Mixed: one shared partition (re-copied by REPLACE), one destination-only (dropped).
		{"partial overlap", []string{"202402"}, []string{"202401", "202402"}, []string{"202401"}},
		// Destination is a subset of the source — nothing stale to drop.
		{"destination subset of source", []string{"202401", "202402"}, []string{"202402"}, nil},
		// A brand-new destination (nothing copied yet) — the source loop will populate it; nothing to drop.
		{"empty destination", []string{"202401"}, nil, nil},
		{"both empty", nil, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := destinationOnlyPartitions(tc.src, tc.dst); !eq(got, tc.expect) {
				t.Fatalf("destinationOnlyPartitions(%v, %v) = %v, want %v", tc.src, tc.dst, got, tc.expect)
			}
		})
	}
}
