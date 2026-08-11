package cmd

import (
	"context"
	"errors"
	"testing"

	"frameworks/cli/internal/releases"
	"frameworks/cli/pkg/provisioner"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

// TestFoldedBelowAllFloors covers the GREENFIELD/fresh exemption and the canonical STRICTLY-BELOW fold rule: a data
// migration strictly below a database's `_schema_baseline` floor was folded into the baseline the database was born
// from, so a baseline-born (fresh) database is exempt. A migration at/above any owning floor, or a database with no
// marker ("" — an existing pre-baseline cluster), is NOT universally folded and must still be checked against the ledger.
func TestFoldedBelowAllFloors(t *testing.T) {
	cases := []struct {
		name       string
		introduced string
		floors     map[string]string
		want       bool
	}{
		{"strictly below floor is folded (fresh baseline-born)", "v0.3.0", map[string]string{"db": "v0.4.0"}, true},
		{"at floor is NOT folded (strictly-below rule)", "v0.4.0", map[string]string{"db": "v0.4.0"}, false},
		{"above floor is not folded", "v0.5.0", map[string]string{"db": "v0.4.0"}, false},
		{"no marker (pre-baseline cluster) never folds", "v0.3.0", map[string]string{"db": ""}, false},
		{"no owning databases does not fold", "v0.3.0", map[string]string{}, false},
		{"must be below EVERY owning floor", "v0.3.0", map[string]string{"a": "v0.4.0", "b": "v0.2.0"}, false},
	}
	for _, tc := range cases {
		if got := foldedBelowAllFloors(tc.introduced, tc.floors); got != tc.want {
			t.Errorf("%s: foldedBelowAllFloors(%q, %v) = %v, want %v", tc.name, tc.introduced, tc.floors, got, tc.want)
		}
	}
}

// TestDataMigrationLiveStatus covers the per-requirement classification against durable evidence: unresolvable owning
// database fails closed; a folded migration is satisfied; a preserved database must have it StatusCompleted or it blocks.
func TestDataMigrationLiveStatus(t *testing.T) {
	const id = "backfill_x"
	completed := string(datamigrate.StatusCompleted)

	if got := dataMigrationLiveStatus("purser", id, "v0.3.0", dbMigrationState{hasDB: false}); got.FetchError == nil {
		t.Fatalf("no owning database must fail closed (FetchError), got %+v", got)
	}
	folded := dataMigrationLiveStatus("purser", id, "v0.3.0", dbMigrationState{hasDB: true, floors: map[string]string{"purser": "v0.4.0"}})
	if folded.Status != datamigrate.StatusCompleted || folded.FetchError != nil {
		t.Fatalf("a strictly-below-floor migration must be folded/completed, got %+v", folded)
	}
	preservedState := func(status string) dbMigrationState {
		return dbMigrationState{hasDB: true, floors: map[string]string{"purser": "v0.2.0"}, ledgers: map[string]provisioner.DataMigrationLedger{"purser": {Exists: true, Statuses: map[string]string{id: status}}}}
	}
	if got := dataMigrationLiveStatus("purser", id, "v0.3.0", preservedState(completed)); got.Status != datamigrate.StatusCompleted || got.FetchError != nil {
		t.Fatalf("a completed migration on a preserved DB must pass, got %+v", got)
	}
	if got := dataMigrationLiveStatus("purser", id, "v0.3.0", preservedState("running")); got.Status == datamigrate.StatusCompleted {
		t.Fatalf("an incomplete migration on a preserved DB must not report completed, got %+v", got)
	}
	unrecorded := dbMigrationState{hasDB: true, floors: map[string]string{"purser": "v0.2.0"}, ledgers: map[string]provisioner.DataMigrationLedger{"purser": {Exists: true, Statuses: map[string]string{}}}}
	if got := dataMigrationLiveStatus("purser", id, "v0.3.0", unrecorded); got.Status == datamigrate.StatusCompleted {
		t.Fatalf("an unrecorded migration on a preserved DB must not report completed, got %+v", got)
	}
}

// TestProvisionDataMigrationStateSource_WindowBeforeIO pins that PreDeployBlockers applies the release window BEFORE the
// state source is invoked, so no database I/O (stateFor) runs for a target-line or not-yet-due requirement.
// stateFor fatals if invoked for either excluded service; only the in-window "purser" requirement may reach it.
func TestProvisionDataMigrationStateSource_WindowBeforeIO(t *testing.T) {
	ctx := context.Background()
	const target = "v0.5.0"

	reqs := []datamigrate.Requirement{
		{ID: "backfill_x", Service: "purser", IntroducedIn: "v0.3.0", RequiredBeforeVersion: "v0.4.0"}, // in window
		{ID: "target_mig", Service: "targetline", IntroducedIn: "v0.5.0"},                              // target-line → excluded
		{ID: "notyet_mig", Service: "notyet", IntroducedIn: "v0.3.0", RequiredBeforeVersion: "v0.9.0"}, // not yet due → excluded
	}
	introduced := map[string]string{}
	for _, r := range reqs {
		introduced[dataMigrationKey(r.Service, r.ID)] = r.IntroducedIn
	}

	stateFor := func(service string) (dbMigrationState, error) {
		if service != "purser" {
			t.Fatalf("state read for out-of-window service %q — the release window must be applied before any I/O", service)
		}
		return dbMigrationState{hasDB: true, floors: map[string]string{"purser": "v0.2.0"}, ledgers: map[string]provisioner.DataMigrationLedger{
			"purser": {Exists: true, Statuses: map[string]string{"backfill_x": string(datamigrate.StatusCompleted)}},
		}}, nil
	}

	src := provisionDataMigrationStateSource(introduced, stateFor)
	blockers, err := datamigrate.PreDeployBlockers(ctx, src, reqs, target, releases.CompareSemver, releases.BaseVersion)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blockers) != 0 {
		t.Fatalf("the in-window migration is complete; expected no blockers, got %d", len(blockers))
	}
}

// TestProvisionDataMigration_DBInfraPendingLazy pins that the DB-infra-pending refusal (modeled here as a stateFor that
// always errors) fires ONLY when an in-window migration actually needs database I/O — not merely because the catalog is
// non-empty. With only excluded (target-line / not-yet-due) requirements, stateFor is never invoked and there is no
// refusal; add an in-window requirement and it blocks.
func TestProvisionDataMigration_DBInfraPendingLazy(t *testing.T) {
	ctx := context.Background()
	const target = "v0.5.0"

	stateFor := func(service string) (dbMigrationState, error) {
		return dbMigrationState{}, errors.New("database infrastructure not initialized yet")
	}

	excluded := []datamigrate.Requirement{
		{ID: "target_mig", Service: "targetline", IntroducedIn: "v0.5.0"},                              // target-line
		{ID: "notyet_mig", Service: "notyet", IntroducedIn: "v0.3.0", RequiredBeforeVersion: "v0.9.0"}, // not yet due
	}
	introduced := map[string]string{}
	for _, r := range excluded {
		introduced[dataMigrationKey(r.Service, r.ID)] = r.IntroducedIn
	}
	src := provisionDataMigrationStateSource(introduced, stateFor)
	if blockers, err := datamigrate.PreDeployBlockers(ctx, src, excluded, target, releases.CompareSemver, releases.BaseVersion); err != nil || len(blockers) != 0 {
		t.Fatalf("only out-of-window requirements must not trigger the DB-infra-pending refusal, got blockers=%d err=%v", len(blockers), err)
	}

	inWindow := append(excluded, datamigrate.Requirement{ID: "backfill_x", Service: "purser", IntroducedIn: "v0.3.0", RequiredBeforeVersion: "v0.4.0"})
	introduced[dataMigrationKey("purser", "backfill_x")] = "v0.3.0"
	src = provisionDataMigrationStateSource(introduced, stateFor)
	if blockers, err := datamigrate.PreDeployBlockers(ctx, src, inWindow, target, releases.CompareSemver, releases.BaseVersion); err != nil || len(blockers) != 1 {
		t.Fatalf("an in-window migration with DB infra pending must block, got blockers=%d err=%v", len(blockers), err)
	}
}
