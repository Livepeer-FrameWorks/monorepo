package balancer

import (
	"context"
	"fmt"
	"math"
	"net"
	"strings"

	"frameworks/pkg/logging"

	"frameworks/api_balancing/internal/state"
)

// LoadBalancer is the main load balancer instance
type LoadBalancer struct {
	logger logging.Logger

	// Configurable weights (exactly like C++ version)
	WeightCPU   uint64
	WeightRAM   uint64
	WeightBW    uint64
	WeightGeo   uint64
	WeightBonus uint64
}

// NewLoadBalancer creates a new load balancer with C++ defaults
func NewLoadBalancer(logger logging.Logger) *LoadBalancer {
	lb := &LoadBalancer{
		logger:      logger,
		WeightCPU:   500,  // Same as C++
		WeightRAM:   500,  // Same as C++
		WeightBW:    1000, // Same as C++
		WeightGeo:   1000, // Same as C++
		WeightBonus: 50,   // Same as C++ (not 200!)
	}

	return lb
}

// hostToBinary converts hostname to 16-byte binary representation (IPv6 compatible)
func (lb *LoadBalancer) hostToBinary(hostname string) [16]byte {
	var binHost [16]byte

	// Try to parse as IP first
	if ip := net.ParseIP(hostname); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			// IPv4 - store in IPv6 mapped format
			copy(binHost[12:], ipv4)
			binHost[10] = 0xff
			binHost[11] = 0xff
		} else if ipv6 := ip.To16(); ipv6 != nil {
			// IPv6
			copy(binHost[:], ipv6)
		}
		return binHost
	}

	// Try to resolve hostname to IP
	ips, err := net.LookupIP(hostname)
	if err != nil || len(ips) == 0 {
		lb.logger.WithField("hostname", hostname).Warn("Could not resolve hostname to IP")
		return binHost // Return zero-filled array
	}

	// Use first IP address
	ip := ips[0]
	if ipv4 := ip.To4(); ipv4 != nil {
		// IPv4 - store in IPv6 mapped format
		copy(binHost[12:], ipv4)
		binHost[10] = 0xff
		binHost[11] = 0xff
	} else if ipv6 := ip.To16(); ipv6 != nil {
		// IPv6
		copy(binHost[:], ipv6)
	}

	return binHost
}

// compareBinaryHosts compares two binary host addresses (like C++ Socket::matchIPv6Addr)
func (lb *LoadBalancer) compareBinaryHosts(host1, host2 [16]byte) bool {
	// Compare all 16 bytes
	for i := 0; i < 16; i++ {
		if host1[i] != host2[i] {
			return false
		}
	}
	return true
}

// GetAllNodes returns all nodes from unified state (including unhealthy/stale for debugging)
func (lb *LoadBalancer) GetAllNodes() []state.EnhancedBalancerNodeSnapshot {
	snapshot := state.DefaultManager().GetAllNodesSnapshot()
	if snapshot == nil {
		return []state.EnhancedBalancerNodeSnapshot{}
	}
	return snapshot.Nodes
}

// GetNodes returns nodes map from unified state
func (lb *LoadBalancer) GetNodes() map[string]state.NodeState {
	snapshot := state.DefaultManager().GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return map[string]state.NodeState{}
	}

	result := make(map[string]state.NodeState)
	for _, snap := range snapshot.Nodes {
		if nodeState := state.DefaultManager().GetNodeState(snap.NodeID); nodeState != nil {
			result[snap.Host] = *nodeState
		}
	}
	return result
}

// GetNodeByID looks up a node's BaseURL by NodeID from unified state
func (lb *LoadBalancer) GetNodeByID(nodeID string) (string, error) {
	node := state.DefaultManager().GetNodeState(nodeID)
	if node == nil {
		return "", fmt.Errorf("node with ID %s not found", nodeID)
	}
	return node.BaseURL, nil
}

// GetNodeIDByHost returns the NodeID for a given host from unified state
func (lb *LoadBalancer) GetNodeIDByHost(host string) string {
	snapshot := state.DefaultManager().GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return ""
	}
	for _, snap := range snapshot.Nodes {
		if snap.Host == host {
			return snap.NodeID
		}
	}
	return ""
}

// GetBestNode finds the best node using EXACT C++ rate() algorithm
func (lb *LoadBalancer) GetBestNode(ctx context.Context, streamName string, lat, lon float64, tagAdjust map[string]int) (string, error) {
	// Default to source selection (true) for backward compatibility/internal use
	host, _, _, _, _, err := lb.GetBestNodeWithScore(ctx, streamName, lat, lon, tagAdjust, "", true)
	return host, err
}

// NodeWithScore contains node info with its score
type NodeWithScore struct {
	Host         string
	NodeID       string
	Score        uint64
	GeoLatitude  float64
	GeoLongitude float64
	LocationName string
}

type nodeRejectionReason string

const (
	rejectHostInvalid      nodeRejectionReason = "node metrics not ready"
	rejectBandwidthExhaust nodeRejectionReason = "node out of bandwidth"
	rejectStreamMissing    nodeRejectionReason = "stream missing on node"
	rejectStreamNoInputs   nodeRejectionReason = "stream has no inputs on node"
	rejectStreamReplicated nodeRejectionReason = "stream is replicated on node (excluded for source selection)"
	rejectConfigStreams    nodeRejectionReason = "stream not allowed by node config"
	rejectAdjustedToZero   nodeRejectionReason = "score adjusted to zero"
)

// GetBestNodeWithScore finds the best node and returns both node and score
func (lb *LoadBalancer) GetBestNodeWithScore(ctx context.Context, streamName string, lat, lon float64, tagAdjust map[string]int, clientIP string, isSourceSelection bool) (string, uint64, float64, float64, string, error) {
	nodes, err := lb.GetTopNodesWithScores(ctx, streamName, lat, lon, tagAdjust, clientIP, 1, isSourceSelection)
	if err != nil {
		return "", 0, 0, 0, "", err
	}
	if len(nodes) == 0 {
		return "", 0, 0, 0, "", fmt.Errorf("no suitable nodes found")
	}
	best := nodes[0]

	// No virtual viewer tracking needed - rely on USER_NEW/USER_END triggers

	return best.Host, best.Score, best.GeoLatitude, best.GeoLongitude, best.LocationName, nil
}

func (lb *LoadBalancer) GetTopNodesWithScores(ctx context.Context, streamName string, lat, lon float64, tagAdjust map[string]int, clientIP string, maxNodes int, isSourceSelection bool) ([]NodeWithScore, error) {
	snapshot := state.DefaultManager().GetBalancerSnapshotAtomic()
	if snapshot == nil || len(snapshot.Nodes) == 0 {
		return nil, fmt.Errorf("no nodes available in unified state")
	}

	type scoredNode struct {
		snap  state.EnhancedBalancerNodeSnapshot
		score uint64
	}
	var scoredNodes []scoredNode

	// Parse capability requirements
	requireCap, _ := ctx.Value("cap").(string)
	var reqs []string
	if requireCap != "" {
		for _, p := range strings.Split(requireCap, ",") {
			if v := strings.TrimSpace(p); v != "" {
				reqs = append(reqs, v)
			}
		}
	}

	skipForCap := func(snap state.EnhancedBalancerNodeSnapshot) bool {
		if len(reqs) == 0 {
			return false
		}
		roleSet := make(map[string]bool, len(snap.Roles))
		for _, r := range snap.Roles {
			roleSet[r] = true
		}
		for _, r := range reqs {
			switch r {
			case "ingest":
				if !snap.CapIngest && !roleSet["ingest"] {
					return true
				}
			case "edge":
				if !snap.CapEdge && !roleSet["edge"] {
					return true
				}
			case "storage":
				if !snap.CapStorage && !roleSet["storage"] {
					return true
				}
			case "processing":
				if !snap.CapProcessing && !roleSet["processing"] {
					return true
				}
			default:
				if !roleSet[r] {
					return true
				}
			}
		}
		return false
	}

	var clientBinHost [16]byte
	if clientIP != "" {
		clientBinHost = lb.hostToBinary(clientIP)
	}

	rejections := map[nodeRejectionReason]int{}
	seenCandidates := 0
	skippedForCapCount := 0

	for _, snap := range snapshot.Nodes {
		if !snap.IsActive || skipForCap(snap) {
			if snap.IsActive {
				skippedForCapCount++
			}
			continue
		}

		seenCandidates++

		if streamName != "" && clientIP != "" && state.CompareBinaryHosts(snap.BinHost, clientBinHost) {
			lb.logger.WithFields(logging.Fields{
				"stream": streamName, "host": snap.Host, "client_ip": clientIP,
			}).Info("Ignoring same-host entry for source selection")
			continue
		}

		score, reason := lb.rateNodeWithReason(snap, streamName, lat, lon, tagAdjust, isSourceSelection)
		if score == 0 {
			if reason != "" {
				rejections[reason]++
			}
			continue
		}

		if clientIP != "" && streamName == "" && state.CompareBinaryHosts(snap.BinHost, clientBinHost) {
			score = score * 5
		}

		scoredNodes = append(scoredNodes, scoredNode{snap: snap, score: score})
	}

	if len(scoredNodes) == 0 {
		// More actionable error reporting than the old generic "out of bandwidth" message.
		if seenCandidates == 0 && skippedForCapCount > 0 {
			if requireCap, _ := ctx.Value("cap").(string); strings.TrimSpace(requireCap) != "" {
				return nil, fmt.Errorf("no nodes match required capabilities (%s)", strings.TrimSpace(requireCap))
			}
			return nil, fmt.Errorf("no nodes match required capabilities")
		}
		if streamName != "" {
			missing := rejections[rejectStreamMissing]
			noInputs := rejections[rejectStreamNoInputs]
			if missing > 0 || noInputs > 0 {
				switch {
				case missing > 0 && noInputs == 0:
					return nil, fmt.Errorf("can't find origin for stream %q (not present on any active node)", streamName)
				case noInputs > 0 && missing == 0:
					return nil, fmt.Errorf("can't find origin for stream %q (no active inputs on any node)", streamName)
				default:
					return nil, fmt.Errorf("can't find origin for stream %q (missing or no inputs)", streamName)
				}
			}
		}
		if rejections[rejectBandwidthExhaust] > 0 && len(rejections) == 1 {
			return nil, fmt.Errorf("all suitable nodes are out of bandwidth")
		}
		if rejections[rejectHostInvalid] > 0 && len(rejections) == 1 {
			return nil, fmt.Errorf("node metrics not ready (missing ram_max/bw_limit)")
		}
		if rejections[rejectConfigStreams] > 0 && len(rejections) == 1 {
			return nil, fmt.Errorf("stream %q not allowed by node configuration", streamName)
		}
		return nil, fmt.Errorf("no suitable nodes available")
	}

	// Sort by score (highest first)
	for i := 0; i < len(scoredNodes); i++ {
		for j := i + 1; j < len(scoredNodes); j++ {
			if scoredNodes[j].score > scoredNodes[i].score {
				scoredNodes[i], scoredNodes[j] = scoredNodes[j], scoredNodes[i]
			}
		}
	}

	if maxNodes > 0 && len(scoredNodes) > maxNodes {
		scoredNodes = scoredNodes[:maxNodes]
	}

	result := make([]NodeWithScore, len(scoredNodes))
	for i, sn := range scoredNodes {
		result[i] = NodeWithScore{
			Host: sn.snap.Host, NodeID: sn.snap.NodeID, Score: sn.score,
			GeoLatitude: sn.snap.GeoLatitude, GeoLongitude: sn.snap.GeoLongitude, LocationName: sn.snap.LocationName,
		}
	}

	lb.logger.WithFields(logging.Fields{
		"stream": streamName, "num_nodes": len(result), "winner": result[0].Host,
		"score": result[0].Score, "lat": lat, "lon": lon,
	}).Info("Load balancing decision")

	return result, nil
}

func (lb *LoadBalancer) rateNode(snap state.EnhancedBalancerNodeSnapshot, streamName string, lat, lon float64, tagAdjust map[string]int, isSourceSelection bool) uint64 {
	score, _ := lb.rateNodeWithReason(snap, streamName, lat, lon, tagAdjust, isSourceSelection)
	return score
}

func (lb *LoadBalancer) rateNodeWithReason(snap state.EnhancedBalancerNodeSnapshot, streamName string, lat, lon float64, tagAdjust map[string]int, isSourceSelection bool) (uint64, nodeRejectionReason) {
	// Check if host is valid
	if snap.RAMMax == 0 || snap.BWLimit == 0 {
		lb.logger.WithFields(logging.Fields{
			"host": snap.Host, "ram_max": snap.RAMMax, "bw_limit": snap.BWLimit,
		}).Warn("Host invalid")
		return 0, rejectHostInvalid
	}

	// Check bandwidth limits using pre-computed available bandwidth
	if snap.BWAvailable == 0 {
		lb.logger.WithFields(logging.Fields{
			"host": snap.Host, "up_speed": snap.UpSpeed, "add_bandwidth": snap.AddBandwidth,
			"bw_limit": snap.BWLimit, "bw_available": snap.BWAvailable,
		}).Info("Host over bandwidth")
		return 0, rejectBandwidthExhaust
	}

	// Check stream exists, has inputs, not replicated
	// This runs during source selection (MistServer asking "where can I pull this stream from?")
	// If no node has the stream with active inputs, MistServer falls back to push/local input
	if streamName != "" {
		stream, exists := snap.Streams[streamName]
		if !exists || stream.Inputs == 0 {
			lb.logger.WithFields(logging.Fields{
				"stream": streamName, "host": snap.Host, "exists": exists,
				"inputs": func() uint32 {
					if exists {
						return stream.Inputs
					}
					return 0
				}(),
			}).Debug("Source lookup: node has no active input for stream (will try other nodes or fall back to push)")
			if !exists {
				return 0, rejectStreamMissing
			}
			return 0, rejectStreamNoInputs
		}

		// If selecting a source (Mist pulling), prevent pulling from a replicated stream
		if isSourceSelection && stream.Replicated {
			lb.logger.WithFields(logging.Fields{
				"stream": streamName, "host": snap.Host, "replicated": true,
			}).Info("Stream excluded: replicated node cannot serve as source")
			return 0, rejectStreamReplicated
		}
	}

	// Check config streams
	if len(snap.ConfigStreams) > 0 {
		allowed := false
		for _, confStream := range snap.ConfigStreams {
			if confStream == streamName || strings.HasPrefix(streamName, confStream+"+") || strings.HasPrefix(streamName, confStream+" ") {
				allowed = true
				break
			}
		}
		if !allowed {
			lb.logger.WithFields(logging.Fields{
				"stream": streamName, "host": snap.Host,
			}).Info("Stream not available from host")
			return 0, rejectConfigStreams
		}
	}

	// Get current weights from unified state manager
	weights := state.DefaultManager().GetWeights()

	// Use pre-computed scores for faster calculation
	cpuScore := snap.CPUScore
	ramScore := snap.RAMScore
	bwScore := weights["bw"] - (uint64(snap.UpSpeed+float64(snap.AddBandwidth))*weights["bw"])/uint64(snap.BWLimit)

	// Geographic score (still computed dynamically)
	var geoScore uint64 = 0
	if snap.GeoLatitude != 0 && snap.GeoLongitude != 0 && lat != 0 && lon != 0 {
		distance := lb.geoDist(snap.GeoLatitude, snap.GeoLongitude, lat, lon)
		geoScore = weights["geo"] - uint64(float64(weights["geo"])*distance)
	}

	// Stream bonus
	var streamBonus uint64 = 0
	if _, hasStream := snap.Streams[streamName]; hasStream {
		streamBonus = weights["bonus"]
	}

	// Base score using pre-computed values
	score := cpuScore + ramScore + bwScore + geoScore + streamBonus

	// Apply tag adjustments
	var adjustment int64 = 0
	if len(tagAdjust) > 0 {
		for tagMatch, adj := range tagAdjust {
			adjustment += int64(lb.applyAdjustment(snap.Tags, tagMatch, adj))
		}
	}

	// Apply adjustment
	if adjustment >= 0 || -adjustment < int64(score) {
		score = uint64(int64(score) + adjustment)
	} else {
		score = 0
	}

	lb.logger.WithFields(logging.Fields{
		"host": snap.Host, "cpu_score": cpuScore, "ram_score": ramScore, "stream_bonus": streamBonus,
		"bw_score": bwScore, "bw_max_mbps": uint64(snap.BWLimit) / 1024 / 1024, "geo_score": geoScore,
		"tag_adjustment": adjustment, "final_score": score,
	}).Info("Host scoring details")

	if score == 0 {
		return 0, rejectAdjustedToZero
	}
	return score, ""
}

// geoDist implements EXACT C++ geoDist function
func (lb *LoadBalancer) geoDist(lat1, long1, lat2, long2 float64) float64 {
	const toRadConstant = 1.0 / 57.29577951308232087684 // Exact C++ constant

	lat1Rad := lat1 * toRadConstant
	long1Rad := long1 * toRadConstant
	lat2Rad := lat2 * toRadConstant
	long2Rad := long2 * toRadConstant

	dist := math.Sin(lat1Rad)*math.Sin(lat2Rad) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Cos(long1Rad-long2Rad)
	return 0.31830988618379067153 * math.Acos(dist) // Exact C++ constants
}

// applyAdjustment implements EXACT C++ applyAdjustment function
func (lb *LoadBalancer) applyAdjustment(tags []string, match string, adj int) int {
	if len(match) == 0 {
		return 0
	}

	invert := false
	haveOne := false
	startPos := 0

	// Check for inversion (like C++)
	if match[0] == '-' {
		invert = true
		startPos = 1
	}

	// Convert tags slice to set for faster lookup
	tagSet := make(map[string]bool)
	for _, tag := range tags {
		tagSet[tag] = true
	}

	// Check comma-separated matches (like C++)
	parts := strings.Split(match[startPos:], ",")
	for _, part := range parts {
		if tagSet[strings.TrimSpace(part)] {
			haveOne = true
			break
		}
	}

	// Apply logic (like C++)
	if haveOne == !invert {
		return adj
	}
	return 0
}

// SetWeights updates the scoring weights and triggers score recomputation (like C++ /?weights= endpoint)
func (lb *LoadBalancer) SetWeights(cpu, ram, bandwidth, geo, streamBonus uint64) {
	// Delegate to unified state manager
	state.DefaultManager().SetWeights(cpu, ram, bandwidth, geo, streamBonus)

	lb.logger.WithFields(logging.Fields{
		"cpu":          cpu,
		"ram":          ram,
		"bandwidth":    bandwidth,
		"geo":          geo,
		"stream_bonus": streamBonus,
	}).Info("Updated load balancer weights - delegated to unified state manager")
}

// GetWeights returns current weights from unified state manager
func (lb *LoadBalancer) GetWeights() map[string]uint64 {
	return state.DefaultManager().GetWeights()
}

// GetStreamsByTenant returns all active streams for a specific tenant
func (lb *LoadBalancer) GetStreamsByTenant(tenantID string) []*state.StreamState {
	return state.DefaultManager().GetStreamsByTenant(tenantID)
}

// GetStreamInstances returns per-node instances for a specific stream
func (lb *LoadBalancer) GetStreamInstances(internalName string) map[string]state.StreamInstanceState {
	return state.DefaultManager().GetStreamInstances(internalName)
}
