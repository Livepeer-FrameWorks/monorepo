package jobs

import (
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// clusterAccessibleForTenant gates media placement on Quartermaster cluster↔tenant entitlement (the candidate
// node's virtual cluster must be entitled to the job's tenant). Seam: tests inject an entitlement map without
// a live Quartermaster; production is the fail-closed control-package resolver.
var clusterAccessibleForTenant = control.ClusterAccessibleForTenant

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
func nodeEligibleForJobTenant(node *state.NodeState, jobTenantID string) bool {
	if node == nil {
		return false
	}
	return clusterAccessibleForTenant(node.ClusterID, jobTenantID)
}

// routeProcessingJob selects the best node for a processing job by matching the
// job's processing class against each node's advertised class capacity, then
// picking the lowest in-flight load within that class. Returns (nodeID,
// reason). Empty nodeID means no suitable node found.
func routeProcessingJob(job *processingJob) (string, string) {
	sm := state.DefaultManager()
	class := jobProcessingClass(job)
	jobTenant := ""
	if job != nil {
		jobTenant = job.TenantID
	}
	aliveIDs := sm.AliveNodeIDs(60 * time.Second)
	if len(aliveIDs) == 0 {
		return "", "no alive nodes"
	}

	if job != nil && job.PreferredNode.Valid && job.PreferredNode.String != "" {
		node := sm.GetNodeState(job.PreferredNode.String)
		if node != nil && node.CapProcessing && node.IsHealthy && node.CanRunClass(class) && nodeEligibleForJobTenant(node, jobTenant) {
			return job.PreferredNode.String, "preferred_source_node"
		}
		return "", "preferred source node unavailable"
	}

	var bestID string
	bestLoad := -1
	for _, id := range aliveIDs {
		node := sm.GetNodeState(id)
		if node == nil || !node.CapProcessing || !node.IsHealthy {
			continue
		}
		if !node.CanRunClass(class) {
			continue
		}
		if !nodeEligibleForJobTenant(node, jobTenant) {
			continue
		}

		// Pick node with the fewest in-flight jobs of this class.
		load, _ := node.ClassLoad(class)
		if bestID == "" || load < bestLoad {
			bestID = id
			bestLoad = load
		}
	}

	if bestID == "" {
		return "", "no nodes with capacity for class " + class
	}
	return bestID, "lowest_load:" + class
}
