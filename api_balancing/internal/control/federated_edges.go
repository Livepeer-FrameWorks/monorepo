package control

import (
	"time"

	"frameworks/api_balancing/internal/balancer"
)

// federatedEdgeMaxAge bounds how old a StreamAdvertisement-fed Location may
// be to feed viewer resolution: about four missed 5s ad pushes, doubling as the
// liveness gate (a healthy peer re-advertises every 5s).
const federatedEdgeMaxAge = 20 * time.Second

// FederatedRemoteEdges converts StreamAdvertisement-fed registry edges into
// balancer candidates: the pre-warmed alternative to the cold QueryStream
// fan-out, shared by the HTTP /play handler and the gRPC viewer-resolution
// surface. Candidates without RAM data (peers predating the ram_used/ram_max
// ad fields) are dropped because ScoreRemoteEdges rejects RAMMax==0; if that
// leaves nothing, callers fall through to the fan-out. Memory-only; nil-safe
// before the registry is wired.
func FederatedRemoteEdges(internalName string) []balancer.RemoteEdgeCandidate {
	r := StreamRegistryInstance
	if r == nil {
		return nil
	}
	byCluster := r.FederatedEdgeCandidates(internalName, federatedEdgeMaxAge)
	var out []balancer.RemoteEdgeCandidate
	for peerClusterID, cands := range byCluster {
		if IsServedCluster(peerClusterID) {
			continue
		}
		for _, c := range cands {
			if c.RAMMax == 0 {
				continue
			}
			out = append(out, balancer.RemoteEdgeCandidate{
				ClusterID:   peerClusterID,
				NodeID:      c.NodeID,
				BaseURL:     c.BaseURL,
				GeoLat:      c.GeoLat,
				GeoLon:      c.GeoLon,
				BWAvailable: uint64(c.BWAvailable),
				CPUPercent:  c.CPUPercent,
				RAMUsed:     c.RAMUsed,
				RAMMax:      c.RAMMax,
			})
		}
	}
	return out
}
