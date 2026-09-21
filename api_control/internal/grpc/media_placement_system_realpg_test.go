//go:build schema_verify

package grpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// TestMediaPlacementSystemApply_RealPG proves the transaction-aware system path
// commits or rolls back with its caller, records a receipt with a server-computed
// review digest and deterministic key, is idempotent, and keeps custom rules
// unless the declarative owner replaces them.
func TestMediaPlacementSystemApply_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	const tenantID = "10000000-0000-4000-8000-000000000081"
	const otherTenantID = "10000000-0000-4000-8000-000000000082"
	const streamID = "30000000-0000-4000-8000-000000000081"
	const userID = "20000000-0000-4000-8000-000000000081"
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES ($1,$2,$3,'system-key','system-playback','system-stream','System')`, streamID, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	scope := placementpolicy.Scope{TenantID: tenantID, Kind: "stream", ID: streamID}
	store := placementpolicy.NewStore(db)
	location := placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationRestricted,
		Clusters:     []placementpolicy.SourceLocationCluster{{ClusterID: "edge-b"}, {ClusterID: "edge-a", NodeIDs: []string{"node-1"}}},
		AvoidNodeIDs: []string{"node-9"},
	}
	systemApply := func(location placementpolicy.SourceLocation, replaceCustom, commit bool, validate func(placementpolicy.Snapshot, *placementpb.PolicySet) error) (placementpolicy.SystemApplyResult, error) {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback() //nolint:errcheck // committed below when requested
		result, err := placementpolicy.ApplySystem(ctx, tx, placementpolicy.SystemApplyInput{
			Scope: scope, ActorID: userID, Validate: validate,
			Update: func(own *placementpb.PolicySet) ([]*placementpb.VerbUpdate, error) {
				return placementpolicy.SourceLocationUpdate(own, location, replaceCustom)
			},
		})
		if err == nil && commit {
			err = tx.Commit()
		}
		return result, err
	}
	countChanges := func() int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_placement_changes WHERE tenant_id=$1 AND scope_kind='stream' AND scope_id=$2`, tenantID, streamID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	if _, err := systemApply(location, false, false, nil); err != nil {
		t.Fatal(err)
	}
	if snapshot, err := store.Read(ctx, scope); err != nil || snapshot.Own.GetRevision() != 0 || countChanges() != 0 {
		t.Fatalf("rolled-back caller transaction left placement state: %+v %v", snapshot, err)
	}

	first, err := systemApply(location, false, true, nil)
	if err != nil || !first.Changed || first.Receipt.Revision != 1 || first.Receipt.ActorID != userID || !strings.HasPrefix(first.Receipt.IdempotencyKey, "system-") || len(first.Receipt.ReviewDigest) != 64 {
		t.Fatalf("first system apply: %+v %v", first.Receipt, err)
	}
	snapshot, err := store.Read(ctx, scope)
	if err != nil || snapshot.Status != "pending" {
		t.Fatalf("stored change: %+v %v", snapshot, err)
	}
	if got := placementpolicy.SourceLocationOf(snapshot.Own); fmt.Sprint(got.Clusters) != "[{edge-a [node-1]} {edge-b []}]" || fmt.Sprint(got.AvoidNodeIDs) != "[node-9]" {
		t.Fatalf("stored source location: %+v", got)
	}
	var refreshes int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_refresh_obligations WHERE tenant_id=$1 AND last_reason=$2`, tenantID, "media_object:live_stream:"+streamID+":media_placement_changed").Scan(&refreshes); err != nil || refreshes != 1 {
		t.Fatalf("authority refresh obligation = %d, err %v", refreshes, err)
	}

	again, err := systemApply(location, false, true, nil)
	if err != nil || again.Changed || countChanges() != 1 {
		t.Fatalf("unchanged location wrote a receipt: %+v %v", again, err)
	}

	refused := errors.New("refused by validation")
	if _, err := systemApply(placementpolicy.SourceLocation{Mode: placementpolicy.SourceLocationAny}, false, true, func(placementpolicy.Snapshot, *placementpb.PolicySet) error { return refused }); !errors.Is(err, refused) || countChanges() != 1 {
		t.Fatalf("validation refusal: %v", err)
	}

	custom := placementpolicy.ApplyInput{Scope: scope, ActorID: userID, IdempotencyKey: "custom", ReviewDigest: strings.Repeat("c", 64), ExpectedRevision: 1,
		Policy: &placementpb.PolicySet{Revision: 2, Ingest: &placementpb.Rules{SchemaVersion: 1,
			Constraints: &placementpb.Constraints{Allow: &placementpb.SelectorSet{Any: []*placementpb.Selector{{Regions: []string{"eu"}}}}},
			Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{{Id: "near"}}},
		}},
	}
	if _, err := store.Apply(ctx, custom, func(placementpolicy.Snapshot) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := systemApply(location, false, true, nil); !errors.Is(err, placementpolicy.ErrSourceLocationCustom) {
		t.Fatalf("custom rules overwritten by a tenant write: %v", err)
	}
	replaced, err := systemApply(location, true, true, nil)
	if err != nil || !replaced.Changed || replaced.Receipt.Revision != 3 || replaced.Policy.GetIngest().GetPreferences().GetGroups()[0].GetId() != "near" {
		t.Fatalf("declarative replacement: %+v %v", replaced, err)
	}

	otherScope := placementpolicy.Scope{TenantID: otherTenantID, Kind: "stream", ID: streamID}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only assertion
	if _, err := placementpolicy.ApplySystem(ctx, tx, placementpolicy.SystemApplyInput{Scope: otherScope, ActorID: userID, Update: func(*placementpb.PolicySet) ([]*placementpb.VerbUpdate, error) { return nil, nil }}); !errors.Is(err, placementpolicy.ErrNotFound) {
		t.Fatalf("cross-tenant stream placement reachable: %v", err)
	}
}
