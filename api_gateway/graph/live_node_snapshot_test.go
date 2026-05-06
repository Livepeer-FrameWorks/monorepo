package graph

import (
	"testing"
	"time"

	proto "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestLiveNodeFromSnapshotCarriesOwnerTenant(t *testing.T) {
	tenantID := "tenant-1"
	region := "eu-west"
	node := &proto.InfrastructureNode{
		NodeId:        "core-1",
		Region:        &region,
		OwnerTenantId: &tenantID,
		ResourceSnapshot: &proto.NodeResourceSnapshot{
			CpuPercent:     12.5,
			RamUsedBytes:   1024,
			RamTotalBytes:  2048,
			DiskUsedBytes:  4096,
			DiskTotalBytes: 8192,
			UptimeSeconds:  60,
			CollectedAt:    timestamppb.New(time.Now().Add(-30 * time.Second)),
		},
	}

	live := liveNodeFromSnapshot(node)
	if live == nil {
		t.Fatal("expected live node")
	}
	if live.GetTenantId() != tenantID {
		t.Fatalf("tenant_id = %q, want %q", live.GetTenantId(), tenantID)
	}
	if !live.GetIsHealthy() {
		t.Fatal("expected fresh snapshot to be healthy")
	}
}

func TestLiveNodeFromSnapshotRejectsFutureFreshness(t *testing.T) {
	tenantID := "tenant-1"
	node := &proto.InfrastructureNode{
		NodeId:        "core-1",
		OwnerTenantId: &tenantID,
		ResourceSnapshot: &proto.NodeResourceSnapshot{
			RamTotalBytes:  2048,
			DiskTotalBytes: 8192,
			UptimeSeconds:  60,
			CollectedAt:    timestamppb.New(time.Now().Add(24 * time.Hour)),
		},
	}

	live := liveNodeFromSnapshot(node)
	if live == nil {
		t.Fatal("expected live node")
	}
	if live.GetIsHealthy() {
		t.Fatal("future snapshot timestamp should not be treated as healthy")
	}
}
