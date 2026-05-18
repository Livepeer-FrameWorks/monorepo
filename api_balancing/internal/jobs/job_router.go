package jobs

import (
	"frameworks/api_balancing/internal/state"
	"time"
)

// routeProcessingJob selects the best edge node for a processing job.
// Returns (nodeID, reason). Empty nodeID means no suitable node found.
func routeProcessingJob(job *processingJob) (string, string) {
	sm := state.DefaultManager()
	aliveIDs := sm.AliveNodeIDs(60 * time.Second)
	if len(aliveIDs) == 0 {
		return "", "no alive nodes"
	}

	if job != nil && job.PreferredNode.Valid && job.PreferredNode.String != "" {
		node := sm.GetNodeState(job.PreferredNode.String)
		if node != nil && node.CapProcessing && node.IsHealthy && (node.MaxTranscodes == 0 || node.CurrentTranscodes < node.MaxTranscodes) {
			return job.PreferredNode.String, "preferred_source_node"
		}
		return "", "preferred source node unavailable"
	}

	var bestID string
	bestLoad := -1

	for _, id := range aliveIDs {
		node := sm.GetNodeState(id)
		if node == nil {
			continue
		}
		if !node.CapProcessing {
			continue
		}
		if !node.IsHealthy {
			continue
		}
		if node.MaxTranscodes > 0 && node.CurrentTranscodes >= node.MaxTranscodes {
			continue
		}

		// Pick node with fewest active transcodes
		if bestID == "" || node.CurrentTranscodes < bestLoad {
			bestID = id
			bestLoad = node.CurrentTranscodes
		}
	}

	if bestID == "" {
		return "", "no processing-capable nodes available"
	}
	return bestID, "lowest_transcode_load"
}
