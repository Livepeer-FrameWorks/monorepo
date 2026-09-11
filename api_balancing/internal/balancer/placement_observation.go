package balancer

import (
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

const placementObservationLifetime = 30 * time.Second

// PlacementClusterFacts must come from the consuming tenant's authority, not
// from node telemetry. A node cannot declare its own owner, entitlement or price.
type PlacementClusterFacts struct {
	OwnerTenantID       string
	Official            bool
	Region              string
	AllowedVerbs        []placement.Verb
	Charging            placement.Charging
	ChargingRevision    string
	ChargingUntil       time.Time
	Price               *placement.Price
	Prices              []placement.Price
	AuthorityUntil      time.Time
	ConsentRevision     uint64
	AllowExternalSource bool
}

// PlacementNodePath is node-specific, already-authorized source evidence.
// It must be recomputed for the selected destination during preparation.
type PlacementNodePath struct {
	Presence       placement.Presence
	SourceFeasible bool
}

type PlacementObservationRequest struct {
	TenantID     string
	Verb         placement.Verb
	InternalName string
	Protocol     string
	Now          time.Time
	Snapshot     *state.BalancerSnapshot
	Clusters     map[string]PlacementClusterFacts
	Paths        map[string]PlacementNodePath
}

// ObservePlacementNodes preserves unavailable and cold members of every entitled
// pool. It never geo-prunes, assigns a coordinator-relative score, or certifies
// pool completeness: discovery must separately account for unreachable members.
func ObservePlacementNodes(req PlacementObservationRequest) ([]placement.Candidate, error) {
	return observePlacementNodes(req, false)
}

// ObservePlacementCapacity reads listener and directional load evidence without
// evaluating a content source. An omitted stream cannot satisfy node allowlists.
// Source fields remain unknown; the result is for EvaluateCapacity, not admission.
func ObservePlacementCapacity(req PlacementObservationRequest) ([]placement.Candidate, error) {
	return observePlacementNodes(req, true)
}

func observePlacementNodes(req PlacementObservationRequest, capacityOnly bool) ([]placement.Candidate, error) {
	if req.TenantID == "" || req.Now.IsZero() || (!capacityOnly && req.InternalName == "") || req.Protocol == "" || (req.Verb != placement.Ingest && req.Verb != placement.Serve) || req.Snapshot == nil {
		return nil, fmt.Errorf("placement observation requires tenant, verb, protocol, stream, clock and snapshot")
	}
	if len(req.Snapshot.Nodes) > 4096 {
		return nil, fmt.Errorf("placement observation exceeds node bound")
	}
	result := make([]placement.Candidate, 0, len(req.Snapshot.Nodes))
	seen := make(map[string]bool, len(req.Snapshot.Nodes))
	for _, node := range req.Snapshot.Nodes {
		facts, entitled := req.Clusters[node.ClusterID]
		if !entitled {
			continue
		}
		if node.NodeID == "" || node.ClusterID == "" || seen[node.NodeID] {
			return nil, fmt.Errorf("placement observation contains ambiguous node identity")
		}
		seen[node.NodeID] = true
		path, pathKnown := req.Paths[node.NodeID]
		if capacityOnly {
			path, pathKnown = PlacementNodePath{}, true
		}
		candidate := placement.Candidate{
			TenantID: req.TenantID, NodeID: node.NodeID, ClusterID: node.ClusterID,
			OwnerTenantID: facts.OwnerTenantID, Official: facts.Official, Region: facts.Region,
			AllowedVerbs: slices.Clone(facts.AllowedVerbs), Charging: facts.Charging, ChargingUntil: facts.ChargingUntil,
			ChargingRevision: facts.ChargingRevision,
			Prices:           slices.Clone(facts.Prices),
			ObservedAt:       node.MetricsObservedAt, ExpiresAt: node.MetricsObservedAt.Add(placementObservationLifetime),
			CPUPercent: node.CPU, Presence: path.Presence, SourceFeasible: path.SourceFeasible,
			Capacity: placement.CapacityUnknown,
		}
		// Invalid telemetry remains unknown; non-finite values must not poison
		// serialization of other candidates in the same cell observation.
		if !finiteMetric(candidate.CPUPercent) || candidate.CPUPercent < 0 || candidate.CPUPercent > 100 {
			candidate.CPUPercent = 0
		}
		if facts.Price != nil {
			price := *facts.Price
			candidate.Price = &price
		}
		// Liveness and capacity have independent clocks; neither refreshes the other.
		if expiry := node.LastHeartbeat.Add(placementObservationLifetime); expiry.Before(candidate.ExpiresAt) {
			candidate.ExpiresAt = expiry
		}
		// A fresh metric or config update cannot extend a listener's lifetime.
		if expiry := node.OutputsObservedAt.Add(placementObservationLifetime); expiry.Before(candidate.ExpiresAt) {
			candidate.ExpiresAt = expiry
		}
		if node.OutputsObservedAt.After(req.Now) {
			candidate.ExpiresAt = req.Now
		}
		if !facts.AuthorityUntil.IsZero() && facts.AuthorityUntil.Before(candidate.ExpiresAt) {
			candidate.ExpiresAt = facts.AuthorityUntil
		}
		if node.HasCoordinates && finiteMetric(node.GeoLatitude) && finiteMetric(node.GeoLongitude) && math.Abs(node.GeoLatitude) <= 90 && math.Abs(node.GeoLongitude) <= 180 {
			candidate.Location = &placement.Coordinates{Latitude: node.GeoLatitude, Longitude: node.GeoLongitude}
		}
		if validPlacementMetrics(node) {
			candidate.BWLimit = uint64(node.BWLimit)
			candidate.RAMMax, candidate.RAMUsed = uint64(node.RAMMax), uint64(node.RAMCurrent)
			candidate.BWAvailable = placementHeadroom(node, req.Verb)
			candidate.Capacity = placement.CapacityAvailable
			if candidate.BWAvailable == 0 || node.CPU >= 100 || node.RAMCurrent >= node.RAMMax {
				candidate.Capacity = placement.CapacityExhausted
			}
		}
		mode := node.OperationalMode
		if mode == "" {
			mode = state.NodeModeNormal
		}
		address, addressErr := url.Parse(node.Host)
		validAddress := addressErr == nil && address.Hostname() != "" && address.User == nil && (address.Scheme == "http" || address.Scheme == "https")
		capable := (req.Verb == placement.Ingest && node.CapIngest) || (req.Verb == placement.Serve && node.CapEdge)
		protocolAvailable := mist.SupportsIngestProtocol(node.Outputs, node.Host, req.Protocol)
		if req.Verb == placement.Serve {
			name := req.InternalName
			if capacityOnly && name == "" {
				// Validate the reported URL template, not existence of this probe name.
				name = "placement-capacity-probe"
			}
			protocolAvailable = mist.ResolvePlaybackURL(node.Outputs, node.Host, req.Protocol, name) != ""
		}
		unknownStream := capacityOnly && req.InternalName == "" && len(node.ConfigStreams) != 0
		if !node.IsActive || mode != state.NodeModeNormal || !validAddress || !capable || !protocolAvailable || (!unknownStream && !placementStreamAllowed(node.ConfigStreams, req.InternalName)) {
			candidate.Capacity = placement.CapacityUnavailable
		} else if !pathKnown || unknownStream {
			// Missing source or stream-allowlist evidence is not a known node refusal.
			candidate.Capacity = placement.CapacityUnknown
		}
		result = append(result, candidate)
	}
	return result, nil
}

func finiteMetric(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func validPlacementMetrics(node state.EnhancedBalancerNodeSnapshot) bool {
	for _, value := range []float64{node.BWLimit, node.UpSpeed, node.DownSpeed, node.RAMMax, node.RAMCurrent} {
		// Float64 cannot represent MaxUint64 itself; rejecting 2^64 also avoids
		// implementation-dependent conversions of invalid telemetry to integers.
		if !finiteMetric(value) || value < 0 || value >= math.Exp2(64) {
			return false
		}
	}
	return node.BWLimit >= 1 && node.RAMMax >= 1 && node.RAMCurrent <= node.RAMMax && finiteMetric(node.CPU) && node.CPU >= 0 && node.CPU <= 100
}

func placementHeadroom(node state.EnhancedBalancerNodeSnapshot, verb placement.Verb) uint64 {
	used := node.UpSpeed
	if verb == placement.Ingest {
		used = node.DownSpeed
	}
	if used >= node.BWLimit {
		return 0
	}
	remaining := uint64(node.BWLimit - used)
	if verb == placement.Serve {
		if node.AddBandwidth >= remaining {
			return 0
		}
		remaining -= node.AddBandwidth
	}
	return remaining
}

func placementStreamAllowed(configured []string, stream string) bool {
	if len(configured) == 0 {
		return true
	}
	for _, entry := range configured {
		if entry != "" && (entry == stream || strings.HasPrefix(stream, entry+"+") || strings.HasPrefix(stream, entry+" ")) {
			return true
		}
	}
	return false
}
