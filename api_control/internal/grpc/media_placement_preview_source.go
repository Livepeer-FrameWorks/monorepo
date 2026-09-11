package grpc

import (
	"context"
	"slices"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

type mediaPlacementPushSourceRead func(context.Context, string, *placementpb.PushSourcePreviewQuery) (*placementpb.PushSourcePreviewObservation, error)

func (s *CommodoreServer) collectPreviewPushSource(ctx context.Context, preview *mediaPlacementPreviewContext, inventory *mediaPreviewInventory) *placementpb.PushSourcePreviewObservation {
	if preview.verb != placement.Serve || preview.internalName == "" || preview.sourceCluster == "" {
		return nil
	}
	peer := inventory.peers[preview.sourceCluster]
	if peer == nil || !peer.GetMediaConsent().GetAllowIngest() {
		return nil
	}
	query := &placementpb.PushSourcePreviewQuery{TenantId: preview.snapshot.Scope.TenantID, ControlCellId: peer.GetControlCellId(), ClusterId: preview.sourceCluster, InternalName: preview.internalName}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var response *placementpb.PushSourcePreviewObservation
	var err error
	if s.placementPushSource != nil {
		response, err = s.placementPushSource(ctx, preview.sourceCluster, query)
	} else {
		if s.foghornPool == nil || s.quartermasterClient == nil {
			return nil
		}
		client, routeErr := s.resolveFoghornForCluster(ctx, preview.sourceCluster, query.TenantId)
		if routeErr != nil {
			return nil
		}
		response, err = client.ObserveMediaPlacementPushSource(ctx, query)
	}
	if err != nil || ctx.Err() != nil || placement.ValidatePushSourcePreviewObservation(query, response, time.Now().UTC()) != nil || !proto.Equal(response.GetConsent(), peer.GetMediaConsent()) {
		return nil
	}
	return response
}

type mediaPreviewEvaluation struct {
	placement.CapacityDecision
	sourceEvaluated bool
	requiresPull    map[string]bool
}

func evaluateMediaPreview(request placement.Request, needsSource bool, source *placementpb.PushSourcePreviewObservation, inventory *mediaPreviewInventory) (mediaPreviewEvaluation, error) {
	if !needsSource {
		decision, err := placement.EvaluateCapacity(request)
		return mediaPreviewEvaluation{CapacityDecision: decision}, err
	}
	if source == nil {
		request.Complete = false
	}
	request.Candidates = slices.Clone(request.Candidates)
	for index := range request.Candidates {
		candidate := &request.Candidates[index]
		candidate.Presence, candidate.SourceFeasible = placement.Absent, false
		if source == nil {
			continue
		}
		if candidate.ClusterID == source.GetScope().GetClusterId() && candidate.NodeID == source.GetNodeId() {
			candidate.Presence = placement.Present
		} else if source.GetPullAvailable() && (candidate.ClusterID == source.GetScope().GetClusterId() || inventory.peers[candidate.ClusterID].GetMediaConsent().GetAllowExternalSource()) {
			candidate.SourceFeasible = true
		}
	}
	decision, err := placement.Evaluate(request)
	out := mediaPreviewEvaluation{CapacityDecision: placement.CapacityDecision{Reason: decision.Reason, Complete: decision.Complete, Assessments: decision.Assessments, Transitions: decision.Transitions}, sourceEvaluated: source != nil, requiresPull: map[string]bool{}}
	for _, choice := range decision.Choices {
		out.Choices = append(out.Choices, placement.CapacityChoice{ClusterID: choice.ClusterID, NodeID: choice.NodeID, GroupID: choice.GroupID, DistanceKM: choice.DistanceKM})
		out.requiresPull[choice.NodeID+"\x00"+choice.GroupID] = choice.RequiresPull
	}
	return out, err
}
