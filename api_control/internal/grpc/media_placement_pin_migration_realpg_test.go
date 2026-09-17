//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// TestMediaPlacementPinMigration_RealPG covers the pull-source pin data
// migration: fresh allow, intersection with an existing allow, a disjoint own
// allow replaced by the pins, managed-stream pins, idempotent rerun, Verify, and
// a read-only dry run.
func TestMediaPlacementPinMigration_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const tenantID = "10000000-0000-4000-8000-000000000091"
	const userID = "20000000-0000-4000-8000-000000000091"
	const freshPull = "30000000-0000-4000-8000-000000000091"
	const intersectPull = "30000000-0000-4000-8000-000000000092"
	const managed = "30000000-0000-4000-8000-000000000093"
	const unpinned = "30000000-0000-4000-8000-000000000094"
	const deleted = "30000000-0000-4000-8000-000000000095"
	const disjointPull = "30000000-0000-4000-8000-000000000096"
	insertStream := func(id, mode string, deletedAt bool) {
		t.Helper()
		query := `INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title,ingest_mode,deleted_at)
			VALUES ($1,$2,$3,$4,$5,$6,'Pinned',$7, CASE WHEN $8::boolean THEN NOW() ELSE NULL END)`
		name := "pins-" + id[len(id)-2:]
		if _, err := db.ExecContext(ctx, query, id, tenantID, userID, name, name, name, mode, deletedAt); err != nil {
			t.Fatal(err)
		}
	}
	insertPull := func(id string, pins string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.stream_pull_sources(stream_id,source_uri_enc,enabled,allowed_cluster_ids) VALUES ($1,'enc',TRUE,$2::text[])`, id, pins); err != nil {
			t.Fatal(err)
		}
	}
	insertStream(freshPull, "pull", false)
	insertPull(freshPull, "{edge-b,edge-a}")
	insertStream(intersectPull, "pull", false)
	insertPull(intersectPull, "{edge-a}")
	insertStream(managed, "mist_native", false)
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.stream_mist_sources(stream_id,source_spec,source_kind,placement_count,allowed_cluster_ids) VALUES ($1,'ts-exec:cat','exec',1,'{edge-m}')`, managed); err != nil {
		t.Fatal(err)
	}
	insertStream(unpinned, "pull", false)
	insertPull(unpinned, "{}")
	insertStream(deleted, "pull", true)
	insertPull(deleted, "{edge-a}")
	insertStream(disjointPull, "pull", false)
	insertPull(disjointPull, "{edge-a}")

	store := placementpolicy.NewStore(db)
	intersectScope := placementpolicy.Scope{TenantID: tenantID, Kind: "stream", ID: intersectPull}
	existing := &placementpb.PolicySet{Revision: 1,
		Ingest: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{
			{Regions: []string{"eu"}}, {ClusterIds: []string{"edge-a", "edge-c"}}, {ClusterIds: []string{"edge-c"}},
		}}}},
		Serve: &placementpb.Rules{SchemaVersion: 1, Constraints: &placementpb.Constraints{Deny: []*placementpb.Selector{{Regions: []string{"us"}}}}},
	}
	if _, err := store.Apply(ctx, placementpolicy.ApplyInput{Scope: intersectScope, ActorID: userID, IdempotencyKey: "seed", ReviewDigest: strings.Repeat("a", 64), Policy: existing}, func(placementpolicy.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	disjointRules := &placementpb.PolicySet{Revision: 1,
		Ingest: &placementpb.Rules{SchemaVersion: 1,
			Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{ClusterIds: []string{"edge-z"}}}}},
			Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{{Id: "near"}}},
		},
	}
	if _, err := store.Apply(ctx, placementpolicy.ApplyInput{Scope: placementpolicy.Scope{TenantID: tenantID, Kind: "stream", ID: disjointPull}, ActorID: userID, IdempotencyKey: "seed-disjoint", ReviewDigest: strings.Repeat("b", 64), Policy: disjointRules}, func(placementpolicy.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	countChanges := func() int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_placement_changes WHERE tenant_id=$1`, tenantID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	readOnly := func(fn func(*sql.Tx)) {
		t.Helper()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck // read-only transaction
		fn(tx)
	}
	runAll := func(dryRun bool) datamigrate.Progress {
		t.Helper()
		var total datamigrate.Progress
		checkpoint := []byte(nil)
		for range 10 {
			var progress datamigrate.Progress
			var runErr error
			if dryRun {
				readOnly(func(tx *sql.Tx) {
					progress, runErr = placementpolicy.RunPullSourcePinsToStreamRules(ctx, tx, datamigrate.RunOptions{BatchSize: 2, DryRun: true, Checkpoint: checkpoint})
				})
			} else {
				progress, runErr = placementpolicy.RunPullSourcePinsToStreamRules(ctx, db, datamigrate.RunOptions{BatchSize: 2, Checkpoint: checkpoint})
			}
			if runErr != nil {
				t.Fatal(runErr)
			}
			total.Scanned += progress.Scanned
			total.Changed += progress.Changed
			total.Skipped += progress.Skipped
			checkpoint = progress.Checkpoint
			if progress.Done {
				return total
			}
		}
		t.Fatal("migration did not finish")
		return total
	}

	if planned := runAll(true); planned.Scanned != 4 || planned.Changed != 4 {
		t.Fatalf("dry run plan = %+v, want 4 pinned streams to convert", planned)
	}
	if countChanges() != 2 {
		t.Fatal("dry run wrote placement changes")
	}
	readOnly(func(tx *sql.Tx) {
		if err := placementpolicy.VerifyPullSourcePinsToStreamRules(ctx, tx); err == nil {
			t.Fatal("verify passed before conversion")
		}
	})

	if converted := runAll(false); converted.Scanned != 4 || converted.Changed != 4 {
		t.Fatalf("conversion = %+v", converted)
	}
	readOwn := func(id string) *placementpb.PolicySet {
		t.Helper()
		snapshot, err := store.Read(ctx, placementpolicy.Scope{TenantID: tenantID, Kind: "stream", ID: id})
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.Own
	}
	if got := placementpolicy.SourceLocationOf(readOwn(freshPull)); got.Mode != placementpolicy.SourceLocationRestricted || fmt.Sprint(got.ClusterIDs()) != "[edge-a edge-b]" {
		t.Fatalf("fresh allow = %+v", got)
	}
	if got := placementpolicy.SourceLocationOf(readOwn(managed)); fmt.Sprint(got.ClusterIDs()) != "[edge-m]" {
		t.Fatalf("managed pin = %+v", got)
	}
	intersected := readOwn(intersectPull)
	alternatives := intersected.GetIngest().GetConstraints().GetAllow().GetAny()
	if len(alternatives) != 2 || intersected.GetServe() == nil {
		t.Fatalf("intersection = %v", intersected)
	}
	for _, alternative := range alternatives {
		if fmt.Sprint(alternative.GetClusterIds()) != "[edge-a]" {
			t.Fatalf("intersected alternative = %v", alternative)
		}
	}
	pinsWon := readOwn(disjointPull)
	if got := placementpolicy.SourceLocationOf(pinsWon); got.Mode != placementpolicy.SourceLocationRestricted || fmt.Sprint(got.ClusterIDs()) != "[edge-a]" || pinsWon.GetIngest().GetPreferences().GetGroups()[0].GetId() != "near" {
		t.Fatalf("disjoint own allow must take the pins and keep preferences: %v", pinsWon.GetIngest())
	}
	if own := readOwn(unpinned); own.GetRevision() != 0 {
		t.Fatalf("unpinned stream received rules: %v", own)
	}
	var actor string
	if err := db.QueryRowContext(ctx, `SELECT actor_id FROM commodore.media_placement_changes WHERE tenant_id=$1 AND scope_id=$2`, tenantID, freshPull).Scan(&actor); err != nil || actor != placementpolicy.SystemActorDataMigration {
		t.Fatalf("receipt actor = %q, err %v", actor, err)
	}
	readOnly(func(tx *sql.Tx) {
		if err := placementpolicy.VerifyPullSourcePinsToStreamRules(ctx, tx); err != nil {
			t.Fatalf("verify after conversion: %v", err)
		}
	})

	changes := countChanges()
	if rerun := runAll(false); rerun.Changed != 0 || rerun.Scanned != 4 || countChanges() != changes {
		t.Fatalf("rerun = %+v, changes %d -> %d", rerun, changes, countChanges())
	}
	if planned := runAll(true); planned.Changed != 0 {
		t.Fatalf("dry run after conversion = %+v", planned)
	}
}

// TestMediaPlacementPinMigrationConcurrentLocationEdit_RealPG commits a stream
// source-location edit after the migration listed its candidates and while the
// conversion waits on the placement locks. The conversion must derive its update
// from the pins current under those locks: a moved pin is already expressed, and
// a cleared pin is skipped, instead of applying the listed values.
func TestMediaPlacementPinMigrationConcurrentLocationEdit_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	const tenantID = "10000000-0000-4000-8000-0000000000a1"
	const userID = "20000000-0000-4000-8000-0000000000a1"
	const moved = "30000000-0000-4000-8000-0000000000a1"
	const cleared = "30000000-0000-4000-8000-0000000000a2"
	for _, seed := range []struct{ id, pins string }{{moved, "{edge-a}"}, {cleared, "{edge-b}"}} {
		name := "concurrent-pins-" + seed.id[len(seed.id)-2:]
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title,ingest_mode) VALUES ($1,$2,$3,$4,$5,$6,'Pinned','pull')`, seed.id, tenantID, userID, name, name, name); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.stream_pull_sources(stream_id,source_uri_enc,enabled,allowed_cluster_ids) VALUES ($1,'enc',TRUE,$2::text[])`, seed.id, seed.pins); err != nil {
			t.Fatal(err)
		}
	}
	// Committed policy rows keep the conversion's ensure inserts from waiting on
	// the edit's uncommitted inserts, so the first wait is the tenant row lock.
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.media_placement_policies(tenant_id,scope_kind,scope_id) VALUES ($1,'tenant',$1),($1,'stream',$2),($1,'stream',$3)`, tenantID, moved, cleared); err != nil {
		t.Fatal(err)
	}

	edit, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer edit.Rollback() //nolint:errcheck // committed below
	writeLocation := func(streamID string, location placementpolicy.SourceLocation, pins string) {
		t.Helper()
		if _, applyErr := placementpolicy.ApplySystem(ctx, edit, placementpolicy.SystemApplyInput{
			Scope: placementpolicy.Scope{TenantID: tenantID, Kind: "stream", ID: streamID}, ActorID: userID,
			Update: func(own *placementpb.PolicySet) ([]*placementpb.VerbUpdate, error) {
				return placementpolicy.SourceLocationUpdate(own, location, false)
			},
		}); applyErr != nil {
			t.Fatal(applyErr)
		}
		if _, execErr := edit.ExecContext(ctx, `UPDATE commodore.stream_pull_sources SET allowed_cluster_ids=$2::text[] WHERE stream_id=$1`, streamID, pins); execErr != nil {
			t.Fatal(execErr)
		}
	}
	writeLocation(moved, placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationRestricted, Clusters: []placementpolicy.SourceLocationCluster{{ClusterID: "edge-z"}}}, "{edge-z}")
	writeLocation(cleared, placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationAny}, "{}")

	type runResult struct {
		progress datamigrate.Progress
		err      error
	}
	done := make(chan runResult, 1)
	go func() {
		progress, runErr := placementpolicy.RunPullSourcePinsToStreamRules(ctx, db, datamigrate.RunOptions{BatchSize: 10})
		done <- runResult{progress, runErr}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		var waiting int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case result := <-done:
			t.Fatalf("conversion finished without waiting on the location edit: %+v %v", result.progress, result.err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("conversion never waited on the placement locks")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := edit.Commit(); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.progress.Scanned != 2 || result.progress.Changed != 0 || result.progress.Skipped != 2 {
		t.Fatalf("conversion = %+v, want both listed streams skipped against their current pins", result.progress)
	}

	store := placementpolicy.NewStore(db)
	for streamID, want := range map[string]string{moved: "[edge-z]", cleared: "[]"} {
		snapshot, readErr := store.Read(ctx, placementpolicy.Scope{TenantID: tenantID, Kind: "stream", ID: streamID})
		if readErr != nil {
			t.Fatal(readErr)
		}
		if got := fmt.Sprint(placementpolicy.SourceLocationOf(snapshot.Own).ClusterIDs()); got != want {
			t.Fatalf("stream %s source location = %s, want %s", streamID, got, want)
		}
	}
	var migrationReceipts int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_placement_changes WHERE tenant_id=$1 AND actor_id=$2`, tenantID, placementpolicy.SystemActorDataMigration).Scan(&migrationReceipts); err != nil || migrationReceipts != 0 {
		t.Fatalf("migration receipts = %d, err %v", migrationReceipts, err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only transaction
	if err := placementpolicy.VerifyPullSourcePinsToStreamRules(ctx, tx); err != nil {
		t.Fatalf("verify after concurrent edit: %v", err)
	}
}
