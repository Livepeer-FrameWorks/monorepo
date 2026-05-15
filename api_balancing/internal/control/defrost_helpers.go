package control

import (
	"fmt"
	"math"
	"sort"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func controlLogger() logging.Logger {
	if registry != nil && registry.log != nil {
		return registry.log
	}
	return logging.NewLoggerWithService("foghorn")
}

// PickStorageNodeIDPublic is a thin shim for callers without viewer-geo
// context; it delegates to PickDefrostNode with zero geo (geo is then skipped
// from the ranking entirely). New callers with viewer geo should call
// PickDefrostNode directly.
func PickStorageNodeIDPublic() (string, error) {
	return pickStorageNodeID()
}

func pickStorageNodeID() (string, error) {
	return PickDefrostNode(0, 0, nil)
}

// PickDefrostNode chooses a storage-capable healthy node for a defrost write.
// Ranking, in order:
//  1. active_defrost_count asc   — spread work across edges (primary signal)
//  2. geo distance asc            — viewer locality (skipped when viewer geo == 0)
//  3. disk usage ratio asc        — tie-breaker only; admission lives in Helmsman
//
// Disk is never a primary signal and never excludes a node. Edges run hot by
// design; if a chosen node is full Helmsman will fail typed
// (REASON_INSUFFICIENT_SPACE) and the caller can retry with the failed node
// in `exclude`.
func PickDefrostNode(viewerLat, viewerLon float64, exclude map[string]struct{}) (string, error) {
	if loadBalancerInstance == nil {
		return "", fmt.Errorf("load balancer not available")
	}
	nodes := loadBalancerInstance.GetNodes()
	if len(nodes) == 0 {
		return "", fmt.Errorf("no storage nodes available")
	}
	counts := ActiveDefrostCount()
	useGeo := viewerLat != 0 || viewerLon != 0

	type candidate struct {
		nodeID         string
		activeDefrosts int
		geoDistance    float64
		usageRatio     float64
	}
	var cands []candidate
	for _, node := range nodes {
		if !node.CapStorage || !node.IsHealthy || node.IsStale {
			continue
		}
		if _, skip := exclude[node.NodeID]; skip {
			continue
		}
		c := candidate{
			nodeID:         node.NodeID,
			activeDefrosts: counts[node.NodeID],
		}
		// Nodes without registered geo sort AFTER nodes with measured distance
		// when the viewer has geo. Using +Inf as sentinel keeps the comparator
		// monotonic without a separate "has geo" branch in the sort.
		if useGeo {
			if node.Latitude != nil && node.Longitude != nil {
				c.geoDistance = CalculateGeoDistance(viewerLat, viewerLon, *node.Latitude, *node.Longitude)
			} else {
				c.geoDistance = math.Inf(1)
			}
		}
		if node.DiskTotalBytes > 0 {
			c.usageRatio = float64(node.DiskUsedBytes) / float64(node.DiskTotalBytes)
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("no storage nodes available")
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].activeDefrosts != cands[j].activeDefrosts {
			return cands[i].activeDefrosts < cands[j].activeDefrosts
		}
		if useGeo && cands[i].geoDistance != cands[j].geoDistance {
			return cands[i].geoDistance < cands[j].geoDistance
		}
		return cands[i].usageRatio < cands[j].usageRatio
	})
	return cands[0].nodeID, nil
}
