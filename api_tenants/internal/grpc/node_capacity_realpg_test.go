//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestNodeCapacityManagement_RealPG(t *testing.T) {
	verifyNodeCapacityManagement(t, openQuartermasterRealPG(t, "node-capacity"))
}

func TestNodeCapacityManagement_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "quartermaster_node_capacity", "quartermaster")
	if !ok {
		t.Skip("requires shared Yugabyte contract fixture")
	}
	verifyNodeCapacityManagement(t, db)
}

func verifyNodeCapacityManagement(t *testing.T, db *sql.DB) {
	t.Helper()
	const tenant = "11111111-1111-4111-8111-111111111181"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO quartermaster.tenants (id, name) VALUES ($1::uuid, 'Capacity owner')`, tenant)
	exec(`INSERT INTO quartermaster.infrastructure_clusters (cluster_id, cluster_name, cluster_type, base_url, cluster_class, owner_tenant_id, control_cell_id)
		VALUES ('capacity-owned', 'Owned', 'edge', 'https://owned.example', 'tenant_private', $1::uuid, 'capacity-cell-a'),
		('capacity-cell-a', 'Cell A', 'edge', 'https://a.example', 'platform_official', NULL, NULL),
		('capacity-cell-b', 'Cell B', 'edge', 'https://b.example', 'platform_official', NULL, NULL)`, tenant)
	exec(`INSERT INTO quartermaster.infrastructure_nodes (node_id, cluster_id, node_name, node_type)
		VALUES ('capacity-edge', 'capacity-owned', 'Capacity edge', 'edge'),
		('capacity-platform', 'capacity-cell-a', 'Platform edge', 'edge')`)
	s := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	owner := context.WithValue(tenantCtx(tenant, "owner"), ctxkeys.KeyUserID, "owner-user")
	get := &quartermasterpb.GetNodeCapacityRequest{NodeId: "capacity-edge"}
	initial, err := s.GetNodeCapacity(owner, get)
	if err != nil || initial.GetRevision() != 0 || initial.GetEgressSource() != quartermasterpb.CapacitySource_CAPACITY_SOURCE_DEFAULT || initial.GetEffectiveEgressBytesPerSec() != capacity.DefaultBudget {
		t.Fatalf("initial default capacity = %v, %v", initial, err)
	}
	change := &quartermasterpb.SetNodeCapacityRequest{NodeId: get.NodeId, EgressBudgetBytesPerSec: proto.Uint64(300000000), IngressBudgetBytesPerSec: proto.Uint64(200000000)}
	updated, err := s.SetNodeCapacity(owner, change)
	if err != nil || updated.GetRevision() != 1 || updated.GetEffectiveEgressBytesPerSec() != 300000000 || updated.GetEffectiveIngressBytesPerSec() != 200000000 || updated.GetUpdatedBy() != "owner-user" {
		t.Fatalf("owner update = %v, %v", updated, err)
	}
	if _, err := s.SetNodeCapacity(owner, change); status.Code(err) != codes.Aborted {
		t.Fatalf("stale write = %v, want Aborted", err)
	}
	foreign := tenantCtx("11111111-1111-4111-8111-111111111182", "owner")
	if _, err := s.SetNodeCapacity(foreign, change); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign write = %v, want PermissionDenied", err)
	}
	if _, err := s.GetNodeCapacity(foreign, get); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign read = %v, want PermissionDenied", err)
	}
	if _, err := s.ListNodeCapacities(foreign, &quartermasterpb.ListNodeCapacitiesRequest{ClusterIds: []string{"capacity-owned"}}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("foreign cluster list = %v, want PermissionDenied", err)
	}
	listed, err := s.ListNodeCapacities(owner, &quartermasterpb.ListNodeCapacitiesRequest{ClusterIds: []string{"capacity-owned"}})
	if err != nil || listed.GetCapacities()[get.NodeId].GetRevision() != 1 {
		t.Fatalf("cluster list = %v, %v", listed, err)
	}
	if _, err := quartermasterdb.New(db).StartClusterControlCellReassignment(context.Background(), quartermasterdb.StartClusterControlCellReassignmentParams{
		ClusterID: "capacity-owned", TargetCellID: "capacity-cell-b", DeadlineAt: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	node, err := s.GetNodeByLogicalName(owner, &quartermasterpb.GetNodeByLogicalNameRequest{NodeId: get.NodeId})
	if err != nil || node.GetNode().GetCapacity().GetRevision() != 1 || node.GetNode().GetCapacity().GetEffectiveEgressBytesPerSec() != 300000000 {
		t.Fatalf("capacity after control-cell reassignment = %v, %v", node, err)
	}
	exec(`ALTER TABLE quartermaster.service_event_outbox RENAME TO unavailable_service_event_outbox`)
	_, writeErr := s.SetNodeCapacity(owner, &quartermasterpb.SetNodeCapacityRequest{NodeId: get.NodeId, ExpectedRevision: 1})
	exec(`ALTER TABLE quartermaster.unavailable_service_event_outbox RENAME TO service_event_outbox`)
	if writeErr == nil {
		t.Fatal("capacity committed without its audit event")
	}
	current, err := s.GetNodeCapacity(owner, get)
	if err != nil || current.GetRevision() != 1 {
		t.Fatalf("failed audit advanced capacity: %v, %v", current, err)
	}
	reset, err := s.SetNodeCapacity(owner, &quartermasterpb.SetNodeCapacityRequest{NodeId: get.NodeId, ExpectedRevision: 1})
	if err != nil || reset.GetRevision() != 2 || reset.EgressBudgetBytesPerSec != nil || reset.IngressBudgetBytesPerSec != nil || reset.GetEffectiveEgressBytesPerSec() != capacity.DefaultBudget {
		t.Fatalf("reset = %v, %v", reset, err)
	}
	operator := context.WithValue(owner, ctxkeys.KeyPlatformOperator, true)
	if _, err := s.SetNodeCapacity(operator, &quartermasterpb.SetNodeCapacityRequest{NodeId: "capacity-platform", EgressBudgetBytesPerSec: proto.Uint64(400000000)}); err != nil {
		t.Fatalf("operator on platform node: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM quartermaster.service_event_outbox WHERE event_type = 'node.capacity_updated' AND resource_id = $1`, get.NodeId).Scan(&count); err != nil || count != 2 {
		t.Fatalf("audit rows = %d, %v; want exactly two successful revisions", count, err)
	}
	service := context.WithValue(owner, ctxkeys.KeyAuthType, "service")
	const newOwner = "11111111-1111-4111-8111-111111111182"
	exec(`INSERT INTO quartermaster.tenants (id,name) VALUES ($1::uuid,'New capacity owner')`, newOwner)
	exec(`UPDATE quartermaster.infrastructure_clusters SET owner_tenant_id=$1::uuid WHERE cluster_id='capacity-owned'`, newOwner)
	t.Run("proxy read cannot outlive ownership transfer", func(t *testing.T) {
		_, err := s.GetNodeCapacity(service, &quartermasterpb.GetNodeCapacityRequest{NodeId: get.NodeId, ExpectedOwnerTenantId: proto.String(tenant)})
		if status.Code(err) != codes.Aborted {
			t.Fatalf("stale owner read=%v; want Aborted", err)
		}
	})
	t.Run("proxy write cannot outlive ownership transfer", func(t *testing.T) {
		_, err := s.SetNodeCapacity(service, &quartermasterpb.SetNodeCapacityRequest{NodeId: get.NodeId, ExpectedRevision: 2, ExpectedOwnerTenantId: proto.String(tenant)})
		if status.Code(err) != codes.Aborted {
			t.Fatalf("stale owner write=%v; want Aborted", err)
		}
	})
	t.Run("platform owner fence", func(t *testing.T) {
		result, err := s.GetNodeCapacity(service, &quartermasterpb.GetNodeCapacityRequest{NodeId: "capacity-platform", ExpectedOwnerTenantId: proto.String("")})
		if err != nil || result.GetEffectiveEgressBytesPerSec() != 400000000 {
			t.Fatalf("platform read=%v %v", result, err)
		}
	})

}
