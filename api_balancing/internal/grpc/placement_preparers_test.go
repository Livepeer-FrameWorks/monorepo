package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
)

// placementEvidenceWindow is the freshness bound the preparation runtimes hold
// telemetry to: a heartbeat or listener report older than this is no longer
// evidence that the node can take traffic right now.
const placementEvidenceWindow = 30 * time.Second

func freshPlacementFixtureEvidence(observed, now time.Time) bool {
	return !observed.IsZero() && !now.Before(observed) && now.Before(observed.Add(placementEvidenceWindow))
}

// sortedFixtureNodes orders the balancer snapshot by node id so a fixture with
// several capable nodes selects deterministically.
func sortedFixtureNodes() []state.EnhancedBalancerNodeSnapshot {
	snapshot := state.DefaultManager().GetBalancerSnapshotAtomic()
	if snapshot == nil {
		return nil
	}
	nodes := append([]state.EnhancedBalancerNodeSnapshot(nil), snapshot.Nodes...)
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	return nodes
}

// prepareIngestFromNodeState stands in for the policy resolver: selection lives
// behind the placement preparer now, so these tests express "which node may take
// this publish" as the same listener evidence the real preparation runtime reads
// — capability, liveness, and a fresh advertised listener for the exact
// protocol. A protocol no node confirms is unavailable rather than substituted.
func prepareIngestFromNodeState(_ context.Context, request control.IngestPlacementRequest) (balancer.PlacementPreparationResult, error) {
	now := time.Now()
	for _, node := range sortedFixtureNodes() {
		if !node.IsActive || !node.CapIngest || node.ClusterID == "" ||
			!freshPlacementFixtureEvidence(node.LastHeartbeat, now) || !freshPlacementFixtureEvidence(node.OutputsObservedAt, now) {
			continue
		}
		endpoint := mist.ResolveIngestEndpointTemplate(node.Outputs, node.Host, request.Protocol)
		publicBase := mist.IngestPublicOrigin(node.Host)
		if endpoint == "" || publicBase == "" {
			continue
		}
		attempt, err := placement.NewPreparationAttemptID(now)
		if err != nil {
			return balancer.PlacementPreparationResult{}, err
		}
		return balancer.PlacementPreparationResult{
			Outcome: balancer.PlacementAccepted, TenantID: request.TenantID,
			ObjectID: sharedauthority.LiveStreamAuthorityID(request.StreamID),
			NodeID:   node.NodeID, ClusterID: node.ClusterID, Protocol: request.Protocol,
			PublicBaseURL: publicBase, Endpoint: endpoint, AttemptID: attempt,
			ExpiresAt: now.Add(10 * time.Second),
		}, nil
	}
	return balancer.PlacementPreparationResult{}, balancer.ErrPlacementUnavailable
}

// prepareViewerFromNodeState stands in for the serve-policy preparer: it answers
// "which edge may serve this viewer" from the evidence the real preparation
// runtime reads — an active, edge-capable node in a known cluster with fresh
// heartbeat and listener telemetry, a live input observed for the stream's
// routing name, and a listener that actually advertises the requested protocol.
// The source generation is bound to that observed input, so a fixture with no
// live source cannot produce a signed destination.
func prepareViewerFromNodeState(_ context.Context, request control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
	routingName := mist.ExtractInternalName(request.InternalName)
	now := time.Now()
	for _, node := range sortedFixtureNodes() {
		if !node.IsActive || !node.CapEdge || node.ClusterID == "" ||
			!freshPlacementFixtureEvidence(node.LastHeartbeat, now) || !freshPlacementFixtureEvidence(node.OutputsObservedAt, now) {
			continue
		}
		stream, present := node.Streams[routingName]
		if !present || stream.Inputs == 0 {
			continue
		}
		endpoint := mist.ResolvePlaybackURL(node.Outputs, node.Host, request.Protocol, request.PlaybackID)
		if endpoint == "" {
			continue
		}
		attempt, err := placement.NewPreparationAttemptID(now)
		if err != nil {
			return balancer.PlacementPreparationResult{}, err
		}
		outputsJSON, err := json.Marshal(node.Outputs)
		if err != nil {
			return balancer.PlacementPreparationResult{}, err
		}
		return balancer.PlacementPreparationResult{
			Outcome: balancer.PlacementAccepted, TenantID: request.TenantID,
			ObjectID:         sharedauthority.LiveStreamAuthorityID(request.StreamID),
			SourceGeneration: fmt.Sprintf("%s:%d", node.NodeID, stream.ObservedAt.UnixNano()),
			NodeID:           node.NodeID, ClusterID: node.ClusterID, Protocol: request.Protocol,
			PublicBaseURL: node.Host, Endpoint: endpoint, OutputsJSON: string(outputsJSON), AttemptID: attempt,
			ExpiresAt: now.Add(10 * time.Second),
		}, nil
	}
	return balancer.PlacementPreparationResult{}, balancer.ErrPlacementUnavailable
}
