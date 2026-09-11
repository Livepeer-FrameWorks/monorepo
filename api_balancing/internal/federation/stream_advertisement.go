package federation

import (
	"strings"

	"frameworks/api_balancing/internal/control"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// Both PeerChannel directions use this projection. Publisher generation and
// virtual-cluster identity are node-specific; a relay cannot claim either from
// an aggregate stream status. Legacy unbound edges remain unbound.
//
// The location is keyed by the sender's control cell, never by its CLUSTER_ID:
// every consumer of that key compares it against a control cell, and a Foghorn's
// physical cluster and the virtual clusters it serves are both different
// namespaces. Reports false when the advertisement cannot be keyed, so the
// caller can say so rather than filing it under a guessed namespace.
func applyRegistryStreamAdvertisement(ad *foghornfederationpb.StreamAdvertisement) bool {
	registry := control.StreamRegistryInstance
	if registry == nil || ad == nil {
		return false
	}
	controlCellID := strings.TrimSpace(ad.GetControlCellId())
	if controlCellID == "" {
		return false
	}
	// The Locations map holds two namespaces, and this registry's own publisher
	// projection sits at LocalLocationKey. A federated advertisement filed there
	// would not merge with it — it replaces the whole value, dropping the source
	// activity, owning node and pull bookkeeping the local slot carries — and an
	// offline one withdraws it, which durably tombstones the entry for every
	// replica in the cell when it is the last location. No peer may write this
	// Foghorn's own slot, whether it names it by mistake (a cluster provisioned
	// with another's control cell) or on purpose.
	if controlCellID == registry.LocalLocationKey() {
		return false
	}
	edges := make([]control.EdgeCandidate, 0, len(ad.GetEdges()))
	for _, edge := range ad.GetEdges() {
		if edge == nil || !validPullGeneration(edge.GetSourceGeneration(), edge.GetSourceRevision()) ||
			(edge.GetSourceGeneration() != "" && !edge.GetIsOrigin()) {
			continue
		}
		edges = append(edges, control.EdgeCandidate{
			NodeID: edge.GetNodeId(), ClusterID: edge.GetClusterId(), BaseURL: edge.GetBaseUrl(), DTSCURL: edge.GetDtscUrl(),
			IsOrigin: edge.GetIsOrigin(), BWAvailable: int64(edge.GetBwAvailable()), CPUPercent: edge.GetCpuPercent(),
			ViewerCount: int32(edge.GetViewerCount()), GeoLat: edge.GetGeoLat(), GeoLon: edge.GetGeoLon(), BufferState: edge.GetBufferState(),
			RAMUsed: edge.GetRamUsed(), RAMMax: edge.GetRamMax(), SourceGeneration: edge.GetSourceGeneration(), SourceRevision: edge.GetSourceRevision(),
			SourceObservedAt: edge.GetSourceObservedAt(), DTSCObservedAt: edge.GetDtscObservedAt(),
		})
	}
	// OriginClusterID is a media cluster and is left as the sender reported it.
	// It is deliberately not defaulted to the location key: the two are different
	// namespaces, and a guessed value reads as a real origin claim downstream.
	registry.UpsertFederatedSource(controlCellID, control.StreamEntry{
		TenantID: ad.GetTenantId(), PlaybackID: ad.GetPlaybackId(), InternalName: ad.GetInternalName(),
		OriginClusterID: ad.GetOriginClusterId(),
	}, control.Location{
		IsLiveNow: ad.GetIsLive(), AdTimestamp: ad.GetTimestamp(), EdgeCandidates: edges, RecordingNodeID: ad.GetDvrRecordingNodeId(),
	})
	return true
}
