package graph

import (
	"time"

	proto "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// liveNodeFromSnapshot maps Privateer's core-infra resource snapshot into the
// LiveNode shape used by GraphQL. Foghorn/Helmsman remains authoritative for
// stream counts, network rates, and operational mode.
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
	if ts := snap.GetCollectedAt(); ts != nil {
		t := ts.AsTime()
		now := time.Now()
		live.UpdatedAt = ts
		live.IsHealthy = !t.After(now) && now.Sub(t) <= 90*time.Second
	}
	return live
}
