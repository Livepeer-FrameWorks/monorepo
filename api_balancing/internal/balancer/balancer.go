package balancer

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"frameworks/pkg/logging"
)

// LoadBalancer is the main load balancer instance
type LoadBalancer struct {
	db     *sql.DB
	logger logging.Logger
	nodes  map[string]*Node
	mu     sync.RWMutex

	// Configurable weights (exactly like C++ version)
	WeightCPU   uint64
	WeightRAM   uint64
	WeightBW    uint64
	WeightGeo   uint64
	WeightBonus uint64
}

// Node represents a MistServer node (matches C++ hostDetails exactly)
type Node struct {
	Host           string            `json:"host"`
	BinHost        [16]byte          `json:"bin_host"` // Binary IP address (IPv6 compatible)
	Port           int               `json:"port"`
	DTSCPort       int               `json:"dtsc_port"`
	Tags           []string          `json:"tags"`
	GeoLatitude    float64           `json:"geo_latitude"`
	GeoLongitude   float64           `json:"geo_longitude"`
	CPU            uint64            `json:"cpu"` // 0-1000 (like C++)
	RAMMax         uint64            `json:"ram_max"`
	RAMCurrent     uint64            `json:"ram_current"`
	UpSpeed        uint64            `json:"up_speed"`        // bytes/sec
	DownSpeed      uint64            `json:"down_speed"`      // bytes/sec
	AvailBandwidth uint64            `json:"avail_bandwidth"` // bytes/sec
	AddBandwidth   uint64            `json:"add_bandwidth"`   // penalty bandwidth
	IsActive       bool              `json:"is_active"`
	LastUpdate     time.Time         `json:"last_update"`
	Streams        map[string]Stream `json:"streams"`
	ConfigStreams  []string          `json:"config_streams"` // streams this node can serve
}

// Stream represents stream information on a node (matches C++ streamDetails)
type Stream struct {
	Total      uint64 `json:"total"`     // viewer count
	Inputs     uint32 `json:"inputs"`    // input count (for ingest)
	Bandwidth  uint32 `json:"bandwidth"` // bandwidth per viewer
	PrevTotal  uint64 `json:"prev_total"`
	BytesUp    uint64 `json:"bytes_up"`
	BytesDown  uint64 `json:"bytes_down"`
	Replicated bool   `json:"replicated"` // whether this stream is replicated from another node
}

// NewLoadBalancer creates a new load balancer with C++ defaults
func NewLoadBalancer(db *sql.DB, logger logging.Logger) *LoadBalancer {
	return &LoadBalancer{
		db:          db,
		logger:      logger,
		nodes:       make(map[string]*Node),
		WeightCPU:   500,  // Same as C++
		WeightRAM:   500,  // Same as C++
		WeightBW:    1000, // Same as C++
		WeightGeo:   1000, // Same as C++
		WeightBonus: 50,   // Same as C++ (not 200!)
	}
}

// AddNode adds a node to the load balancer
func (lb *LoadBalancer) AddNode(host string, port int) error {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	// Resolve host to binary IP address (like C++)
	binHost := lb.hostToBinary(host)

	node := &Node{
		Host:           host,
		BinHost:        binHost,
		Port:           port,
		DTSCPort:       4200,              // Default DTSC port
		CPU:            1000,              // Start at max load (like C++)
		AvailBandwidth: 128 * 1024 * 1024, // Assume 1G connection (like C++)
		IsActive:       true,
		Streams:        make(map[string]Stream),
		Tags:           make([]string, 0),
		ConfigStreams:  make([]string, 0),
	}

	lb.nodes[host] = node
	lb.logger.WithField("host", host).Info("Added node to load balancer")

	return nil
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

// RemoveNode removes a node from the load balancer
func (lb *LoadBalancer) RemoveNode(host string) error {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if _, exists := lb.nodes[host]; exists {
		delete(lb.nodes, host)
		lb.logger.WithField("host", host).Info("Removed node from load balancer")
	}

	return nil
}

// UpdateNodeMetrics updates metrics for a node (called by Helmsman)
func (lb *LoadBalancer) UpdateNodeMetrics(host string, data map[string]interface{}) error {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	node, exists := lb.nodes[host]
	if !exists {
		return fmt.Errorf("node %s not found", host)
	}

	// Update metrics exactly like C++ update() method
	if cpu, ok := data["cpu"].(float64); ok {
		node.CPU = uint64(cpu)
	}
	if ramMax, ok := data["ram_max"].(float64); ok {
		node.RAMMax = uint64(ramMax)
	}
	if ramCurr, ok := data["ram_current"].(float64); ok {
		node.RAMCurrent = uint64(ramCurr)
	}
	if upSpeed, ok := data["up_speed"].(float64); ok {
		node.UpSpeed = uint64(upSpeed)
	}
	if downSpeed, ok := data["down_speed"].(float64); ok {
		node.DownSpeed = uint64(downSpeed)
	}
	if bwLimit, ok := data["bwlimit"].(float64); ok && bwLimit > 0 {
		node.AvailBandwidth = uint64(bwLimit)
	}

	// Update geo location
	if loc, ok := data["loc"].(map[string]interface{}); ok {
		if lat, ok := loc["lat"].(float64); ok {
			node.GeoLatitude = lat
		}
		if lon, ok := loc["lon"].(float64); ok {
			node.GeoLongitude = lon
		}
	}

	// Update tags
	if tags, ok := data["tags"].([]interface{}); ok {
		node.Tags = make([]string, len(tags))
		for i, tag := range tags {
			if tagStr, ok := tag.(string); ok {
				node.Tags[i] = tagStr
			}
		}
	}

	// Update streams (exactly like C++)
	if streams, ok := data["streams"].(map[string]interface{}); ok {
		node.Streams = make(map[string]Stream)
		for streamName, streamData := range streams {
			if streamMap, ok := streamData.(map[string]interface{}); ok {
				stream := Stream{}
				if total, ok := streamMap["total"].(uint64); ok {
					stream.Total = total
				} else if total, ok := streamMap["total"].(float64); ok {
					stream.Total = uint64(total)
				}
				if inputs, ok := streamMap["inputs"].(uint32); ok {
					stream.Inputs = inputs
				} else if inputs, ok := streamMap["inputs"].(float64); ok {
					stream.Inputs = uint32(inputs)
				}
				if bandwidth, ok := streamMap["bandwidth"].(uint32); ok {
					stream.Bandwidth = bandwidth
				} else if bandwidth, ok := streamMap["bandwidth"].(float64); ok {
					stream.Bandwidth = uint32(bandwidth)
				}
				if bytesUp, ok := streamMap["bytes_up"].(uint64); ok {
					stream.BytesUp = bytesUp
				} else if bytesUp, ok := streamMap["bytes_up"].(float64); ok {
					stream.BytesUp = uint64(bytesUp)
				}
				if bytesDown, ok := streamMap["bytes_down"].(uint64); ok {
					stream.BytesDown = bytesDown
				} else if bytesDown, ok := streamMap["bytes_down"].(float64); ok {
					stream.BytesDown = uint64(bytesDown)
				}
				// Check for replication flag (matches C++ strm.rep parsing)
				if replicated, ok := streamMap["replicated"].(bool); ok {
					stream.Replicated = replicated
				}
				node.Streams[streamName] = stream
			}
		}
	}

	// Update config streams
	if confStreams, ok := data["conf_streams"].([]interface{}); ok {
		node.ConfigStreams = make([]string, len(confStreams))
		for i, stream := range confStreams {
			if streamStr, ok := stream.(string); ok {
				node.ConfigStreams[i] = streamStr
			}
		}
	}

	// Decay add bandwidth (like C++)
	node.AddBandwidth = uint64(float64(node.AddBandwidth) * 0.75)
	node.LastUpdate = time.Now()

	return nil
}

// GetAllNodes returns all nodes
func (lb *LoadBalancer) GetAllNodes() []*Node {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	nodes := make([]*Node, 0, len(lb.nodes))
	for _, node := range lb.nodes {
		nodes = append(nodes, node)
	}

	return nodes
}

// Add this method to the LoadBalancer struct
func (lb *LoadBalancer) GetNodes() map[string]*Node {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return lb.nodes
}

// GetBestNode finds the best node using EXACT C++ rate() algorithm
func (lb *LoadBalancer) GetBestNode(ctx context.Context, streamName string, lat, lon float64, tagAdjust map[string]int) (string, error) {
	host, _, err := lb.GetBestNodeWithScore(ctx, streamName, lat, lon, tagAdjust, "")
	return host, err
}

// GetBestNodeWithScore finds the best node and returns both node and score
func (lb *LoadBalancer) GetBestNodeWithScore(ctx context.Context, streamName string, lat, lon float64, tagAdjust map[string]int, clientIP string) (string, uint64, error) {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	var bestHost *Node
	var bestScore uint64 = 0

	// Get client's binary IP for same-host detection
	var clientBinHost [16]byte
	if clientIP != "" {
		clientBinHost = lb.hostToBinary(clientIP)
	}

	for _, node := range lb.nodes {
		if !node.IsActive {
			continue
		}

		// Skip same-host nodes for source selection (like C++)
		if streamName != "" && clientIP != "" && lb.compareBinaryHosts(node.BinHost, clientBinHost) {
			lb.logger.WithFields(logging.Fields{
				"stream":    streamName,
				"host":      node.Host,
				"client_ip": clientIP,
			}).Debug("Ignoring same-host entry for source selection")
			continue
		}

		// Calculate score using EXACT C++ rate() method
		score := lb.rateNode(node, streamName, lat, lon, tagAdjust)
		if score > bestScore {
			bestHost = node
			bestScore = score
		}
	}

	if bestScore == 0 || bestHost == nil {
		return "", 0, fmt.Errorf("all servers seem to be out of bandwidth")
	}

	lb.logger.WithFields(logging.Fields{
		"stream": streamName,
		"winner": bestHost.Host,
		"score":  bestScore,
		"lat":    lat,
		"lon":    lon,
	}).Info("Load balancing decision")

	// Add viewer (like C++)
	lb.addViewer(bestHost, streamName)

	return bestHost.Host, bestScore, nil
}

// rateNode implements the EXACT C++ rate() method
func (lb *LoadBalancer) rateNode(node *Node, streamName string, lat, lon float64, tagAdjust map[string]int) uint64 {
	// Check if host is valid (like C++)
	if node.RAMMax == 0 || node.AvailBandwidth == 0 {
		lb.logger.WithFields(logging.Fields{
			"host":      node.Host,
			"ram_max":   node.RAMMax,
			"bandwidth": node.AvailBandwidth,
		}).Warn("Host invalid")
		return 0
	}

	// Check bandwidth limits (like C++)
	if node.UpSpeed >= node.AvailBandwidth || (node.UpSpeed+node.AddBandwidth) >= node.AvailBandwidth {
		lb.logger.WithFields(logging.Fields{
			"host":            node.Host,
			"up_speed":        node.UpSpeed,
			"add_bandwidth":   node.AddBandwidth,
			"avail_bandwidth": node.AvailBandwidth,
		}).Info("Host over bandwidth")
		return 0
	}

	// Check if stream exists, has inputs, and is not replicated (EXACT C++ source() method line 279)
	if streamName != "" {
		stream, exists := node.Streams[streamName]
		if !exists || stream.Inputs == 0 || stream.Replicated {
			lb.logger.WithFields(logging.Fields{
				"stream": streamName,
				"host":   node.Host,
				"exists": exists,
				"inputs": func() uint32 {
					if exists {
						return stream.Inputs
					} else {
						return 0
					}
				}(),
				"replicated": func() bool {
					if exists {
						return stream.Replicated
					} else {
						return false
					}
				}(),
			}).Debug("Stream not suitable for source: missing, no inputs, or replicated")
			return 0
		}
	}

	// Check config streams (like C++)
	if len(node.ConfigStreams) > 0 {
		allowed := false
		for _, confStream := range node.ConfigStreams {
			if confStream == streamName {
				allowed = true
				break
			}
			// Check prefix match (like C++)
			if strings.HasPrefix(streamName, confStream+"+") || strings.HasPrefix(streamName, confStream+" ") {
				allowed = true
				break
			}
		}
		if !allowed {
			lb.logger.WithFields(logging.Fields{
				"stream": streamName,
				"host":   node.Host,
			}).Debug("Stream not available from host")
			return 0
		}
	}

	// Calculate scores EXACTLY like C++
	cpuScore := lb.WeightCPU - (node.CPU*lb.WeightCPU)/1000
	ramScore := lb.WeightRAM - ((node.RAMCurrent * lb.WeightRAM) / node.RAMMax)
	bwScore := lb.WeightBW - (((node.UpSpeed + node.AddBandwidth) * lb.WeightBW) / node.AvailBandwidth)

	// Geographic score (like C++)
	var geoScore uint64 = 0
	if node.GeoLatitude != 0 && node.GeoLongitude != 0 && lat != 0 && lon != 0 {
		distance := lb.geoDist(node.GeoLatitude, node.GeoLongitude, lat, lon)
		geoScore = lb.WeightGeo - uint64(float64(lb.WeightGeo)*distance)
	}

	// Stream bonus (like C++)
	var streamBonus uint64 = 0
	if _, hasStream := node.Streams[streamName]; hasStream {
		streamBonus = lb.WeightBonus
	}

	// Base score
	score := cpuScore + ramScore + bwScore + geoScore + streamBonus

	// Apply tag adjustments EXACTLY like C++
	var adjustment int64 = 0
	if len(tagAdjust) > 0 {
		for tagMatch, adj := range tagAdjust {
			adjustment += int64(lb.applyAdjustment(node.Tags, tagMatch, adj))
		}
	}

	// Apply adjustment (like C++)
	if adjustment >= 0 || -adjustment < int64(score) {
		score = uint64(int64(score) + adjustment)
	} else {
		score = 0
	}

	// Log detailed scoring (like C++)
	lb.logger.WithFields(logging.Fields{
		"host":           node.Host,
		"cpu_score":      cpuScore,
		"ram_score":      ramScore,
		"stream_bonus":   streamBonus,
		"bw_score":       bwScore,
		"bw_max_mbps":    node.AvailBandwidth / 1024 / 1024,
		"geo_score":      geoScore,
		"tag_adjustment": adjustment,
		"final_score":    score,
	}).Debug("Host scoring details")

	return score
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

// addViewer implements C++ addViewer method
func (lb *LoadBalancer) addViewer(node *Node, streamName string) {
	var toAdd uint64 = 0

	if stream, exists := node.Streams[streamName]; exists {
		toAdd = uint64(stream.Bandwidth)
	} else {
		// Calculate estimated bandwidth like C++
		total := uint64(0)
		for _, stream := range node.Streams {
			total += stream.Total
		}
		if total > 0 {
			toAdd = (node.UpSpeed + node.DownSpeed) / total
		} else {
			toAdd = 131072 // assume 1mbps (like C++)
		}
	}

	// Ensure reasonable limits (like C++)
	if toAdd < 64*1024 {
		toAdd = 64 * 1024 // minimum 0.5 mbps
	}
	if toAdd > 1024*1024 {
		toAdd = 1024 * 1024 // maximum 8 mbps
	}

	node.AddBandwidth += toAdd
}

// UpdateStreamHealth updates the health status of a stream on a node
func (lb *LoadBalancer) UpdateStreamHealth(host string, streamName string, isHealthy bool, details map[string]interface{}) error {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	node, exists := lb.nodes[host]
	if !exists {
		return fmt.Errorf("node %s not found", host)
	}

	// Get or create stream
	stream, exists := node.Streams[streamName]
	if !exists {
		stream = Stream{}
	}

	// Update stream health
	if !isHealthy {
		// If stream is not healthy, mark it as having no viewers
		stream.Total = 0
		stream.Inputs = 0
		stream.Bandwidth = 0
		stream.BytesUp = 0
		stream.BytesDown = 0
	} else {
		// If stream is healthy, ensure it has at least basic metrics
		if stream.Total == 0 {
			stream.Total = 1 // At least one viewer
		}
		if stream.Bandwidth == 0 {
			stream.Bandwidth = 131072 // Default 1mbps like C++
		}
	}

	// Update additional details if provided
	if details != nil {
		if bufferState, ok := details["buffer_state"].(string); ok {
			// Buffer state affects health
			switch bufferState {
			case "EMPTY", "DRY":
				stream.Total = 0
				stream.Inputs = 0
			case "FULL", "RECOVER":
				if stream.Total == 0 {
					stream.Total = 1
				}
			}
		}

		// Update bandwidth if provided
		if bwData, ok := details["bandwidth_data"].(string); ok {
			if bw, err := strconv.ParseUint(bwData, 10, 32); err == nil {
				stream.Bandwidth = uint32(bw)
			}
		}
	}

	// Update the stream in the node
	node.Streams[streamName] = stream

	lb.logger.WithFields(logging.Fields{
		"host":         host,
		"stream":       streamName,
		"is_healthy":   isHealthy,
		"total":        stream.Total,
		"bandwidth":    stream.Bandwidth,
		"buffer_state": details["buffer_state"],
	}).Info("Updated stream health status")

	return nil
}

// HandleNodeShutdown marks a node as inactive and cleans up its state
func (lb *LoadBalancer) HandleNodeShutdown(host string, reason string, details map[string]interface{}) error {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	node, exists := lb.nodes[host]
	if !exists {
		return fmt.Errorf("node %s not found", host)
	}

	// Mark node as inactive
	node.IsActive = false

	// Clear all streams since node is shutting down
	node.Streams = make(map[string]Stream)

	// Log shutdown
	lb.logger.WithFields(logging.Fields{
		"host":    host,
		"reason":  reason,
		"details": details,
	}).Info("Node marked as inactive due to shutdown")

	return nil
}

// SetWeights updates the scoring weights (like C++ /?weights= endpoint)
func (lb *LoadBalancer) SetWeights(cpu, ram, bandwidth, geo, streamBonus uint64) {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	lb.WeightCPU = cpu
	lb.WeightRAM = ram
	lb.WeightBW = bandwidth
	lb.WeightGeo = geo
	lb.WeightBonus = streamBonus

	lb.logger.WithFields(logging.Fields{
		"cpu":          cpu,
		"ram":          ram,
		"bandwidth":    bandwidth,
		"geo":          geo,
		"stream_bonus": streamBonus,
	}).Info("Updated load balancer weights")
}

// GetWeights returns current weights
func (lb *LoadBalancer) GetWeights() map[string]uint64 {
	lb.mu.RLock()
	defer lb.mu.RUnlock()

	return map[string]uint64{
		"cpu":   lb.WeightCPU,
		"ram":   lb.WeightRAM,
		"bw":    lb.WeightBW,
		"geo":   lb.WeightGeo,
		"bonus": lb.WeightBonus,
	}
}
