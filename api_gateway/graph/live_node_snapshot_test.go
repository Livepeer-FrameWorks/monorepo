package graph

import (
	"testing"
	"time"

	proto "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	structpb "google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func snapshotNode(snapshotAge time.Duration, heartbeatAge time.Duration) *proto.InfrastructureNode {
	tenantID := "tenant-1"
	region := "eu-west"
	now := time.Now()
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
			CollectedAt:    timestamppb.New(now.Add(-snapshotAge)),
		},
	}
	if heartbeatAge >= 0 {
		node.LastHeartbeat = timestamppb.New(now.Add(-heartbeatAge))
	}
	return node
}

func metaString(t *testing.T, live *proto.LiveNode, key string) string {
	t.Helper()
	if live == nil || live.GetMetadata() == nil {
		t.Fatalf("metadata missing")
	}
	v, ok := live.GetMetadata().GetFields()[key]
	if !ok {
		t.Fatalf("metadata key %q missing", key)
	}
	return v.GetStringValue()
}

func metaHas(live *proto.LiveNode, key string) bool {
	if live == nil || live.GetMetadata() == nil {
		return false
	}
	_, ok := live.GetMetadata().GetFields()[key]
	return ok
}

func TestLiveNodeFromSnapshot_FreshSnapshot(t *testing.T) {
	node := snapshotNode(30*time.Second, 10*time.Second)

	live := liveNodeFromSnapshot(node)
	if live == nil {
		t.Fatal("expected live node")
	}
	if !live.GetIsHealthy() {
		t.Fatal("fresh snapshot should be healthy")
	}
	if got := metaString(t, live, "source"); got != liveNodeSourceSnapshot {
		t.Fatalf("source = %q, want %q", got, liveNodeSourceSnapshot)
	}
	if metaHas(live, "snapshot_stale") {
		t.Fatal("fresh snapshot should not carry snapshot_stale")
	}
	if !metaHas(live, "snapshot_received_at") {
		t.Fatal("snapshot_received_at must be set on fresh snapshot")
	}
}

func TestLiveNodeFromSnapshot_StaleSnapshotFreshHeartbeat(t *testing.T) {
	node := snapshotNode(5*time.Minute, 10*time.Second)

	live := liveNodeFromSnapshot(node)
	if live == nil {
		t.Fatal("expected live node")
	}
	if !live.GetIsHealthy() {
		t.Fatal("stale snapshot + fresh heartbeat should be healthy")
	}
	v := live.GetMetadata().GetFields()["snapshot_stale"]
	if v == nil || !v.GetBoolValue() {
		t.Fatalf("snapshot_stale should be true (got %#v)", v)
	}
}

func TestLiveNodeFromSnapshot_StaleSnapshotStaleHeartbeat(t *testing.T) {
	node := snapshotNode(5*time.Minute, 5*time.Minute)

	live := liveNodeFromSnapshot(node)
	if live == nil {
		t.Fatal("expected live node")
	}
	if live.GetIsHealthy() {
		t.Fatal("stale snapshot + stale heartbeat must be unhealthy")
	}
	v := live.GetMetadata().GetFields()["snapshot_stale"]
	if v == nil || !v.GetBoolValue() {
		t.Fatalf("snapshot_stale should be true (got %#v)", v)
	}
}

func TestLiveNodeFromSnapshot_StaleSnapshotMissingHeartbeat(t *testing.T) {
	node := snapshotNode(5*time.Minute, -1) // negative => no heartbeat set

	live := liveNodeFromSnapshot(node)
	if live == nil {
		t.Fatal("expected live node")
	}
	if live.GetIsHealthy() {
		t.Fatal("stale snapshot + missing heartbeat must be unhealthy")
	}
}

func TestLiveNodeFromSnapshot_FutureTimestamp(t *testing.T) {
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
		t.Fatal("future snapshot must not be healthy")
	}
	if metaHas(live, "snapshot_stale") {
		t.Fatal("future snapshot must use snapshot_invalid_reason, not snapshot_stale")
	}
	if got := metaString(t, live, "snapshot_invalid_reason"); got != "future_timestamp" {
		t.Fatalf("snapshot_invalid_reason = %q, want %q", got, "future_timestamp")
	}
}

func TestLiveNodeFromSnapshot_MissingTimestamp(t *testing.T) {
	tenantID := "tenant-1"
	node := &proto.InfrastructureNode{
		NodeId:        "core-1",
		OwnerTenantId: &tenantID,
		ResourceSnapshot: &proto.NodeResourceSnapshot{
			RamTotalBytes:  2048,
			DiskTotalBytes: 8192,
			UptimeSeconds:  60,
			// no CollectedAt
		},
	}
	live := liveNodeFromSnapshot(node)
	if live == nil {
		t.Fatal("expected live node")
	}
	if live.GetIsHealthy() {
		t.Fatal("missing timestamp + missing heartbeat must be unhealthy")
	}
	if got := metaString(t, live, "snapshot_invalid_reason"); got != "missing_timestamp" {
		t.Fatalf("snapshot_invalid_reason = %q, want %q", got, "missing_timestamp")
	}
}

func TestLiveNodeFromSnapshot_MissingSnapshot(t *testing.T) {
	tenantID := "tenant-1"
	node := &proto.InfrastructureNode{
		NodeId:        "core-1",
		OwnerTenantId: &tenantID,
	}
	if live := liveNodeFromSnapshot(node); live != nil {
		t.Fatalf("missing snapshot must yield nil; got %+v", live)
	}
}

func TestLiveNodeFromSnapshotCarriesOwnerTenant(t *testing.T) {
	node := snapshotNode(30*time.Second, 0)
	live := liveNodeFromSnapshot(node)
	if live == nil {
		t.Fatal("expected live node")
	}
	if live.GetTenantId() != "tenant-1" {
		t.Fatalf("tenant_id = %q, want %q", live.GetTenantId(), "tenant-1")
	}
}

func TestLiveNodeWithMetadataSource_PreservesExisting(t *testing.T) {
	original := &proto.LiveNode{
		NodeId: "edge-1",
		Metadata: &structpb.Struct{
			Fields: map[string]*structpb.Value{
				"region_hint": structpb.NewStringValue("us-east"),
			},
		},
	}

	wrapped := liveNodeWithMetadataSource(original, liveNodeSourcePeriscope)
	if wrapped == original {
		t.Fatal("helper must return a clone, not mutate the input")
	}
	if got := wrapped.GetMetadata().GetFields()["region_hint"].GetStringValue(); got != "us-east" {
		t.Fatalf("existing metadata clobbered: region_hint = %q", got)
	}
	if got := wrapped.GetMetadata().GetFields()["source"].GetStringValue(); got != liveNodeSourcePeriscope {
		t.Fatalf("source = %q, want %q", got, liveNodeSourcePeriscope)
	}

	// Original metadata must remain unchanged so cached loader objects do not
	// leak the source attribution from one resolver call into another.
	if _, leaked := original.GetMetadata().GetFields()["source"]; leaked {
		t.Fatal("helper mutated the input's metadata in place")
	}
}

func TestLiveNodeWithMetadataSource_NilSafe(t *testing.T) {
	if got := liveNodeWithMetadataSource(nil, liveNodeSourcePeriscope); got != nil {
		t.Fatalf("nil input must return nil; got %+v", got)
	}

	// Nil metadata on a non-nil node is also valid; helper should populate.
	in := &proto.LiveNode{NodeId: "edge-2"}
	out := liveNodeWithMetadataSource(in, liveNodeSourcePeriscope)
	if out == nil || out.GetMetadata() == nil {
		t.Fatal("helper must materialize metadata when input lacks it")
	}
	if got := out.GetMetadata().GetFields()["source"].GetStringValue(); got != liveNodeSourcePeriscope {
		t.Fatalf("source = %q", got)
	}
}
