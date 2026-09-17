//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type placementOptionsOwner struct {
	mediaAuthorityTenantSource
	t           *testing.T
	tenantID    string
	entitlement *quartermasterpb.GetTenantEntitlementResponse
	calls       int
}

func (owner *placementOptionsOwner) GetTenantEntitlement(_ context.Context, tenantID string) (*quartermasterpb.GetTenantEntitlementResponse, error) {
	owner.calls++
	if tenantID != owner.tenantID {
		owner.t.Fatal("options requested another tenant's grants")
	}
	return proto.CloneOf(owner.entitlement), nil
}

type placementOptionsInventory struct {
	t        *testing.T
	tenantID string
	calls    int
}

func (inventory *placementOptionsInventory) GetMediaPlacementInventory(_ context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	inventory.calls++
	if req.GetTenantId() != inventory.tenantID || req.GetControlCellId() != "cell-eu" || len(req.GetClusterIds()) != 1 || req.GetClusterIds()[0] != "owned" {
		inventory.t.Fatalf("node inventory requested outside the tenant's owned clusters: %v", req)
	}
	return &quartermasterpb.MediaPlacementInventory{
		TenantId: req.GetTenantId(), ControlCellId: req.GetControlCellId(), ClusterIds: req.GetClusterIds(), Complete: true,
		Nodes: []*quartermasterpb.MediaPlacementInventoryNode{{ClusterId: "owned", NodeId: "owned-node-1", AdmissionEnabled: true}},
	}, nil
}

func TestMediaPlacementOptions_RealPG(t *testing.T) {
	testMediaPlacementOptionsDatabase(t, startCommodoreRealPG(t))
}

func TestMediaPlacementOptions_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "commodore_placement_options")
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
	testMediaPlacementOptionsDatabase(t, db)
}

func testMediaPlacementOptionsDatabase(t *testing.T, db *sql.DB) {
	t.Helper()
	scope, req, entitlement := placementOptionsFixture()
	const actorID = "20000000-0000-4000-8000-000000000071"
	const streamID = "30000000-0000-4000-8000-000000000071"
	// Only the tenant's own cluster has a node inventory lookup; the platform and
	// marketplace clusters in the fixture never reach Quartermaster.
	entitlement.EffectiveAccess[1].ControlCellId = "cell-eu"
	owner := &placementOptionsOwner{t: t, tenantID: scope.TenantID, entitlement: entitlement}
	inventory := &placementOptionsInventory{t: t, tenantID: scope.TenantID}
	server := &CommodoreServer{db: db, authorityTenantSource: owner, placementInventorySource: inventory}
	ctx := context.WithValue(ctxAs(actorID, scope.TenantID, "viewer"), ctxkeys.KeyAuthType, "jwt")
	result, err := server.GetMediaPlacementOptions(ctx, req)
	if err != nil || len(result.GetNodes()) != 8 || owner.calls != 1 || inventory.calls != 1 {
		t.Fatalf("tenant options: %d options, %v", len(result.GetNodes()), err)
	}
	nodeOptions := 0
	for _, option := range result.GetNodes() {
		if option.GetKind() == placementpb.OptionKind_OPTION_KIND_NODE {
			nodeOptions++
			if option.GetId() != "owned-node-1" || option.GetClusterId() != "owned" {
				t.Fatalf("node option = %v", option)
			}
		}
	}
	if nodeOptions != 1 {
		t.Fatalf("node options = %d, want 1", nodeOptions)
	}
	req.Scope = &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: streamID}
	if _, err := server.GetMediaPlacementOptions(ctx, req); status.Code(err) != codes.NotFound || owner.calls != 1 {
		t.Fatalf("missing stream exposed inventory: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.streams(id,tenant_id,user_id,stream_key,playback_id,internal_name,title) VALUES ($1,$2,$3,'placement-option-key','placement-option-playback','placement-option-stream','Options')`, streamID, scope.TenantID, actorID); err != nil {
		t.Fatal(err)
	}
	result, err = server.GetMediaPlacementOptions(ctx, req)
	if err != nil || len(result.GetNodes()) != 8 || owner.calls != 2 {
		t.Fatalf("owned stream options: %v", err)
	}
	foreign := context.WithValue(ctx, ctxkeys.KeyTenantID, "10000000-0000-4000-8000-000000000072")
	if _, err := server.GetMediaPlacementOptions(foreign, req); status.Code(err) != codes.NotFound || owner.calls != 2 {
		t.Fatalf("cross-tenant stream options exposed inventory: %v", err)
	}
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_placement_policies WHERE tenant_id=$1", scope.TenantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("read initialized policy state: %d %v", count, err)
	}
}
