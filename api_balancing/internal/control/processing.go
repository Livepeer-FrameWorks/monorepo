package control

import (
	"frameworks/api_balancing/internal/state"
)

// Hardcoded codec support matrix
// Livepeer Gateway only supports H.264 input/output
// Local MistServer (via FFmpeg/hardware) supports additional codecs
var gatewayCodecs = map[string]bool{
	"H264": true,
	"h264": true,
	"AVC":  true,
	"avc":  true,
}

var localCodecs = map[string]bool{
	"H264": true, "h264": true, "AVC": true, "avc": true,
	"H265": true, "h265": true, "HEVC": true, "hevc": true,
	"AV1": true, "av1": true,
	"VP9": true, "vp9": true,
	"VP8": true, "vp8": true,
}

// ProcessingRoute represents the decision on where to route transcoding
type ProcessingRoute struct {
	UseGateway     bool   // Use Livepeer Gateway if true
	GatewayURL     string // Gateway URL (if UseGateway=true)
	ProcessingNode string // Node ID for local processing (if UseGateway=false)
	InputCodec     string // Input codec that was detected
	Reason         string // Human-readable reason for routing decision
}

// SupportsGateway returns true if the codec is supported by Livepeer Gateway
func SupportsGateway(codec string) bool {
	return gatewayCodecs[codec]
}

// SupportsLocal returns true if the codec can be processed locally
func SupportsLocal(codec string) bool {
	return localCodecs[codec]
}

// DecideProcessingRoute determines whether to use Livepeer Gateway or local processing
// based on the input codec and Gateway availability.
//
// Routing logic:
// - H.264 + Gateway available → use Gateway (cost-effective, decentralized)
// - H.264 + Gateway unavailable → use local processing node
// - Non-H.264 (H.265, AV1, VP9) → always use local processing node
func DecideProcessingRoute(inputCodec string) ProcessingRoute {
	gatewayAvailable := livepeerGatewayURL != ""

	// Check if Gateway supports this codec
	if gatewayAvailable && SupportsGateway(inputCodec) {
		return ProcessingRoute{
			UseGateway: true,
			GatewayURL: livepeerGatewayURL,
			InputCodec: inputCodec,
			Reason:     "H.264 input routed to Livepeer Gateway",
		}
	}

	// Fallback to local processing node
	processingNode := findProcessingNode()
	reason := "No Gateway available, using local processing"
	if !SupportsGateway(inputCodec) {
		reason = inputCodec + " not supported by Gateway, using local processing"
	}

	return ProcessingRoute{
		UseGateway:     false,
		ProcessingNode: processingNode,
		InputCodec:     inputCodec,
		Reason:         reason,
	}
}

// findProcessingNode returns the first healthy node with processing capability
func findProcessingNode() string {
	mgr := state.DefaultManager()
	if mgr == nil {
		return ""
	}

	_, nodes := mgr.GetClusterSnapshot()
	for _, node := range nodes {
		if node.CapProcessing && node.IsHealthy {
			return node.NodeID
		}
	}
	return ""
}

// FindProcessingNodes returns all healthy nodes with processing capability
func FindProcessingNodes() []string {
	mgr := state.DefaultManager()
	if mgr == nil {
		return nil
	}

	_, nodes := mgr.GetClusterSnapshot()
	var result []string
	for _, node := range nodes {
		if node.CapProcessing && node.IsHealthy {
			result = append(result, node.NodeID)
		}
	}
	return result
}

// IsGatewayAvailable returns true if Livepeer Gateway is configured
func IsGatewayAvailable() bool {
	return livepeerGatewayURL != ""
}

// GetGatewayURL returns the configured Livepeer Gateway URL
func GetGatewayURL() string {
	return livepeerGatewayURL
}
