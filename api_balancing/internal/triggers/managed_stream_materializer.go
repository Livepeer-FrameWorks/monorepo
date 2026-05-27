package triggers

import (
	"context"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// managedDVRStartSuppression caps how often the materializer dispatches
// StartDVR for the same (stream_id, source_node_id) pair. The DVR service
// is the authoritative idempotency layer (same contract PUSH_REWRITE relies
// on at processor.go:1325-1370), but the reconciler fires every 30s and
// always-on streams stay admitted for hours; a local cooldown avoids
// pummeling the service with redundant Start calls that all collapse to
// the same active session. A placement change to a different source node
// bypasses the cooldown because the key includes node_id.
const managedDVRStartCooldown = 5 * time.Minute

var managedDVRStarts = struct {
	sync.Mutex
	m map[string]time.Time
}{m: make(map[string]time.Time)}

// ManagedStreamMaterializer satisfies control.ManagedStreamMaterializer
// using the trigger Processor's existing streamCache and DVR service. It
// replays the same cache writes handlePushRewrite performs at PUSH_REWRITE
// time so STREAM_PROCESS / billing / DVR lookups find the same context for
// mist_native streams (which never fire PUSH_REWRITE).
//
// Construct via Processor.ManagedStreamMaterializer() and register with
// control.SetManagedStreamMaterializer at startup.
type ManagedStreamMaterializer struct {
	p *Processor
}

// ManagedStreamMaterializer returns the implementation backed by this
// Processor. Method on *Processor (not a free function) so the materializer
// has access to the cache and DVR service without exporting them.
func (p *Processor) ManagedStreamMaterializer() *ManagedStreamMaterializer {
	return &ManagedStreamMaterializer{p: p}
}

// PopulateStreamContext mirrors the PUSH_REWRITE cache shape at
// processor.go:1174-1222: writes "tenantId:internalName" → streamContext
// and (when processes_json is non-empty) "process:internalName" → policy.
// TTL: 1m for prepaid tenants, 10m for postpaid (same selection as
// PUSH_REWRITE so balance-change enforcement timing matches).
func (m *ManagedStreamMaterializer) PopulateStreamContext(streamCtx *pb.ResolveStreamContextResponse) {
	if m == nil || m.p == nil || m.p.streamCache == nil || streamCtx == nil {
		return
	}
	if !streamCtx.GetAdmitted() {
		return
	}
	tenantID := streamCtx.GetTenantId()
	internalName := streamCtx.GetInternalName()
	if tenantID == "" || internalName == "" {
		return
	}

	isFree, exhausted := freeTierAllowanceState(streamCtx.GetAllowances())
	caps := streamCtx.GetTenantResourceLimits()
	info := streamContext{
		TenantID:           tenantID,
		UserID:             streamCtx.GetUserId(),
		StreamID:           streamCtx.GetStreamId(),
		Source:             "managed_stream_apply",
		UpdatedAt:          time.Now(),
		BillingModel:       streamCtx.GetBillingModel(),
		IsSuspended:        streamCtx.GetIsSuspended(),
		IsBalanceNegative:  streamCtx.GetIsBalanceNegative(),
		OfficialClusterID:  streamCtx.GetOfficialClusterId(),
		OriginClusterID:    streamCtx.GetOriginClusterId(),
		ClusterPeers:       streamCtx.GetClusterPeers(),
		ProcessesJSON:      streamCtx.GetProcessesJson(),
		DVRPolicy:          streamCtx.GetDvrPolicy(),
		MaxStreams:         caps.GetMaxStreams(),
		MaxViewers:         caps.GetMaxViewers(),
		IsFreeTier:         isFree,
		AllowanceExhausted: exhausted,
	}

	cacheTTL := 10 * time.Minute
	if info.BillingModel == "prepaid" {
		cacheTTL = 1 * time.Minute
	}
	m.p.streamCache.Set(tenantID+":"+internalName, info, cacheTTL)
	if info.ProcessesJSON != "" {
		candidates := []string{
			streamCtx.GetOriginClusterId(),
			streamCtx.GetOfficialClusterId(),
			m.p.clusterID,
		}
		processes := m.p.SubstituteGatewayURL(info.ProcessesJSON, candidates)
		m.p.streamCache.Set("process:"+internalName, processes, cacheTTL)
	}
}

// EnsureManagedStreamDVR materializes auto-DVR for a managed stream when
// is_recording_enabled is true. Idempotency is layered:
//
//  1. Local cooldown: skip if the same (stream_id, source_node_id) was kicked
//     in the last managedDVRStartCooldown. Stops the reconciler tick (30s)
//     from spamming Start calls for always-on streams.
//  2. DVR service: authoritative — Start for an already-active session must
//     return the existing session (same contract PUSH_REWRITE relies on at
//     processor.go:1325-1370).
//
// Placement-change cleanup of the previous source node's DVR session is
// handled by the natural STREAM_END → control.StopDVRByInternalName path
// (processor.go:2487) when Foghorn sends RetractManagedStream and Mist
// emits STREAM_END for the bare name.
func (m *ManagedStreamMaterializer) EnsureManagedStreamDVR(ctx context.Context, streamCtx *pb.ResolveStreamContextResponse, sourceNodeID string) {
	if m == nil || m.p == nil || m.p.dvrService == nil || streamCtx == nil {
		return
	}
	if !streamCtx.GetAdmitted() || !streamCtx.GetIsRecordingEnabled() {
		return
	}
	key := streamCtx.GetStreamId() + ":" + sourceNodeID
	now := time.Now()
	managedDVRStarts.Lock()
	if last, ok := managedDVRStarts.m[key]; ok && now.Sub(last) < managedDVRStartCooldown {
		managedDVRStarts.Unlock()
		return
	}
	// Optimistic stamp prevents two concurrent reconciler ticks from
	// double-dispatching for the same key; cleared below on failure so a
	// transient DVR error can retry on the next reconciler tick instead of
	// being silently suppressed for the full cooldown.
	managedDVRStarts.m[key] = now
	managedDVRStarts.Unlock()

	userID := streamCtx.GetUserId()
	req := &pb.StartDVRRequest{
		TenantId:      streamCtx.GetTenantId(),
		InternalName:  streamCtx.GetInternalName(),
		UserId:        &userID,
		ClusterId:     streamCtx.GetOriginClusterId(),
		DvrPolicy:     streamCtx.GetDvrPolicy(),
		ProcessesJson: mist.ThumbsOnlyProcesses(streamCtx.GetProcessesJson()),
	}
	dvrCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var err error
	if hinted, ok := m.p.dvrService.(DVRStarterWithSourceHint); ok {
		_, err = hinted.StartDVRWithSourceHint(dvrCtx, req, sourceNodeID)
	} else {
		_, err = m.p.dvrService.StartDVR(dvrCtx, req)
	}
	if err != nil {
		managedDVRStarts.Lock()
		delete(managedDVRStarts.m, key)
		managedDVRStarts.Unlock()
		m.p.logger.WithFields(logging.Fields{
			"stream_id":      streamCtx.GetStreamId(),
			"source_node_id": sourceNodeID,
			"error":          err,
		}).Warn("EnsureManagedStreamDVR: StartDVR failed; cooldown cleared for retry")
	}
}
