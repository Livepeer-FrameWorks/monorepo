package jobs

import (
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

// jobProcessingClass resolves the processing class a job needs, matched against
// a node's advertised per-class capacity during routing. processing_jobs has no
// per-job class column, so every queued job is video_transcode (VOD/clip/DVR).
func jobProcessingClass(_ *processingJob) string {
	return mist.ProcessingClassVideoTranscode
}

// routeProcessingJob selects the best node for a processing job by matching the
// job's processing class against each node's advertised class capacity, then
// picking the lowest in-flight load within that class. Returns (nodeID,
// reason). Empty nodeID means no suitable node found.
func routeProcessingJob(job *processingJob) (string, string) {
	sm := state.DefaultManager()
	class := jobProcessingClass(job)
	aliveIDs := sm.AliveNodeIDs(60 * time.Second)
	if len(aliveIDs) == 0 {
		return "", "no alive nodes"
	}

	if job != nil && job.PreferredNode.Valid && job.PreferredNode.String != "" {
		node := sm.GetNodeState(job.PreferredNode.String)
		if node != nil && node.CapProcessing && node.IsHealthy && node.CanRunClass(class) {
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
