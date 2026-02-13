package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/geo"
	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/ctxkeys"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// ContentResolution contains the result of resolving a playback request input
type ContentResolution struct {
	ContentType  string // "live", "clip", "dvr"
	ContentId    string // Public view key (playback_id) for live/clip/dvr/vod
	FixedNode    string // Storage node URL for VOD content, empty for live
	FixedNodeID  string // Storage node ID for VOD content
	TenantId     string
	StreamId     string
	InternalName string                  // Original stream internal name (for clips/DVR: the source stream)
	ClusterPeers []*pb.TenantClusterPeer // Tenant's cluster context from Commodore (free with every resolve)
}

// ResolveContent determines content type and resolution strategy for a public playback ID.
// Input must be a public playback ID (live or artifact).
func ResolveContent(ctx context.Context, input string) (*ContentResolution, error) {
	if input == "" {
		return nil, fmt.Errorf("empty input")
	}

	// 1. Artifact playback_id (clip/dvr/vod)
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolveArtifactPlaybackID(ctx, input); err == nil && resp.Found {
			contentType := strings.ToLower(strings.TrimSpace(resp.ContentType))
			switch contentType {
			case "clip", "dvr", "vod":
			default:
				return nil, fmt.Errorf("invalid artifact content_type %q", resp.ContentType)
			}
			res := &ContentResolution{
				ContentType:  contentType,
				ContentId:    input,
				TenantId:     resp.TenantId,
				StreamId:     resp.StreamId,
				InternalName: resp.ArtifactInternalName,
			}
			if resp.ArtifactHash != "" {
				if host, _ := state.DefaultManager().FindNodeByArtifactHash(resp.ArtifactHash); host != "" {
					res.FixedNode = host
					if loadBalancerInstance != nil {
						res.FixedNodeID = loadBalancerInstance.GetNodeIDByHost(host)
					}
				}
			}
			return res, nil
		}
	}

	// 2. Live playback_id resolution
	if CommodoreClient != nil {
		if resp, err := CommodoreClient.ResolvePlaybackID(ctx, input); err == nil && resp.InternalName != "" {
			return &ContentResolution{
				ContentType:  "live",
				ContentId:    input,
				TenantId:     resp.TenantId,
				StreamId:     resp.StreamId,
				InternalName: resp.InternalName,
				ClusterPeers: resp.ClusterPeers,
			}, nil
		}
	}

	return nil, fmt.Errorf("content not found")
}

// ArtifactFederationClient is the subset of federation.FederationClient needed for cross-cluster artifact resolution.
type ArtifactFederationClient interface {
	PrepareArtifact(ctx context.Context, clusterID, addr string, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error)
}

// PeerAddressResolver resolves gRPC addresses for peer clusters.
type PeerAddressResolver interface {
	GetPeerAddr(clusterID string) string
}

// RemoteArtifactLookup queries the federation cache for hot artifacts on peer edges.
type RemoteArtifactLookup interface {
	GetRemoteArtifacts(ctx context.Context, artifactHash string) ([]*RemoteArtifactInfo, error)
}

// RemoteArtifactInfo describes a hot artifact on a peer cluster's edge node.
type RemoteArtifactInfo struct {
	PeerCluster  string
	NodeID       string
	BaseURL      string
	SizeBytes    uint64
	AccessCount  uint32
	LastAccessed int64
	GeoLat       float64
	GeoLon       float64
}

// PlaybackDependencies contains dependencies needed for playback resolution
type PlaybackDependencies struct {
	DB              *sql.DB
	LB              *balancer.LoadBalancer
	GeoLat          float64
	GeoLon          float64
	RemoteEdges     []balancer.RemoteEdgeCandidate // optional: pre-collected remote edge candidates from federation
	FedClient       ArtifactFederationClient       // optional: for cross-cluster artifact resolution
	PeerResolver    PeerAddressResolver            // optional: resolves peer cluster addresses
	LocalClusterID  string                         // this cluster's ID
	RemoteArtifacts RemoteArtifactLookup           // optional: hot artifact locations from peering
}

// ResolveArtifactPlayback resolves playback endpoints for any artifact (clip/dvr/vod) using playback ID
func ResolveArtifactPlayback(ctx context.Context, deps *PlaybackDependencies, playbackID string) (*pb.ViewerEndpointResponse, error) {
	if deps.DB == nil {
		return nil, fmt.Errorf("database not available")
	}
	if playbackID == "" {
		return nil, fmt.Errorf("playback_id is required")
	}
	if CommodoreClient == nil {
		return nil, fmt.Errorf("commodore client not available")
	}

	artifactResp, err := CommodoreClient.ResolveArtifactPlaybackID(ctx, playbackID)
	if err != nil || !artifactResp.Found || artifactResp.ArtifactHash == "" || artifactResp.ContentType == "" {
		return nil, fmt.Errorf("content not found")
	}
	if artifactResp.TenantId == "" {
		return nil, fmt.Errorf("tenant_id missing for artifact")
	}

	contentType := strings.ToLower(artifactResp.ContentType)
	artifactType := contentType
	tenantID := artifactResp.TenantId
	originClusterID := artifactResp.GetOriginClusterId()
	allowedClusters := artifactResp.GetClusterPeers()

	// Query foghorn.artifacts for lifecycle state
	var internalName string
	var status string
	var durationSeconds sql.NullInt64
	var sizeBytes sql.NullInt64
	var createdAt sql.NullTime
	var format sql.NullString
	var storageLocation sql.NullString
	var syncStatus sql.NullString

	err = deps.DB.QueryRowContext(ctx, `
		SELECT COALESCE(internal_name, ''),
		       status,
		       duration_seconds,
		       size_bytes,
		       created_at,
		       format,
		       COALESCE(storage_location, ''),
		       COALESCE(sync_status, '')
		FROM foghorn.artifacts
		WHERE artifact_hash = $1 AND artifact_type = $2 AND status != 'deleted' AND tenant_id = $3
	`, artifactResp.ArtifactHash, artifactType, tenantID).Scan(&internalName, &status, &durationSeconds, &sizeBytes, &createdAt, &format, &storageLocation, &syncStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if originClusterID != "" && originClusterID != deps.LocalClusterID && deps.FedClient != nil {
				return resolveRemoteArtifact(ctx, deps, artifactResp.ArtifactHash, originClusterID, contentType, tenantID, allowedClusters)
			}
			return nil, fmt.Errorf("%s not found", contentType)
		}
		return nil, fmt.Errorf("failed to query artifact: %w", err)
	}

	var artifactNodes []state.ArtifactNodeInfo
	if manager := state.DefaultManager(); manager != nil {
		artifactNodes = manager.FindNodesByArtifactHash(artifactResp.ArtifactHash)
	}
	if len(artifactNodes) == 0 {
		// Check peer clusters for hot copies before falling through to S3/defrost
		if deps.RemoteArtifacts != nil {
			remoteHits, _ := deps.RemoteArtifacts.GetRemoteArtifacts(ctx, artifactResp.ArtifactHash)
			if len(remoteHits) > 0 {
				best := pickBestRemoteArtifact(remoteHits, deps.GeoLat, deps.GeoLon)
				if best != nil {
					return &pb.ViewerEndpointResponse{
						Primary: &pb.ViewerEndpoint{
							NodeId:      best.NodeID,
							BaseUrl:     best.BaseURL,
							Protocol:    "https",
							GeoDistance: CalculateGeoDistance(deps.GeoLat, deps.GeoLon, best.GeoLat, best.GeoLon),
							ClusterId:   best.PeerCluster,
						},
					}, nil
				}
			}
		}

		// No cached nodes - trigger defrost if asset is in S3
		location := strings.ToLower(strings.TrimSpace(storageLocation.String))
		sync := strings.ToLower(strings.TrimSpace(syncStatus.String))
		if location == "defrosting" {
			return nil, NewDefrostingError(10, "defrost in progress")
		}
		if sync == "synced" || location == "s3" {
			// Pick a storage node for defrost
			nodeID, err := pickStorageNodeID()
			if err != nil {
				return nil, fmt.Errorf("storage node unknown: %w", err)
			}
			if _, err := StartDefrost(ctx, contentType, artifactResp.ArtifactHash, nodeID, 30*time.Second, controlLogger()); err != nil {
				// If defrost already in progress, return retry
				var defrostErr *DefrostingError
				if errors.As(err, &defrostErr) {
					return nil, defrostErr
				}
				return nil, fmt.Errorf("failed to start defrost: %w", err)
			}
			return nil, NewDefrostingError(10, "defrost started")
		}
		// Federation fallback: artifact exists locally but not on any node and not in S3
		if originClusterID != "" && originClusterID != deps.LocalClusterID && deps.FedClient != nil {
			return resolveRemoteArtifact(ctx, deps, artifactResp.ArtifactHash, originClusterID, contentType, tenantID, allowedClusters)
		}
		return nil, fmt.Errorf("storage node unknown: no node assignment found")
	}

	streamID := artifactResp.StreamId
	title := ""
	description := ""
	clipDurationMs := int64(0)
	resolvedPlaybackID := playbackID

	switch contentType {
	case "clip":
		if resp, err := CommodoreClient.ResolveClipHash(ctx, artifactResp.ArtifactHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantID = resp.TenantId
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
			if resp.InternalName != "" {
				internalName = resp.InternalName
			}
			title = resp.Title
			description = resp.Description
			clipDurationMs = resp.Duration
			if resp.PlaybackId != "" {
				resolvedPlaybackID = resp.PlaybackId
			}
		}
	case "dvr":
		if resp, err := CommodoreClient.ResolveDVRHash(ctx, artifactResp.ArtifactHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantID = resp.TenantId
			}
			if resp.StreamId != "" {
				streamID = resp.StreamId
			}
			if resp.PlaybackId != "" {
				resolvedPlaybackID = resp.PlaybackId
			}
		}
	case "vod":
		if resp, err := CommodoreClient.ResolveVodHash(ctx, artifactResp.ArtifactHash); err == nil && resp.Found {
			if resp.TenantId != "" {
				tenantID = resp.TenantId
			}
			title = resp.Title
			description = resp.Description
			if resp.PlaybackId != "" {
				resolvedPlaybackID = resp.PlaybackId
			}
		}
	}

	rankedNodes := rankArtifactNodes(artifactNodes, deps.GeoLat, deps.GeoLon, 5)
	if len(rankedNodes) == 0 {
		return nil, fmt.Errorf("storage node outputs not available")
	}

	var endpoints []*pb.ViewerEndpoint
	for _, node := range rankedNodes {
		nodeOutputs, exists := GetNodeOutputs(node.NodeID)
		if !exists || nodeOutputs.Outputs == nil {
			continue
		}

		// Build URLs using playback ID (MistServer resolves via PLAY_REWRITE trigger)
		var protocol, endpointURL string
		formatValue := ""
		if format.Valid {
			formatValue = format.String
		}
		protocol, endpointURL = selectPrimaryArtifactOutput(nodeOutputs.Outputs, nodeOutputs.BaseURL, resolvedPlaybackID, formatValue)
		if endpointURL == "" {
			endpointURL = EnsureTrailingSlash(nodeOutputs.BaseURL) + resolvedPlaybackID + ".html"
			protocol = "html"
		}

		geoDistance := 0.0
		if geo.IsValidLatLon(deps.GeoLat, deps.GeoLon) && geo.IsValidLatLon(node.GeoLatitude, node.GeoLongitude) {
			geoDistance = CalculateGeoDistance(deps.GeoLat, deps.GeoLon, node.GeoLatitude, node.GeoLongitude)
		}

		endpoints = append(endpoints, &pb.ViewerEndpoint{
			NodeId:      node.NodeID,
			BaseUrl:     nodeOutputs.BaseURL,
			Protocol:    protocol,
			Url:         endpointURL,
			GeoDistance: geoDistance,
			LoadScore:   float64(node.Score),
			Outputs:     BuildOutputsMap(nodeOutputs.BaseURL, nodeOutputs.Outputs, resolvedPlaybackID, false),
		})
	}

	if len(endpoints) == 0 {
		return nil, fmt.Errorf("storage node outputs not available")
	}

	metadata := &pb.PlaybackMetadata{
		Status:      status,
		IsLive:      contentType == "dvr" && status == "recording",
		TenantId:    tenantID,
		ContentId:   resolvedPlaybackID,
		ContentType: contentType,
	}
	if streamID != "" {
		metadata.StreamId = &streamID
	}
	if contentType == "dvr" {
		metadata.DvrStatus = status
	}
	if contentType == "clip" && internalName != "" {
		metadata.ClipSource = &internalName
	}
	if title != "" {
		metadata.Title = &title
	}
	if description != "" {
		metadata.Description = &description
	}
	if contentType == "clip" && clipDurationMs > 0 {
		d := int32(clipDurationMs / 1000)
		metadata.DurationSeconds = &d
	} else if durationSeconds.Valid {
		d := int32(durationSeconds.Int64)
		metadata.DurationSeconds = &d
	}
	if sizeBytes.Valid {
		metadata.RecordingSizeBytes = &sizeBytes.Int64
	}
	if createdAt.Valid {
		metadata.CreatedAt = timestamppb.New(createdAt.Time)
	}
	if format.Valid && format.String != "" {
		metadata.Format = &format.String
	}

	return &pb.ViewerEndpointResponse{
		Primary:   endpoints[0],
		Fallbacks: endpoints[1:],
		Metadata:  metadata,
	}, nil
}

func rankArtifactNodes(nodes []state.ArtifactNodeInfo, viewerLat, viewerLon float64, maxNodes int) []state.ArtifactNodeInfo {
	if len(nodes) == 0 {
		return nil
	}
	ranked := append([]state.ArtifactNodeInfo(nil), nodes...)
	useGeo := viewerLat != 0 || viewerLon != 0
	sort.Slice(ranked, func(i, j int) bool {
		if useGeo {
			di := artifactGeoDistance(viewerLat, viewerLon, ranked[i])
			dj := artifactGeoDistance(viewerLat, viewerLon, ranked[j])
			if di != dj {
				return di < dj
			}
		}
		if ranked[i].Score != ranked[j].Score {
			return ranked[i].Score < ranked[j].Score
		}
		return ranked[i].NodeID < ranked[j].NodeID
	})
	if maxNodes > 0 && len(ranked) > maxNodes {
		return ranked[:maxNodes]
	}
	return ranked
}

func artifactGeoDistance(viewerLat, viewerLon float64, node state.ArtifactNodeInfo) float64 {
	if node.GeoLatitude == 0 && node.GeoLongitude == 0 {
		return math.MaxFloat64
	}
	return CalculateGeoDistance(viewerLat, viewerLon, node.GeoLatitude, node.GeoLongitude)
}

func selectPrimaryArtifactOutput(outputs map[string]interface{}, baseURL, playbackID, format string) (string, string) {
	if outputs == nil {
		return "", ""
	}
	for _, key := range preferredArtifactOutputKeys(format) {
		raw, ok := outputs[key]
		if !ok {
			continue
		}
		if u := ResolveTemplateURL(raw, baseURL, playbackID); u != "" {
			return strings.ToLower(key), u
		}
	}
	return "", ""
}

func preferredArtifactOutputKeys(format string) []string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "m3u8":
		return []string{"HLS", "DASH", "CMAF", "HDS"}
	case "mp4":
		return []string{"HTTP", "MP4", "HLS", "DASH", "CMAF"}
	case "webm":
		return []string{"HTTP", "WEBM", "HLS", "DASH", "CMAF"}
	default:
		return []string{"HTTP", "HLS", "DASH", "CMAF"}
	}
}

// ResolveLivePlayback resolves playback endpoints for a live stream using load balancing
func ResolveLivePlayback(ctx context.Context, deps *PlaybackDependencies, viewKey string, internalName string, streamID string, tenantID string) (*pb.ViewerEndpointResponse, error) {
	if deps.LB == nil {
		return nil, fmt.Errorf("load balancer not available")
	}

	// Unified state tracks live streams by their bare internal_name (e.g. "demo_live_stream_001"),
	// while MistServer uses wildcard names (e.g. "live+demo_live_stream_001").
	// Normalize here so load balancing doesn't incorrectly filter out healthy nodes.
	internalName = strings.TrimPrefix(strings.TrimSpace(internalName), "live+")

	// Use load balancer with internal name to find nodes that have the stream
	lbctx := context.WithValue(ctx, ctxkeys.KeyCapability, "edge")
	if tenantID != "" {
		lbctx = context.WithValue(lbctx, ctxkeys.KeyClusterScope, tenantID)
	}
	nodes, err := deps.LB.GetTopNodesWithScores(lbctx, internalName, deps.GeoLat, deps.GeoLon, make(map[string]int), "", 5, false)
	if err != nil && len(deps.RemoteEdges) == 0 {
		return nil, fmt.Errorf("no suitable edge nodes available: %w", err)
	}

	// Score remote edges and merge with local results
	if len(deps.RemoteEdges) > 0 {
		remoteNodes := deps.LB.ScoreRemoteEdges(deps.RemoteEdges, deps.GeoLat, deps.GeoLon)
		nodes = append(nodes, remoteNodes...)
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].Score > nodes[j].Score })
		if len(nodes) > 5 {
			nodes = nodes[:5]
		}
	}

	var endpoints []*pb.ViewerEndpoint

	for _, node := range nodes {
		// Remote edges: produce a redirect endpoint to the peer cluster's play domain
		if node.ClusterID != "" {
			geoDistance := 0.0
			if geo.IsValidLatLon(deps.GeoLat, deps.GeoLon) && geo.IsValidLatLon(node.GeoLatitude, node.GeoLongitude) {
				geoDistance = CalculateGeoDistance(deps.GeoLat, deps.GeoLon, node.GeoLatitude, node.GeoLongitude)
			}
			endpoints = append(endpoints, &pb.ViewerEndpoint{
				NodeId:      node.NodeID,
				BaseUrl:     node.Host,
				Protocol:    "redirect",
				Url:         "https://" + node.Host + "/play/" + viewKey,
				GeoDistance: geoDistance,
				LoadScore:   float64(node.Score),
				ClusterId:   node.ClusterID,
			})
			continue
		}

		nodeOutputs, exists := GetNodeOutputs(node.NodeID)
		if !exists || nodeOutputs.Outputs == nil {
			continue
		}

		// Build URLs with view key (MistServer resolves via PLAY_REWRITE trigger)
		// With correct pubaddr/pubhost, MistServer fills HTTP-based outputs with full URLs.
		// Only direct protocols (RTMP, RTSP, SRT, DTSC) keep HOST placeholder.
		var protocol, endpointURL string

		// Extract public host from HTTP outputs for HOST replacement in direct protocols
		publicHost := ExtractPublicHostFromOutputs(nodeOutputs.Outputs)

		if webrtcURL, ok := nodeOutputs.Outputs["WebRTC"]; ok {
			protocol = "webrtc"
			endpointURL = ResolveTemplateURL(webrtcURL, nodeOutputs.BaseURL, viewKey)
			// If HOST wasn't replaced (direct protocol), use extracted public host
			if strings.Contains(endpointURL, "HOST") && publicHost != "" {
				endpointURL = strings.ReplaceAll(endpointURL, "HOST", publicHost)
			}
		} else if hlsURL, ok := nodeOutputs.Outputs["HLS"]; ok {
			protocol = "hls"
			endpointURL = ResolveTemplateURL(hlsURL, nodeOutputs.BaseURL, viewKey)
		}

		if endpointURL == "" {
			continue
		}

		// Calculate geo distance
		geoDistance := 0.0
		if geo.IsValidLatLon(deps.GeoLat, deps.GeoLon) && geo.IsValidLatLon(node.GeoLatitude, node.GeoLongitude) {
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
		return nil, fmt.Errorf("no eligible edge nodes have HLS/WebRTC outputs configured for stream %q", internalName)
	}

	// Build metadata from stream state
	metadata := &pb.PlaybackMetadata{
		Status:      "live",
		IsLive:      true,
		TenantId:    tenantID,
		ContentId:   viewKey,
		ContentType: "live",
	}
	if streamID != "" {
		metadata.StreamId = &streamID
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

// ExtractPublicHostFromOutputs extracts the public hostname:port from MistServer outputs.
// MistServer outputs like HLS contain the actual public-facing host (e.g., "localhost:18090")
// while WebRTC uses "HOST" placeholder. This function extracts the public host from outputs
// that already contain it, so we can use it for HOST replacement.
func ExtractPublicHostFromOutputs(outputs map[string]interface{}) string {
	// Try to extract from HLS, HTTP, or other outputs that typically have full URLs
	for _, key := range []string{"HLS", "HTTP", "CMAF", "HDS"} {
		raw, ok := outputs[key]
		if !ok {
			continue
		}
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
		}
		if s == "" {
			continue
		}
		// Parse URL patterns like "//["localhost:18090]/view/..." or "//localhost:18090/..."
		s = strings.Trim(s, "[]\"")
		// Handle protocol-relative URLs
		if strings.HasPrefix(s, "//") {
			s = "http:" + s
		}
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			return u.Host
		}
	}
	return ""
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
	s = strings.ReplaceAll(s, "$", streamName)
	if strings.Contains(s, "HOST") {
		host := baseURL
		host = strings.TrimPrefix(host, "https://")
		host = strings.TrimPrefix(host, "http://")
		host = strings.TrimSuffix(host, "/")
		if host == "" {
			return ""
		}
		s = strings.ReplaceAll(s, "HOST", host)
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

	// Extract public host from HTTP outputs for HOST replacement in direct protocols
	publicHost := ExtractPublicHostFromOutputs(rawOutputs)

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

	// Direct protocols (bypass nginx, need HOST replacement with public host)
	directProtocols := []string{"RTMP", "RTSP", "SRT", "DTSC"}
	for _, proto := range directProtocols {
		if raw, ok := rawOutputs[proto]; ok {
			if u := ResolveTemplateURL(raw, base, streamName); u != "" {
				// Replace HOST with public host extracted from HTTP outputs
				if strings.Contains(u, "HOST") && publicHost != "" {
					// For direct protocols, just use the hostname (no port from publicHost)
					// since they have their own ports in the URL
					hostOnly := strings.Split(publicHost, ":")[0]
					u = strings.ReplaceAll(u, "HOST", hostOnly)
				}
				outputs[proto] = &pb.OutputEndpoint{Protocol: proto, Url: u, Capabilities: BuildOutputCapabilities(proto, isLive)}
			}
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

// pickBestRemoteArtifact selects the best remote artifact location by geo distance
// to the viewer. Applies a CrossClusterPenalty in the scoring so local edges
// (if they existed) would have been preferred — this function is only called
// when there are zero local nodes with the artifact.
func pickBestRemoteArtifact(hits []*RemoteArtifactInfo, viewerLat, viewerLon float64) *RemoteArtifactInfo {
	if len(hits) == 0 {
		return nil
	}
	var best *RemoteArtifactInfo
	bestDist := math.MaxFloat64
	for _, h := range hits {
		dist := CalculateGeoDistance(viewerLat, viewerLon, h.GeoLat, h.GeoLon)
		if dist < bestDist {
			bestDist = dist
			best = h
		}
	}
	return best
}

func isAuthorizedPeerCluster(clusterID string, peers []*pb.TenantClusterPeer) bool {
	if clusterID == "" {
		return false
	}
	if len(peers) == 0 {
		return false
	}
	for _, peer := range peers {
		if peer.GetClusterId() == clusterID {
			return true
		}
	}
	return false
}

// resolveRemoteArtifact handles cross-cluster artifact resolution by calling
// PrepareArtifact on the origin cluster's Foghorn. If the artifact is ready,
// it creates a local adoption record and triggers defrost from the presigned URLs.
func resolveRemoteArtifact(ctx context.Context, deps *PlaybackDependencies, artifactHash, originClusterID, contentType, tenantID string, clusterPeers []*pb.TenantClusterPeer) (*pb.ViewerEndpointResponse, error) {
	if deps.PeerResolver == nil {
		return nil, fmt.Errorf("peer resolver not available for cross-cluster artifact")
	}
	if !isAuthorizedPeerCluster(originClusterID, clusterPeers) {
		return nil, fmt.Errorf("origin cluster %s is not authorized for tenant", originClusterID)
	}
	addr := deps.PeerResolver.GetPeerAddr(originClusterID)
	if addr == "" {
		return nil, fmt.Errorf("origin cluster %s address unknown", originClusterID)
	}

	resp, err := deps.FedClient.PrepareArtifact(ctx, originClusterID, addr, &pb.PrepareArtifactRequest{
		ArtifactId:        artifactHash,
		RequestingCluster: deps.LocalClusterID,
		ArtifactType:      contentType,
		TenantId:          tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to prepare artifact from origin cluster: %w", err)
	}
	if resp.GetError() != "" {
		return nil, fmt.Errorf("origin cluster error: %s", resp.GetError())
	}
	if !resp.GetReady() {
		est := resp.GetEstReadySeconds()
		if est == 0 {
			est = 15
		}
		return nil, NewDefrostingError(int(est), "remote artifact being prepared")
	}

	// Adopt the artifact locally (INSERT ON CONFLICT DO NOTHING)
	if deps.DB != nil {
		_, _ = deps.DB.ExecContext(ctx, `
			INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, internal_name, format, status, storage_location, sync_status, origin_cluster_id)
			VALUES ($1, $2, $3, $4, $5, 'active', 's3', 'synced', $6)
			ON CONFLICT (artifact_hash, artifact_type, tenant_id) DO UPDATE
			SET storage_location = 's3',
			    sync_status = 'synced',
			    internal_name = CASE WHEN foghorn.artifacts.internal_name = '' AND EXCLUDED.internal_name <> '' THEN EXCLUDED.internal_name ELSE foghorn.artifacts.internal_name END,
			    format = CASE WHEN COALESCE(foghorn.artifacts.format, '') = '' AND EXCLUDED.format <> '' THEN EXCLUDED.format ELSE foghorn.artifacts.format END,
			    origin_cluster_id = CASE WHEN COALESCE(foghorn.artifacts.origin_cluster_id, '') = '' THEN EXCLUDED.origin_cluster_id ELSE foghorn.artifacts.origin_cluster_id END
		`, artifactHash, contentType, tenantID, resp.GetInternalName(), resp.GetFormat(), originClusterID)
	}

	// Trigger local defrost using the origin cluster's presigned URLs
	nodeID, err := pickStorageNodeID()
	if err != nil {
		return nil, fmt.Errorf("no local storage node for remote artifact defrost: %w", err)
	}
	if _, err := StartRemoteDefrost(ctx, contentType, artifactHash, nodeID, 30*time.Second, controlLogger(), resp.GetUrl(), resp.GetSegmentUrls()); err != nil {
		var defrostErr *DefrostingError
		if errors.As(err, &defrostErr) {
			return nil, defrostErr
		}
		return nil, fmt.Errorf("failed to start remote artifact defrost: %w", err)
	}
	return nil, NewDefrostingError(10, "remote artifact defrost started")
}
