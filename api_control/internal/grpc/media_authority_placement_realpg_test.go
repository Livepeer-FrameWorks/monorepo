//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/placementpolicy"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/protobuf/proto"
)

func TestMediaPlacementObjectPolicy_RealPG(t *testing.T) {
	testMediaPlacementObjectPolicy(t, startCommodoreRealPG(t))
}

func TestMediaPlacementObjectPolicy_RealYugabyte(t *testing.T) {
	testMediaPlacementObjectPolicy(t, placementObjectYugabyte(t, "placement_object"))
}

func placementObjectYugabyte(t *testing.T, name string) *sql.DB {
	t.Helper()
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, name)
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
	return db
}

func testMediaPlacementObjectPolicy(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const streamID = "31000000-0000-4000-8000-000000000011"
	const actorID = "21000000-0000-4000-8000-000000000011"
	tenant, live := commercialAuthorityFixture()
	live.GetLiveStream().StreamId = streamID
	if _, err := db.ExecContext(ctx, "INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES ($1,$2,$3,'object-policy-key','object-policy-playback','object-policy-internal','Policy')", streamID, tenant.TenantId, actorID); err != nil {
		t.Fatal(err)
	}
	store := placementpolicy.NewStore(db)
	tenant.MediaPlacement = &pb.PolicySet{Revision: 1}
	overlay := &pb.PolicySet{Revision: 1, Serve: &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Deny: []*pb.Selector{{Classes: []pb.ClusterClass{pb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL}, Charging: []pb.Charging{pb.Charging_CHARGING_RATED}}}}}}
	for _, command := range []placementpolicy.ApplyInput{
		{Scope: placementpolicy.Scope{TenantID: tenant.TenantId, Kind: "tenant", ID: tenant.TenantId}, Policy: tenant.MediaPlacement},
		{Scope: placementpolicy.Scope{TenantID: tenant.TenantId, Kind: "stream", ID: streamID}, Policy: overlay, ExpectedParentRevision: 1},
	} {
		command.ActorID, command.IdempotencyKey, command.ReviewDigest = actorID, "object-policy", strings.Repeat("a", 64)
		if _, err := store.Apply(ctx, command, func(placementpolicy.Snapshot) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	server := &CommodoreServer{db: db}
	for _, scenario := range []string{"live", "derived artifact", "standalone artifact", "parent changed", "missing parent", "foreign parent"} {
		t.Run(scenario, func(t *testing.T) {
			parent, object := proto.CloneOf(tenant), proto.CloneOf(live)
			want := overlay
			if scenario != "live" && scenario != "parent changed" {
				object.ObjectKind = mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT
				object.Object = &mediapb.MediaObjectAuthority_Artifact{Artifact: &mediapb.ArtifactAuthority{ArtifactId: "artifact", ParentStreamId: streamID}}
			}
			switch scenario {
			case "standalone artifact":
				object.GetArtifact().ParentStreamId = ""
				want = &pb.PolicySet{}
			case "parent changed":
				parent.MediaPlacement.Revision++
			case "missing parent":
				object.GetArtifact().ParentStreamId = "31000000-0000-4000-8000-000000000012"
			case "foreign parent":
				parent.TenantId = "81000000-0000-4000-8000-000000000002"
				object.TenantId = parent.TenantId
			}
			before := proto.CloneOf(object)
			err := server.compileObjectPlacement(ctx, parent, object)
			if scenario == "parent changed" || scenario == "missing parent" || scenario == "foreign parent" {
				if err == nil || !proto.Equal(object, before) {
					t.Fatalf("invalid parent compiled or partially mutated object: %v", err)
				}
				return
			}
			if err != nil || object.SchemaVersion != 2 || object.PlacementTenantRevision != 1 || !proto.Equal(object.MediaPlacement, want) {
				t.Fatalf("incorrect object policy: %v %v", object, err)
			}
		})
	}
	if _, err := db.ExecContext(ctx, "UPDATE commodore.streams SET deleted_at=NOW() WHERE tenant_id=$1 AND id=$2", tenant.TenantId, streamID); err != nil {
		t.Fatal(err)
	}
	if err := server.compileObjectPlacement(ctx, tenant, live); !errors.Is(err, placementpolicy.ErrNotFound) {
		t.Fatalf("deleted parent silently regained default placement: %v", err)
	}
	if n, err := commodoredb.New(db).RetainDeletedStreamMediaPlacementPolicy(ctx, commodoredb.RetainDeletedStreamMediaPlacementPolicyParams{TenantID: tenant.TenantId, StreamID: streamID}); err != nil || n != 0 {
		t.Fatalf("retention overwrote an existing overlay: rows=%d err=%v", n, err)
	}
	artifact := proto.CloneOf(live)
	artifact.ObjectKind = mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT
	artifact.Object = &mediapb.MediaObjectAuthority_Artifact{Artifact: &mediapb.ArtifactAuthority{ArtifactId: "artifact", ParentStreamId: streamID}}
	for _, stage := range []string{"soft deleted", "hard deleted"} {
		if stage == "hard deleted" {
			if _, err := db.ExecContext(ctx, "DELETE FROM commodore.streams WHERE tenant_id=$1 AND id=$2", tenant.TenantId, streamID); err != nil {
				t.Fatal(err)
			}
		}
		if err := server.compileObjectPlacement(ctx, tenant, artifact); err != nil || !proto.Equal(artifact.MediaPlacement, overlay) {
			t.Fatalf("%s artifact lost retained policy: %v %v", stage, artifact.MediaPlacement, err)
		}
		if _, err := store.Read(ctx, placementpolicy.Scope{TenantID: tenant.TenantId, Kind: "stream", ID: streamID}); !errors.Is(err, placementpolicy.ErrNotFound) {
			t.Fatalf("%s parent became publicly manageable: %v", stage, err)
		}
	}
	changedParent := proto.CloneOf(tenant)
	changedParent.MediaPlacement.Revision++
	before := proto.CloneOf(artifact)
	if err := server.compileObjectPlacement(ctx, changedParent, artifact); err == nil || !proto.Equal(before, artifact) {
		t.Fatalf("retained overlay accepted mismatched signed parent or partially mutated: %v", err)
	}
}

func TestMediaPlacementObjectPolicyRetainedDefault_RealPG(t *testing.T) {
	testMediaPlacementObjectPolicyRetainedDefault(t, startCommodoreRealPG(t))
}

func TestMediaPlacementObjectPolicyRetainedDefault_RealYugabyte(t *testing.T) {
	testMediaPlacementObjectPolicyRetainedDefault(t, placementObjectYugabyte(t, "placement_retained_default"))
}

func testMediaPlacementObjectPolicyRetainedDefault(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const streamID = "31000000-0000-4000-8000-000000000021"
	const actorID = "21000000-0000-4000-8000-000000000011"
	tenant, artifact := commercialAuthorityFixture()
	tenant.MediaPlacement = &pb.PolicySet{}
	artifact.ObjectKind = mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT
	artifact.Object = &mediapb.MediaObjectAuthority_Artifact{Artifact: &mediapb.ArtifactAuthority{ArtifactId: "artifact", ParentStreamId: streamID}}
	if _, err := db.ExecContext(ctx, "INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES ($1,$2,$3,'retained-key','retained-playback','retained-internal','Retained')", streamID, tenant.TenantId, actorID); err != nil {
		t.Fatal(err)
	}
	q := commodoredb.New(db)
	params := commodoredb.RetainDeletedStreamMediaPlacementPolicyParams{TenantID: tenant.TenantId, StreamID: streamID}
	if n, err := q.RetainDeletedStreamMediaPlacementPolicy(ctx, params); err != nil || n != 0 {
		t.Fatalf("live stream minted a retention record: rows=%d err=%v", n, err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE commodore.streams SET deleted_at=NOW() WHERE tenant_id=$1 AND id=$2", tenant.TenantId, streamID); err != nil {
		t.Fatal(err)
	}
	server := &CommodoreServer{db: db}
	if err := server.compileObjectPlacement(ctx, tenant, artifact); !errors.Is(err, placementpolicy.ErrNotFound) {
		t.Fatalf("missing retained history silently became default: %v", err)
	}
	foreign := params
	foreign.TenantID = "81000000-0000-4000-8000-000000000002"
	if n, err := q.RetainDeletedStreamMediaPlacementPolicy(ctx, foreign); err != nil || n != 0 {
		t.Fatalf("foreign tenant minted a retention record: rows=%d err=%v", n, err)
	}
	if n, err := q.RetainDeletedStreamMediaPlacementPolicy(ctx, params); err != nil || n != 1 {
		t.Fatalf("default retention: rows=%d err=%v", n, err)
	}
	if n, err := q.RetainDeletedStreamMediaPlacementPolicy(ctx, params); err != nil || n != 0 {
		t.Fatalf("duplicate retention rewrote history: rows=%d err=%v", n, err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM commodore.streams WHERE tenant_id=$1 AND id=$2", tenant.TenantId, streamID); err != nil {
		t.Fatal(err)
	}
	if err := server.compileObjectPlacement(ctx, tenant, artifact); err != nil || !proto.Equal(artifact.MediaPlacement, &pb.PolicySet{}) {
		t.Fatalf("retained default unavailable: %v %v", artifact.MediaPlacement, err)
	}
}
