package demo

import (
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GenerateMediaPlacementPreview runs the shared evaluator against only synthetic
// observations. No URLs, preparation receipts or live authorization are produced.
func GenerateMediaPlacementPreview(req *placementpb.PreviewRequest, now time.Time) (*placementpb.Preview, error) {
	if err := placement.ValidatePreviewRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := ValidateMediaPlacementScope(req.Scope); err != nil {
		return nil, err
	}
	if req.GetExpectedRevision() != 0 || req.GetExpectedParentRevision() != 0 {
		return nil, status.Error(codes.Aborted, "demo policy revision changed")
	}
	verb := placement.Ingest
	if req.Verb == placementpb.Verb_VERB_SERVE {
		verb = placement.Serve
	}
	streamID := req.GetStreamId()
	if req.Scope.Kind == placementpb.ScopeKind_SCOPE_KIND_STREAM {
		streamID = req.Scope.StreamId
	}
	active := ""
	if streamID != "" {
		if err := ValidateMediaPlacementScope(&placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_STREAM, StreamId: streamID}); err != nil {
			return nil, err
		}
		for _, stream := range GenerateStreams() {
			if stream.StreamId == streamID && stream.IsLive {
				active = "cluster_demo_eu_west"
			}
		}
		if active == "" && verb == placement.Serve {
			return nil, status.Error(codes.FailedPrecondition, "demo stream has no live publisher")
		}
	}
	protocol := req.GetProtocol()
	if protocol != "" {
		supported := protocol == "rtmp" || protocol == "srt" || protocol == "whip"
		if verb == placement.Serve {
			supported = protocol == "hls" || protocol == "dash" || protocol == "webrtc"
		}
		if !supported {
			return nil, status.Error(codes.Unimplemented, "protocol has no demo observation")
		}
	}
	var own *placementpb.PolicySet
	var err error
	if req.DraftUpdate != nil {
		own, err = placement.ApplyUpdates(nil, []*placementpb.VerbUpdate{req.DraftUpdate})
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}
	tenant, stream := demoPlacementPolicySets(req.Scope, own)
	policy, err := placement.CompilePolicySets(tenant, stream, verb)
	if err != nil {
		return nil, err
	}
	digest, err := placement.PolicySetsDigest(tenant, stream)
	if err != nil {
		return nil, err
	}
	until := now.Add(20 * time.Second)
	request := placement.Request{TenantID: DemoTenantID, Verb: verb, Now: now, Policy: policy, Complete: true}
	if req.Coordinates != nil {
		request.Location = &placement.Coordinates{Latitude: req.Coordinates.Latitude, Longitude: req.Coordinates.Longitude}
	}
	if verb == placement.Ingest {
		request.ActiveIngestClusterID = active
	}
	clusters := map[string]demoPlacementCluster{}
	byNode := map[string]placement.Candidate{}
	for _, cluster := range demoPlacementClusters() {
		charging := placement.Rated
		if cluster.owner == DemoTenantID {
			charging = placement.PermanentlyFree
		}
		candidate := placement.Candidate{TenantID: DemoTenantID, ClusterID: cluster.id, NodeID: cluster.id + "-demo-node", OwnerTenantID: cluster.owner,
			Official: cluster.class == placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL, Region: cluster.region,
			AllowedVerbs: []placement.Verb{placement.Ingest, placement.Serve}, Charging: charging, ChargingUntil: until, ChargingRevision: "demo-simulated",
			Location: &placement.Coordinates{Latitude: cluster.lat, Longitude: cluster.lon}, ObservedAt: now, ExpiresAt: until,
			Capacity: placement.CapacityAvailable, BWAvailable: 800, BWLimit: 1000, CPUPercent: 10, RAMUsed: 100, RAMMax: 1000,
			Presence: placement.Absent, SourceFeasible: active != "",
			Prices: []placement.Price{{AmountMicros: cluster.price, Currency: "EUR", Unit: string(verb) + ":minutes=1;gib=1", Revision: "demo-simulated", ExpiresAt: until}}}
		if cluster.id == active {
			candidate.Presence = placement.Present
		}
		request.Candidates = append(request.Candidates, candidate)
		clusters[cluster.id], byNode[candidate.NodeID] = cluster, candidate
	}
	sourceEvaluated := verb == placement.Serve && streamID != ""
	var decision placement.Decision
	if sourceEvaluated {
		decision, err = placement.Evaluate(request)
	} else {
		capacity, capacityErr := placement.EvaluateCapacity(request)
		err = capacityErr
		decision = placement.Decision{Reason: capacity.Reason, Assessments: capacity.Assessments, Transitions: capacity.Transitions, Complete: capacity.Complete}
		for _, choice := range capacity.Choices {
			decision.Choices = append(decision.Choices, placement.Choice{ClusterID: choice.ClusterID, NodeID: choice.NodeID, GroupID: choice.GroupID, DistanceKM: choice.DistanceKM})
		}
	}
	if err != nil {
		return nil, err
	}
	out := &placementpb.Preview{Scope: proto.CloneOf(req.Scope), Verb: req.Verb, Digest: digest, Reason: "demo_simulated_" + string(decision.Reason),
		ObservedAt: timestamppb.New(now), ExpiresAt: timestamppb.New(until), Complete: decision.Complete, SourceEvaluated: sourceEvaluated,
		ActiveIngestClusterId: request.ActiveIngestClusterID}
	explain := func(clusterID, nodeID, groupID, reason string, distance *float64) *placementpb.PreviewCandidate {
		cluster := clusters[clusterID]
		row := &placementpb.PreviewCandidate{ClusterId: clusterID, ClusterName: cluster.name, Region: cluster.region,
			GroupId: groupID, Reason: reason, DistanceKm: distance}
		for _, choice := range decision.Choices {
			if choice.NodeID == nodeID && choice.GroupID == groupID {
				row.RequiresSourcePull = choice.RequiresPull
			}
		}
		if policy != nil {
			for _, group := range policy.Groups {
				if group.ID == groupID {
					if price := placement.PreviewComparisonPrice(byNode[nodeID], group, now); price != nil {
						row.Price = &placementpb.PreviewPrice{AmountMicros: price.AmountMicros, Currency: price.Currency, Unit: price.Unit, Revision: price.Revision, ExpiresAt: timestamppb.New(price.ExpiresAt)}
					}
				}
			}
		}
		return row
	}
	if len(decision.Choices) > 0 {
		choice := decision.Choices[0]
		out.Selected = explain(choice.ClusterID, choice.NodeID, choice.GroupID, "selected", choice.DistanceKM)
	}
	for _, assessment := range decision.Assessments {
		out.Candidates = append(out.Candidates, explain(assessment.ClusterID, assessment.NodeID, assessment.GroupID, string(assessment.Reason), assessment.DistanceKM))
	}
	for _, transition := range decision.Transitions {
		out.Transitions = append(out.Transitions, &placementpb.PreviewTransition{FromGroup: transition.FromGroup, Reason: string(transition.Reason)})
	}
	return out, nil
}
