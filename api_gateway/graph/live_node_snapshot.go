package graph

import (
	"maps"
	"time"

	proto "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	protoclone "google.golang.org/protobuf/proto"
	structpb "google.golang.org/protobuf/types/known/structpb"
)

const (
	liveNodeSourcePeriscope = "periscope"
	liveNodeSourceSnapshot  = "quartermaster_snapshot"

	// liveNodeFreshnessWindow bounds both snapshot age and heartbeat age for
	// the snapshot-fallback health calculation. 90s is the existing snapshot
	// window; SyncMesh heartbeats are an order of magnitude faster, so anything
	// older than this window is treated as stale.
	liveNodeFreshnessWindow = 90 * time.Second
)

// liveNodeFromSnapshot maps Privateer's core-infra resource snapshot into the
// LiveNode shape used by GraphQL. Foghorn/Helmsman remains authoritative for
// stream counts, network rates, and operational mode.
//
// Health rules and metadata contract:
//   - fresh snapshot (within window)                   -> healthy
//   - stale snapshot + fresh last_heartbeat            -> healthy + snapshot_stale=true
//   - stale snapshot + stale or missing last_heartbeat -> unhealthy + snapshot_stale=true
//   - snapshot timestamp in the future                 -> unhealthy + snapshot_invalid_reason="future_timestamp"
//   - missing snapshot timestamp                       -> unhealthy unless heartbeat fresh + snapshot_invalid_reason="missing_timestamp"
//
// snapshot_stale is always boolean; the enum-ish "why is this timestamp not
// usable" lives in snapshot_invalid_reason so the frontend / alerts don't
// have to handle a polymorphic key.
func liveNodeFromSnapshot(obj *proto.InfrastructureNode) *proto.LiveNode {
	snap := obj.GetResourceSnapshot()
	if snap == nil {
		return nil
	}
	live := &proto.LiveNode{
		NodeId:         obj.GetNodeId(),
		TenantId:       obj.GetOwnerTenantId(),
		CpuPercent:     snap.GetCpuPercent(),
		RamUsedBytes:   snap.GetRamUsedBytes(),
		RamTotalBytes:  snap.GetRamTotalBytes(),
		DiskUsedBytes:  snap.GetDiskUsedBytes(),
		DiskTotalBytes: snap.GetDiskTotalBytes(),
		Location:       obj.GetRegion(),
	}
	if lat := obj.GetLatitude(); lat != 0 {
		live.Latitude = lat
	}
	if lon := obj.GetLongitude(); lon != 0 {
		live.Longitude = lon
	}

	now := time.Now()
	meta := map[string]any{
		"source": liveNodeSourceSnapshot,
	}

	collected := snap.GetCollectedAt()
	if collected != nil {
		t := collected.AsTime()
		live.UpdatedAt = collected
		meta["snapshot_received_at"] = t.UTC().Format(time.RFC3339Nano)

		switch {
		case t.After(now):
			live.IsHealthy = false
			meta["snapshot_invalid_reason"] = "future_timestamp"
		case now.Sub(t) <= liveNodeFreshnessWindow:
			live.IsHealthy = true
		default:
			meta["snapshot_stale"] = true
			live.IsHealthy = heartbeatFresh(obj, now)
		}
	} else {
		meta["snapshot_invalid_reason"] = "missing_timestamp"
		live.IsHealthy = heartbeatFresh(obj, now)
	}

	if md, err := structpb.NewStruct(meta); err == nil {
		live.Metadata = md
	}
	return live
}

// liveNodeWithMetadataSource returns a clone of in with metadata.source set,
// preserving all other metadata keys. Loaders return cached pointers per
// request; mutating in place would clobber concurrent resolver calls. nil-safe.
func liveNodeWithMetadataSource(in *proto.LiveNode, source string) *proto.LiveNode {
	if in == nil {
		return nil
	}
	cloned := protoclone.Clone(in)
	out, ok := cloned.(*proto.LiveNode)
	if !ok {
		return in
	}
	fields := map[string]*structpb.Value{}
	if existing := out.GetMetadata(); existing != nil {
		maps.Copy(fields, existing.GetFields())
	}
	fields["source"] = structpb.NewStringValue(source)
	out.Metadata = &structpb.Struct{Fields: fields}
	return out
}

func heartbeatFresh(obj *proto.InfrastructureNode, now time.Time) bool {
	hb := obj.GetLastHeartbeat()
	if hb == nil {
		return false
	}
	t := hb.AsTime()
	if t.After(now) {
		return false
	}
	return now.Sub(t) <= liveNodeFreshnessWindow
}
