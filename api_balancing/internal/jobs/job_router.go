package jobs

import (
	"strings"
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
func routeProcessingJob(job *processingJob) (string, string) {
	sm := state.DefaultManager()
	class := jobProcessingClass(job)
	jobTenant := ""
	if job != nil {
		jobTenant = job.TenantID
	}
	// Processing output is durable media owned by the artifact's origin cluster, so only that cluster's nodes may
	// run the job. An artifact without a recorded origin is not placed anywhere.
	originCluster := ""
	if job != nil {
		originCluster = strings.TrimSpace(job.OriginCluster)
	}
	if originCluster == "" {
		return "", "artifact origin cluster unknown"
	}
	aliveIDs := sm.AliveNodeIDs(60 * time.Second)
	if len(aliveIDs) == 0 {
		return "", "no alive nodes"
	}

	if job.PreferredNode.Valid && job.PreferredNode.String != "" {
		node := sm.GetNodeState(job.PreferredNode.String)
		if node != nil && node.ClusterID == originCluster && node.CapProcessing && node.IsHealthy && nodeAcceptsProcessing(node, true) && node.CanRunClass(class) && nodeEligibleForJobTenant(node, jobTenant) {
			return job.PreferredNode.String, "preferred_source_node"
		}
		return "", "preferred source node unavailable"
	}

	var bestID string
	bestLoad := -1
	for _, id := range aliveIDs {
		node := sm.GetNodeState(id)
		if node == nil || node.ClusterID != originCluster || !node.CapProcessing || !node.IsHealthy || !nodeAcceptsProcessing(node, false) {
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
		return "", "no nodes in origin cluster " + originCluster + " with capacity for class " + class
	}
	return bestID, "lowest_load:" + class
}
