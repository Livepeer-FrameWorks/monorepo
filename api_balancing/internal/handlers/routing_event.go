package handlers

import (
	"math"

	"frameworks/api_balancing/internal/geo"
	pb "frameworks/pkg/proto"
)

// RoutingEvent captures all context for a viewer routing decision.
// Callers fill the fields they have; BuildLoadBalancingData handles
// bucketing, distance, and optional-field nil-wrapping.
type RoutingEvent struct {
	// Decision outcome
	Status  string // "success", "failed", "redirect", "remote_redirect", "cross_cluster_dtsc"
	Details string // redirect URL, error message, DTSC URL, etc.
	Score   uint64

	// Stream identity (pre-resolved by caller, or resolved via enrichClient)
	StreamName     string
	InternalName   string
	StreamID       string // public UUID
	StreamTenantID string // stream owner tenant
	TenantID       string // infra owner (cluster operator) — overrides package-level ownerTenantID if set

	// Client location (raw, pre-bucketing)
	ClientIP      string
	ClientCountry string
	ClientLat     float64
	ClientLon     float64

	// Selected node
	SelectedNode   string
	SelectedNodeID string
	NodeLat        float64
	NodeLon        float64
	NodeName       string

	// Timing and scoring
	LatencyMs       float32
	CandidatesCount int32

	// Event classification
	EventType string // "load_balancing", "play_rewrite", "grpc_resolve"
	Source    string // "http", "grpc"

	// Federation: set when the decision routes to a remote cluster
	RemoteClusterID string
}

// BuildLoadBalancingData converts a RoutingEvent into a proto-ready
// LoadBalancingData with geo-bucketed coordinates, haversine distance,
// and dual-tenant attribution.
func BuildLoadBalancingData(e *RoutingEvent) *pb.LoadBalancingData {
	// Geo-bucket coordinates for privacy
	clientBucket, clientCentLat, clientCentLon, hasClient := geo.Bucket(e.ClientLat, e.ClientLon)
	nodeBucket, nodeCentLat, nodeCentLon, hasNode := geo.Bucket(e.NodeLat, e.NodeLon)

	// Haversine distance between client and node
	var routingDistanceKm float64
	if geo.IsValidLatLon(e.ClientLat, e.ClientLon) && geo.IsValidLatLon(e.NodeLat, e.NodeLon) {
		const toRad = math.Pi / 180.0
		lat1, lon1 := e.ClientLat*toRad, e.ClientLon*toRad
		lat2, lon2 := e.NodeLat*toRad, e.NodeLon*toRad
		val := math.Sin(lat1)*math.Sin(lat2) + math.Cos(lat1)*math.Cos(lat2)*math.Cos(lon1-lon2)
		if val > 1 {
			val = 1
		} else if val < -1 {
			val = -1
		}
		routingDistanceKm = 6371.0 * math.Acos(val)
	}

	// Resolve cluster identity from package-level bootstrap
	cID, oTenantID := GetClusterInfo()
	if e.TenantID != "" {
		oTenantID = e.TenantID
	}

	data := &pb.LoadBalancingData{
		SelectedNode:  e.SelectedNode,
		Status:        e.Status,
		Details:       e.Details,
		Score:         e.Score,
		ClientIp:      e.ClientIP,
		ClientCountry: e.ClientCountry,
		NodeName:      e.NodeName,
		ClientBucket:  clientBucket,
		NodeBucket:    nodeBucket,
	}

	// Bucketed centroid coordinates (privacy-preserving)
	if hasClient {
		data.Latitude = clientCentLat
		data.Longitude = clientCentLon
	}
	if hasNode {
		data.NodeLatitude = nodeCentLat
		data.NodeLongitude = nodeCentLon
	}

	// Optional fields — proto uses *T for optional
	data.SelectedNodeId = optStr(e.SelectedNodeID)
	data.InternalName = optStr(e.InternalName)
	data.StreamId = optStr(e.StreamID)
	data.TenantId = optStr(oTenantID)
	data.StreamTenantId = optStr(e.StreamTenantID)
	data.ClusterId = optStr(cID)
	data.EventType = optStr(e.EventType)
	data.Source = optStr(e.Source)
	data.RemoteClusterId = optStr(e.RemoteClusterID)

	if routingDistanceKm > 0 {
		data.RoutingDistanceKm = &routingDistanceKm
	}
	if e.LatencyMs > 0 {
		data.LatencyMs = &e.LatencyMs
	}
	if e.CandidatesCount > 0 {
		v := uint32(e.CandidatesCount)
		data.CandidatesCount = &v
	}

	return data
}

// optStr returns a *string for non-empty values, nil otherwise.
func optStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
