//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/protobuf/proto"
)

func TestMediaPlacementRepository_RealPG(t *testing.T) {
	testMediaPlacementRepository(t, startCommodoreRealPG(t))
}

func TestMediaPlacementRepository_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "commodore_placement")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	baseline, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(baseline)); err != nil {
		t.Fatal(err)
	}
	testMediaPlacementRepository(t, db)
}

func testMediaPlacementRepository(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-4000-8000-000000000071"
	const otherID = "10000000-0000-4000-8000-000000000072"
	const streamID = "30000000-0000-4000-8000-000000000071"
	const userID = "20000000-0000-4000-8000-000000000071"
	store := placementpolicy.NewStore(db)
	scope := placementpolicy.Scope{TenantID: tenantID, Kind: "tenant", ID: tenantID}
	streamScope := placementpolicy.Scope{TenantID: tenantID, Kind: "stream", ID: streamID}
	snapshot, err := store.Read(ctx, scope)
	if err != nil || snapshot.Own.GetRevision() != 0 || snapshot.Active != nil || snapshot.Status != "not_configured" {
		t.Fatalf("default read: %+v %v", snapshot, err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_placement_policies WHERE tenant_id=$1`, tenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("read created state: %d %v", count, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES ($1,$2,$3,'placement-key','placement-playback','placement-stream','Placement')`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	allowReview := func(placementpolicy.Snapshot) error { return nil }
	neverReview := func(placementpolicy.Snapshot) error {
		return errors.New("expired review must not run for a committed retry")
	}
	command := placementpolicy.ApplyInput{
		Scope: scope, ActorID: userID, IdempotencyKey: "first", ReviewDigest: strings.Repeat("a", 64),
		Policy: &placementpb.PolicySet{Revision: 1, Serve: &placementpb.Rules{SchemaVersion: 1,
			Constraints: &placementpb.Constraints{Deny: []*placementpb.Selector{{Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL}, Charging: []placementpb.Charging{placementpb.Charging_CHARGING_RATED}}}},
		}},
	}
	first, err := store.Apply(ctx, command, allowReview)
	if err != nil || first.Revision != 1 || first.RolloutStatus != "pending" {
		t.Fatalf("first apply: %+v %v", first, err)
	}
	snapshot, err = store.Read(ctx, scope)
	if err != nil || snapshot.Own.GetRevision() != 1 || snapshot.Active != nil || snapshot.Status != "pending" {
		t.Fatalf("stored vs active: %+v %v", snapshot, err)
	}
	repeated, err := store.Apply(ctx, command, neverReview)
	if err != nil || repeated.Revision != first.Revision || !repeated.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("ambiguous response recovery: %+v %v", repeated, err)
	}
	changed := command
	changed.Policy = &placementpb.PolicySet{Revision: 1}
	if _, err := store.Apply(ctx, changed, allowReview); !errors.Is(err, placementpolicy.ErrIdempotencyConflict) {
		t.Fatalf("key reuse with changed intent: %v", err)
	}
	changed = command
	changed.ActorID = "another-actor"
	if _, err := store.Apply(ctx, changed, allowReview); !errors.Is(err, placementpolicy.ErrIdempotencyConflict) {
		t.Fatalf("actor substitution: %v", err)
	}
	if _, err := store.Change(ctx, placementpolicy.Scope{TenantID: otherID, Kind: "tenant", ID: otherID}, command.IdempotencyKey); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-tenant receipt disclosed: %v", err)
	}
	if _, err := store.Read(ctx, placementpolicy.Scope{TenantID: otherID, Kind: "stream", ID: streamID}); !errors.Is(err, placementpolicy.ErrNotFound) {
		t.Fatalf("cross-tenant stream disclosed: %v", err)
	}

	streamCommand := placementpolicy.ApplyInput{Scope: streamScope, ActorID: userID, IdempotencyKey: "overlay", ExpectedParentRevision: 1, ReviewDigest: strings.Repeat("b", 64), Policy: &placementpb.PolicySet{Revision: 1}}
	if _, err := store.Apply(ctx, streamCommand, allowReview); err != nil {
		t.Fatal(err)
	}
	streamSnapshot, err := store.Read(ctx, streamScope)
	if err != nil || streamSnapshot.Parent.GetRevision() != 1 || streamSnapshot.Own.GetRevision() != 1 {
		t.Fatalf("overlay state: %+v %v", streamSnapshot, err)
	}

	// Every writer races the same CAS. Only one may create revision 2 and its outbox row.
	var wait sync.WaitGroup
	results := make(chan error, 6)
	for i := range 6 {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			racing := command
			racing.ExpectedRevision = 1
			racing.IdempotencyKey = fmt.Sprintf("race-%d", i)
			racing.Policy = &placementpb.PolicySet{Revision: 2}
			_, raceErr := store.Apply(ctx, racing, allowReview)
			results <- raceErr
		}(i)
	}
	wait.Wait()
	close(results)
	successes := 0
	for raceErr := range results {
		if raceErr == nil {
			successes++
		} else if !errors.Is(raceErr, placementpolicy.ErrRevisionConflict) {
			t.Fatal(raceErr)
		}
	}
	if successes != 1 {
		t.Fatalf("CAS winners=%d", successes)
	}
	staleOverlay := streamCommand
	staleOverlay.IdempotencyKey = "stale-parent"
	staleOverlay.ExpectedRevision = 1
	staleOverlay.Policy = &placementpb.PolicySet{Revision: 2}
	if _, err := store.Apply(ctx, staleOverlay, allowReview); !errors.Is(err, placementpolicy.ErrRevisionConflict) {
		t.Fatalf("stale parent applied: %v", err)
	}
	if _, err := store.Apply(ctx, command, neverReview); err != nil {
		t.Fatalf("old successful retry failed after later change: %v", err)
	}
	old, err := store.Change(ctx, scope, command.IdempotencyKey)
	if err != nil || old.RolloutStatus != "superseded" {
		t.Fatalf("superseded receipt: %+v %v", old, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_placement_changes WHERE tenant_id=$1 AND scope_kind='tenant'`, tenantID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("CAS duplicate receipts: %d %v", count, err)
	}
	// Both applied revisions refresh the same tenant authority, so they fold into
	// one obligation whose revision counts them.
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(sum(revision), 0) FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND lane='event' AND last_source_event_id LIKE 'placement:tenant:%'`, tenantID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("CAS duplicate outbox: %d %v", count, err)
	}

	// A failed refresh enqueue must roll back both intent and its success receipt.
	if _, err := db.ExecContext(ctx, `ALTER TABLE commodore.media_authority_refresh_obligations ADD CONSTRAINT placement_test_outbox_failure CHECK (last_source_event_id <> 'placement:tenant:10000000-0000-4000-8000-000000000071:3')`); err != nil {
		t.Fatal(err)
	}
	failing := command
	failing.ExpectedRevision, failing.IdempotencyKey = 2, "outbox-failure"
	failing.Policy = proto.CloneOf(command.Policy)
	failing.Policy.Revision = 3
	if _, err := store.Apply(ctx, failing, allowReview); err == nil {
		t.Fatal("injected outbox failure was ignored")
	}
	snapshot, err = store.Read(ctx, scope)
	if err != nil || snapshot.Own.GetRevision() != 2 {
		t.Fatalf("failed outbox left committed policy: %+v %v", snapshot, err)
	}
	if _, err := store.Change(ctx, scope, failing.IdempotencyKey); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed outbox left success receipt: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE commodore.streams SET deleted_at=NOW() WHERE tenant_id=$1 AND id=$2`, tenantID, streamID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(ctx, streamScope); !errors.Is(err, placementpolicy.ErrNotFound) {
		t.Fatalf("deleted stream editable: %v", err)
	}
}
