package control

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"strings"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/state"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// ContentResolution contains the result of resolving a playback request input
type ContentResolution struct {
	ContentType  string // "live", "clip", "dvr"
	ContentId    string // Internal name (for live) or hash (for clip/dvr)
	FixedNode    string // Storage node URL for VOD content, empty for live
	FixedNodeID  string // Storage node ID for VOD content
	TenantId     string
	InternalName string // Original stream internal name (for clips/DVR: the source stream)
}

// ResolveContent determines content type and resolution strategy for any input.
// Input can be: view key, clip hash, DVR hash, or internal name.
// This is the unified resolution function that replaces scattered resolution logic.
func ResolveContent(ctx context.Context, input string) (*ContentResolution, error) {
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	// 1. Check if already prefixed (internal name format)
	if strings.HasPrefix(input, "live+") {
		internalName := strings.TrimPrefix(input, "live+")
		return &ContentResolution{
			ContentType:  "live",
			ContentId:    internalName,
			InternalName: internalName,
		}, nil
	}
	if strings.HasPrefix(input, "vod+") {
		hash := strings.TrimPrefix(input, "vod+")
		// Need to determine if it's clip or DVR
		res := &ContentResolution{
			ContentId:    hash,
			InternalName: hash,
		}
		if db != nil {
			// Check clips first
			var tenantID, streamName string
			err := db.QueryRowContext(ctx, "SELECT tenant_id, stream_name FROM foghorn.clips WHERE clip_hash = $1 AND status != 'deleted'", hash).Scan(&tenantID, &streamName)
			if err == nil {
				res.ContentType = "clip"
				res.TenantId = tenantID
				res.InternalName = streamName
				return res, nil
			}
			// Check DVR
			err = db.QueryRowContext(ctx, "SELECT tenant_id, internal_name FROM foghorn.dvr_requests WHERE request_hash = $1", hash).Scan(&tenantID, &streamName)
			if err == nil {
				res.ContentType = "dvr"
				res.TenantId = tenantID
				res.InternalName = streamName
				return res, nil
			}
		}
		// Default to clip if can't determine
		res.ContentType = "clip"
		return res, nil
	}

	// 2. Check VOD Artifacts in-memory (clip hashes, DVR hashes)
	if host, artifact := state.DefaultManager().FindNodeByArtifactHash(input); host != "" {
		res := &ContentResolution{
			ContentId: input,
			FixedNode: host,
		}

		// Get node ID from host URL
		if loadBalancerInstance != nil {
			res.FixedNodeID = loadBalancerInstance.GetNodeIDByHost(host)
		}

		// Determine content type and get tenant info from DB
		if db != nil {
			// Check clips first
			var tenantID, streamName string
			err := db.QueryRowContext(ctx, "SELECT tenant_id, stream_name FROM foghorn.clips WHERE clip_hash = $1 AND status != 'deleted'", input).Scan(&tenantID, &streamName)
			if err == nil {
				res.ContentType = "clip"
				res.TenantId = tenantID
				res.InternalName = streamName
				return res, nil
			}
			// Check DVR
			err = db.QueryRowContext(ctx, "SELECT tenant_id, internal_name FROM foghorn.dvr_requests WHERE request_hash = $1", input).Scan(&tenantID, &streamName)
			if err == nil {
				res.ContentType = "dvr"
				res.TenantId = tenantID
				res.InternalName = streamName
				return res, nil
			}
		}

		// Fallback: use artifact info if available
		if artifact != nil {
			res.ContentType = "clip" // Artifacts are primarily clips
		}
		return res, nil
	}

	// 3. Try Commodore for view key resolution (live streams)
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolvePlaybackID(ctx, input); err == nil && resp.InternalName != "" {
			return &ContentResolution{
				ContentType:  "live",
				ContentId:    resp.InternalName,
				TenantId:     resp.TenantId,
				InternalName: resp.InternalName,
			}, nil
		}
	}

	// 4. Check database directly for clip/DVR by hash (in case not in memory yet)
	if db != nil {
		// Check clips
		var tenantID, streamName, nodeID string
		err := db.QueryRowContext(ctx, "SELECT tenant_id, stream_name, COALESCE(node_id, '') FROM foghorn.clips WHERE clip_hash = $1 AND status != 'deleted'", input).Scan(&tenantID, &streamName, &nodeID)
		if err == nil {
			res := &ContentResolution{
				ContentType:  "clip",
				ContentId:    input,
				TenantId:     tenantID,
				InternalName: streamName,
				FixedNodeID:  nodeID,
			}
			// Get node URL
			if nodeID != "" {
				if outputs, ok := GetNodeOutputs(nodeID); ok {
					res.FixedNode = outputs.BaseURL
				}
			}
			return res, nil
		}

		// Check DVR
		err = db.QueryRowContext(ctx, "SELECT tenant_id, internal_name, COALESCE(storage_node_id, '') FROM foghorn.dvr_requests WHERE request_hash = $1", input).Scan(&tenantID, &streamName, &nodeID)
		if err == nil {
			res := &ContentResolution{
				ContentType:  "dvr",
				ContentId:    input,
				TenantId:     tenantID,
				InternalName: streamName,
				FixedNodeID:  nodeID,
			}
			// Get node URL
			if nodeID != "" {
				if outputs, ok := GetNodeOutputs(nodeID); ok {
					res.FixedNode = outputs.BaseURL
				}
			}
			return res, nil
		}
	}

	// 5. Fallback: treat as internal name (live stream)
	return &ContentResolution{
		ContentType:  "live",
		ContentId:    input,
		InternalName: input,
	}, nil
}

// PlaybackDependencies contains dependencies needed for playback resolution
type PlaybackDependencies struct {
	DB     *sql.DB
	LB     *balancer.LoadBalancer
	GeoLat float64
	GeoLon float64
}

// ResolveClipPlayback resolves playback endpoints for a clip
func ResolveClipPlayback(ctx context.Context, deps *PlaybackDependencies, clipHash string) (*pb.ViewerEndpointResponse, error) {
	if deps.DB == nil {
		return nil, fmt.Errorf("database not available")
	}

	var tenantID, streamName, title, description, nodeID, clipStatus string
	var startTime, clipDuration int64
	var sizeBytes sql.NullInt64
	var createdAt sql.NullTime

	err := deps.DB.QueryRowContext(ctx, `
		SELECT tenant_id, stream_name, COALESCE(title, ''), COALESCE(description, ''),
		       COALESCE(node_id, ''), status, start_time, duration, size_bytes, created_at
		FROM foghorn.clips
		WHERE clip_hash = $1 AND status != 'deleted'
	`, clipHash).Scan(&tenantID, &streamName, &title, &description, &nodeID, &clipStatus, &startTime, &clipDuration, &sizeBytes, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("clip not found")
		}
		return nil, fmt.Errorf("failed to query clip: %v", err)
	}

	if nodeID == "" {
		return nil, fmt.Errorf("clip storage node unknown")
	}

	nodeOutputs, exists := GetNodeOutputs(nodeID)
	if !exists || nodeOutputs.Outputs == nil {
		return nil, fmt.Errorf("storage node outputs not available")
	}

	// Build URLs using clip hash (MistServer resolves via PLAY_REWRITE trigger)
	var protocol, endpointURL string
	if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
		protocol = "hls"
		endpointURL = ResolveTemplateURL(hlsURL, nodeOutputs.BaseURL, clipHash)
	} else {
		endpointURL = EnsureTrailingSlash(nodeOutputs.BaseURL) + clipHash + ".html"
		protocol = "html"
	}

	endpoint := &pb.ViewerEndpoint{
		NodeId:      nodeID,
		BaseUrl:     nodeOutputs.BaseURL,
		Protocol:    protocol,
		Url:         endpointURL,
		GeoDistance: 0,
		LoadScore:   0,
		Outputs:     BuildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, clipHash, false),
	}

	metadata := &pb.PlaybackMetadata{
		Status:      clipStatus,
		IsLive:      false,
		TenantId:    tenantID,
		ContentId:   clipHash,
		ContentType: "clip",
		ClipSource:  &streamName,
	}

	if title != "" {
		metadata.Title = &title
	}
	if description != "" {
		metadata.Description = &description
	}
	if clipDuration > 0 {
		d := int32(clipDuration / 1000)
		metadata.DurationSeconds = &d
	}
	if sizeBytes.Valid {
		metadata.RecordingSizeBytes = &sizeBytes.Int64
	}
	if createdAt.Valid {
		metadata.CreatedAt = timestamppb.New(createdAt.Time)
	}

	return &pb.ViewerEndpointResponse{
		Primary:   endpoint,
		Fallbacks: []*pb.ViewerEndpoint{},
		Metadata:  metadata,
	}, nil
}

// ResolveDVRPlayback resolves playback endpoints for a DVR recording
func ResolveDVRPlayback(ctx context.Context, deps *PlaybackDependencies, dvrHash string) (*pb.ViewerEndpointResponse, error) {
	if deps.DB == nil {
		return nil, fmt.Errorf("database not available")
	}

	var tenantID, internalName, nodeID, dvrStatus string
	var duration, recordingSize sql.NullInt64
	var manifestPath sql.NullString
	var createdAt sql.NullTime

	err := deps.DB.QueryRowContext(ctx, `
		SELECT tenant_id, internal_name, storage_node_id, status, duration_seconds, size_bytes, manifest_path, created_at
		FROM foghorn.dvr_requests
		WHERE request_hash = $1 AND status = 'completed'
	`, dvrHash).Scan(&tenantID, &internalName, &nodeID, &dvrStatus, &duration, &recordingSize, &manifestPath, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("DVR recording not found")
		}
		return nil, fmt.Errorf("failed to query DVR: %v", err)
	}

	nodeOutputs, exists := GetNodeOutputs(nodeID)
	if !exists || nodeOutputs.Outputs == nil {
		return nil, fmt.Errorf("storage node outputs not available")
	}

	// Build URLs using DVR hash (MistServer resolves via PLAY_REWRITE trigger)
	var protocol, endpointURL string
	if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
		protocol = "hls"
		endpointURL = ResolveTemplateURL(hlsURL, nodeOutputs.BaseURL, dvrHash)
	} else {
		endpointURL = EnsureTrailingSlash(nodeOutputs.BaseURL) + dvrHash + ".html"
		protocol = "html"
	}

	endpoint := &pb.ViewerEndpoint{
		NodeId:      nodeID,
		BaseUrl:     nodeOutputs.BaseURL,
		Protocol:    protocol,
		Url:         endpointURL,
		GeoDistance: 0,
		LoadScore:   0,
		Outputs:     BuildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, dvrHash, false),
	}

	metadata := &pb.PlaybackMetadata{
		Status:      "completed",
		IsLive:      false,
		DvrStatus:   "completed",
		TenantId:    tenantID,
		ContentId:   dvrHash,
		ContentType: "dvr",
	}

	if duration.Valid {
		d := int32(duration.Int64)
		metadata.DurationSeconds = &d
	}
	if recordingSize.Valid {
		metadata.RecordingSizeBytes = &recordingSize.Int64
	}
	if createdAt.Valid {
		metadata.CreatedAt = timestamppb.New(createdAt.Time)
	}

	return &pb.ViewerEndpointResponse{
		Primary:   endpoint,
		Fallbacks: []*pb.ViewerEndpoint{},
		Metadata:  metadata,
	}, nil
}

// ResolveLivePlayback resolves playback endpoints for a live stream using load balancing
func ResolveLivePlayback(ctx context.Context, deps *PlaybackDependencies, viewKey string, internalName string) (*pb.ViewerEndpointResponse, error) {
	if deps.LB == nil {
		return nil, fmt.Errorf("load balancer not available")
	}

	// Use load balancer with internal name to find nodes that have the stream
	lbctx := context.WithValue(ctx, "cap", "edge")
	nodes, err := deps.LB.GetTopNodesWithScores(lbctx, internalName, deps.GeoLat, deps.GeoLon, make(map[string]int), "", 5, false)
	if err != nil {
		return nil, fmt.Errorf("no suitable edge nodes available: %v", err)
	}

	var endpoints []*pb.ViewerEndpoint

	for _, node := range nodes {
		nodeOutputs, exists := GetNodeOutputs(node.NodeID)
		if !exists || nodeOutputs.Outputs == nil {
			continue
		}

		// Build URLs with view key (MistServer resolves via PLAY_REWRITE trigger)
		var protocol, endpointURL string
		if webrtcURL, ok := nodeOutputs.Outputs["WebRTC"].(string); ok {
			protocol = "webrtc"
			endpointURL = strings.Replace(webrtcURL, "$", viewKey, -1)
			endpointURL = strings.Replace(endpointURL, "HOST", strings.TrimPrefix(nodeOutputs.BaseURL, "https://"), -1)
		} else if hlsURL, ok := nodeOutputs.Outputs["HLS"].(string); ok {
			protocol = "hls"
			endpointURL = strings.Replace(hlsURL, "$", viewKey, -1)
			endpointURL = strings.Trim(endpointURL, "[\"")
		}

		if endpointURL == "" {
			continue
		}

		// Calculate geo distance
		geoDistance := 0.0
		if deps.GeoLat != 0 && deps.GeoLon != 0 && node.GeoLatitude != 0 && node.GeoLongitude != 0 {
			geoDistance = CalculateGeoDistance(deps.GeoLat, deps.GeoLon, node.GeoLatitude, node.GeoLongitude)
		}

		endpoint := &pb.ViewerEndpoint{
			NodeId:      node.NodeID,
			BaseUrl:     nodeOutputs.BaseURL,
			Protocol:    protocol,
			Url:         endpointURL,
			GeoDistance: geoDistance,
			LoadScore:   float64(node.Score),
			Outputs:     BuildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, viewKey, true),
		}
		endpoints = append(endpoints, endpoint)
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("no nodes with suitable outputs available")
	}

	// Build metadata from stream state
	metadata := &pb.PlaybackMetadata{
		Status:      "live",
		IsLive:      true,
		ContentId:   viewKey,
		ContentType: "live",
	}

	// Enrich with stream state if available
	st := state.DefaultManager().GetStreamState(internalName)
	if st != nil {
		metadata.IsLive = st.Status == "live"
		metadata.Status = st.Status
		metadata.Viewers = int32(st.Viewers)
		metadata.BufferState = st.BufferState
	}

	// Add protocol hints
	if len(endpoints) > 0 && endpoints[0].Outputs != nil {
		for proto := range endpoints[0].Outputs {
			metadata.ProtocolHints = append(metadata.ProtocolHints, proto)
		}
	}

	return &pb.ViewerEndpointResponse{
		Primary:   endpoints[0],
		Fallbacks: endpoints[1:],
		Metadata:  metadata,
	}, nil
}

// =============================================================================
// HELPER FUNCTIONS (consolidated from grpc/server.go and handlers/handlers.go)
// =============================================================================

// EnsureTrailingSlash adds a trailing slash if not present
func EnsureTrailingSlash(s string) string {
	if !strings.HasSuffix(s, "/") {
		return s + "/"
	}
	return s
}

// ResolveTemplateURL replaces placeholders in Mist outputs ($ for stream name, HOST for hostname)
func ResolveTemplateURL(raw interface{}, baseURL, streamName string) string {
	var s string
	switch v := raw.(type) {
	case string:
		s = v
	case []interface{}:
		if len(v) > 0 {
			if ss, ok := v[0].(string); ok {
				s = ss
			}
		}
	default:
		return ""
	}
	if s == "" {
		return ""
	}
	s = strings.Replace(s, "$", streamName, -1)
	if strings.Contains(s, "HOST") {
		host := baseURL
		if strings.HasPrefix(host, "https://") {
			host = strings.TrimPrefix(host, "https://")
		}
		if strings.HasPrefix(host, "http://") {
			host = strings.TrimPrefix(host, "http://")
		}
		host = strings.TrimSuffix(host, "/")
		s = strings.Replace(s, "HOST", host, -1)
	}
	s = strings.Trim(s, "[]\"")
	return s
}

// BuildOutputsMap constructs the per-protocol outputs for a node/stream
func BuildOutputsMap(baseURL string, rawOutputs map[string]interface{}, streamName string, isLive bool) map[string]*pb.OutputEndpoint {
	outputs := make(map[string]*pb.OutputEndpoint)

	base := EnsureTrailingSlash(baseURL)
	html := base + streamName + ".html"
	outputs["MIST_HTML"] = &pb.OutputEndpoint{Protocol: "MIST_HTML", Url: html, Capabilities: BuildOutputCapabilities("MIST_HTML", isLive)}
	outputs["PLAYER_JS"] = &pb.OutputEndpoint{Protocol: "PLAYER_JS", Url: base + "player.js", Capabilities: BuildOutputCapabilities("PLAYER_JS", isLive)}

	// WHEP
	if raw, ok := rawOutputs["WHEP"]; ok {
		if u := ResolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WHEP"] = &pb.OutputEndpoint{Protocol: "WHEP", Url: u, Capabilities: BuildOutputCapabilities("WHEP", isLive)}
		}
	}
	if _, ok := outputs["WHEP"]; !ok {
		if u := DeriveWHEPFromHTML(html); u != "" {
			outputs["WHEP"] = &pb.OutputEndpoint{Protocol: "WHEP", Url: u, Capabilities: BuildOutputCapabilities("WHEP", isLive)}
		}
	}

	if raw, ok := rawOutputs["HLS"]; ok {
		if u := ResolveTemplateURL(raw, base, streamName); u != "" {
			outputs["HLS"] = &pb.OutputEndpoint{Protocol: "HLS", Url: u, Capabilities: BuildOutputCapabilities("HLS", isLive)}
		}
	}
	if raw, ok := rawOutputs["DASH"]; ok {
		if u := ResolveTemplateURL(raw, base, streamName); u != "" {
			outputs["DASH"] = &pb.OutputEndpoint{Protocol: "DASH", Url: u, Capabilities: BuildOutputCapabilities("DASH", isLive)}
		}
	}
	if raw, ok := rawOutputs["MP4"]; ok {
		if u := ResolveTemplateURL(raw, base, streamName); u != "" {
			outputs["MP4"] = &pb.OutputEndpoint{Protocol: "MP4", Url: u, Capabilities: BuildOutputCapabilities("MP4", isLive)}
		}
	}
	if raw, ok := rawOutputs["WEBM"]; ok {
		if u := ResolveTemplateURL(raw, base, streamName); u != "" {
			outputs["WEBM"] = &pb.OutputEndpoint{Protocol: "WEBM", Url: u, Capabilities: BuildOutputCapabilities("WEBM", isLive)}
		}
	}
	if raw, ok := rawOutputs["HTTP"]; ok {
		if u := ResolveTemplateURL(raw, base, streamName); u != "" {
			outputs["HTTP"] = &pb.OutputEndpoint{Protocol: "HTTP", Url: u, Capabilities: BuildOutputCapabilities("HTTP", isLive)}
		}
	}

	return outputs
}

// BuildOutputCapabilities returns default capabilities for a given protocol and content type
func BuildOutputCapabilities(protocol string, isLive bool) *pb.OutputCapability {
	caps := &pb.OutputCapability{
		SupportsSeek:          !isLive,
		SupportsQualitySwitch: true,
		HasAudio:              true,
		HasVideo:              true,
	}
	switch strings.ToUpper(protocol) {
	case "WHEP":
		caps.SupportsQualitySwitch = false
		caps.SupportsSeek = false
	case "MP4", "WEBM":
		caps.SupportsQualitySwitch = false
		caps.SupportsSeek = true
	}
	return caps
}

// DeriveWHEPFromHTML derives a WHEP URL by replacing the trailing .../stream.html with .../webrtc/stream
func DeriveWHEPFromHTML(htmlURL string) string {
	u, err := url.Parse(htmlURL)
	if err != nil {
		return ""
	}
	path := strings.Trim(u.Path, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	if !strings.HasSuffix(last, ".html") {
		return ""
	}
	stream := strings.TrimSuffix(last, ".html")
	base := parts[:len(parts)-1]
	base = append(base, "webrtc", stream)
	u.Path = "/" + strings.Join(base, "/")
	return u.String()
}

// CalculateGeoDistance calculates distance in km between two lat/lon points using Haversine formula
func CalculateGeoDistance(lat1, lon1, lat2, lon2 float64) float64 {
	const toRad = math.Pi / 180.0
	lat1Rad := lat1 * toRad
	lon1Rad := lon1 * toRad
	lat2Rad := lat2 * toRad
	lon2Rad := lon2 * toRad
	val := math.Sin(lat1Rad)*math.Sin(lat2Rad) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Cos(lon1Rad-lon2Rad)
	if val > 1 {
		val = 1
	}
	if val < -1 {
		val = -1
	}
	angle := math.Acos(val)
	return 6371.0 * angle
}

// DeriveMistHTTPBase converts a base URL to MistServer HTTP base
func DeriveMistHTTPBase(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		host := strings.TrimPrefix(base, "http://")
		host = strings.TrimPrefix(host, "https://")
		parts := strings.Split(host, ":")
		hostname := parts[0]
		port := "8080"
		return "http://" + hostname + ":" + port
	}
	hostname := u.Hostname()
	port := u.Port()
	if port == "" || port == "4242" {
		port = "8080"
	}
	return u.Scheme + "://" + hostname + ":" + port
}
