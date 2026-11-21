package jobs

import (
	"context"
	"strings"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// clusterAccessibleForTenant gates media placement on Quartermaster cluster↔tenant entitlement (the candidate
// node's virtual cluster must be entitled to the job's tenant). Seam: tests inject an entitlement map without
// a live Quartermaster; production is the fail-closed control-package resolver.
var clusterAccessibleForTenant = control.ClusterAccessibleForTenantContext

var registeredProcessingNodes = control.RegisteredProcessingNodes

// jobProcessingClass resolves the processing class a job needs, matched against
// a node's advertised per-class capacity during routing. processing_jobs has no
// per-job class column, so every queued job is video_transcode (VOD/clip/DVR).
func jobProcessingClass(_ *processingJob) string {
	return mist.ProcessingClassVideoTranscode
}

// nodeEligibleForJobTenant is the tenant-boundary gate on processing placement: the candidate node's virtual
// cluster must be entitled to run the job's tenant's media, per Quartermaster cluster↔tenant entitlement. This
// binds authority to the authenticated node→cluster + cluster→tenant chain, NOT to a NodeState.TenantID string
// (an empty TenantID is not universal authority). Fail-closed: an unentitled or unproven cluster is skipped.
// A job with no tenant is only placeable on a platform-shared cluster (handled inside the predicate).
func nodeEligibleForJobTenant(ctx context.Context, node *state.NodeState, jobTenantID string) bool {
	if node == nil {
		return false
	}
	return clusterAccessibleForTenant(ctx, node.ClusterID, jobTenantID)
}

// nodeAcceptsProcessing applies the operational mode to new processing work:
// maintenance isolates a node from all work, and draining refuses new work
// except on the preferred source node, whose source bytes are already local.
func nodeAcceptsProcessing(node *state.NodeState, preferredSource bool) bool {
	switch node.OperationalMode {
	case "", state.NodeModeNormal:
		return true
	case state.NodeModeDraining:
		return preferredSource
	default:
		return false
	}
}

// routeProcessingJob selects the best node for a processing job by matching the
// job's processing class against each node's advertised class capacity, then
// picking the lowest in-flight load within that class. Returns (nodeID,
// reason). Empty nodeID means no suitable node found.
func routeProcessingJobWithInventory(ctx context.Context, job *processingJob, tick *processingTickInventory) (string, string, string) {
	sm := state.DefaultManager()
	class := jobProcessingClass(job)
	if job == nil || strings.TrimSpace(job.OriginCluster) == "" {
		return "", "artifact origin cluster unknown", mist.ProcessingWaitClusterUnavailable
	}
	origin := strings.TrimSpace(job.OriginCluster)
	if !clusterAccessibleForTenant(ctx, origin, job.TenantID) {
		return "", "origin cluster entitlement unavailable", mist.ProcessingWaitClusterUnavailable
	}
	inventory := tick.cluster(ctx, origin)
	// Missing reports and a warming inventory cannot establish permanent incapacity.
	if job.RequiredMemoryBytes > 0 && inventory.err == nil {
		known, possible := len(inventory.ids) > 0, false
		for _, id := range inventory.ids {
			budget, ok := inventory.budget(id, sm)
			if !ok {
				known = false
				continue
			}
			if budget >= uint64(job.RequiredMemoryBytes) {
				possible = true
			}
		}
		if tick.warm && known && !possible {
			return "", mist.ProcessingMemoryNoNodeReason, ""
		}
	}
	registered := make(map[string]bool, len(inventory.ids))
	for _, id := range inventory.ids {
		registered[id] = true
	}
	fits := func(n *state.NodeState) bool {
		if job.RequiredMemoryBytes <= 0 {
			return true
		}
		budget, ok := inventory.budget(n.NodeID, sm)
		return ok && budget >= uint64(job.RequiredMemoryBytes)
	}
	candidate := func(n *state.NodeState, preferred bool) bool {
		if n == nil || n.ClusterID != origin || !n.CapProcessing || !nodeEligibleForJobTenant(ctx, n, job.TenantID) {
			return false
		}
		if inventory.err == nil && !registered[n.NodeID] {
			return false
		}
		return n.IsHealthy && nodeAcceptsProcessing(n, preferred) && fits(n)
	}
	alive := sm.AliveNodeIDs(60 * time.Second)
	aliveSet := make(map[string]bool, len(alive))
	for _, id := range alive {
		aliveSet[id] = true
	}
	if job.PreferredNode.Valid && job.PreferredNode.String != "" {
		n := sm.GetNodeState(job.PreferredNode.String)
		if candidate(n, true) && aliveSet[n.NodeID] && n.CanRunClass(class) {
			return n.NodeID, "preferred_source_node", ""
		}
		return "", "preferred source node unavailable", mist.ProcessingWaitFittingNodeUnavailable
	}
	bestID := ""
	bestLoad := -1
	slotsBlocked := false
	for _, id := range alive {
		n := sm.GetNodeState(id)
		if !candidate(n, false) {
			continue
		}
		if !n.CanRunClass(class) {
			slotsBlocked = true
			continue
		}
		load, _ := n.ClassLoad(class)
		if bestID == "" || load < bestLoad {
			bestID, bestLoad = id, load
		}
	}
	if bestID != "" {
		return bestID, "lowest_load:" + class, ""
	}
	reason := mist.ProcessingWaitFittingNodeUnavailable
	if inventory.err != nil || len(inventory.ids) == 0 {
		reason = mist.ProcessingWaitClusterUnavailable
	} else if slotsBlocked {
		reason = mist.ProcessingWaitSlots
	}
	detail := "no nodes in origin cluster " + origin + " with capacity for class " + class
	if len(alive) == 0 {
		detail = "no alive nodes"
	}
	return "", detail, reason
}
