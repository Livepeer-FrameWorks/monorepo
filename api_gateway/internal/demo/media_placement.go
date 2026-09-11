package demo

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const demoPlacementNotice = "Simulated, read-only demo. No live capacity is observed, no media is prepared, and no changes are saved."

func ValidateMediaPlacementScope(scope *placementpb.Scope) error {
	if scope == nil {
		return status.Error(codes.InvalidArgument, "demo placement scope is required")
	}
	switch scope.GetKind() {
	case placementpb.ScopeKind_SCOPE_KIND_TENANT:
		if scope.GetStreamId() == "" {
			return nil
		}
	case placementpb.ScopeKind_SCOPE_KIND_STREAM:
		for _, stream := range GenerateStreams() {
			if stream.GetStreamId() == scope.GetStreamId() {
				return nil
			}
		}
		return status.Error(codes.NotFound, "demo stream not found")
	}
	return status.Error(codes.InvalidArgument, "invalid demo placement scope")
}

func demoPlacementRollout() *placementpb.Rollout {
	return &placementpb.Rollout{Status: placementpb.RolloutStatus_ROLLOUT_STATUS_NOT_CONFIGURED,
		ExistingSessionsRetained: true, PendingRecipients: []*placementpb.RolloutRecipient{{
			Id: "demo-only", Name: "Simulated demo", Status: "NOT_CONFIGURED", Reason: demoPlacementNotice,
		}}}
}

// GenerateMediaPlacementPolicy returns fresh synthetic data, never a saved or
// effective live policy. Demo requests do not share mutable policy state.
func GenerateMediaPlacementPolicy(scope *placementpb.Scope) (*placementpb.PolicyState, error) {
	if err := ValidateMediaPlacementScope(scope); err != nil {
		return nil, err
	}
	return &placementpb.PolicyState{Scope: proto.CloneOf(scope), Own: &placementpb.PolicySet{},
		Rollout: demoPlacementRollout(), Actions: &placementpb.Actions{CanRead: true, CanPreview: true},
		Features: &placementpb.Features{SchemaVersion: placement.SchemaVersion, GeographicSpillover: true, PriceOrdering: true,
			SupportedPresets: []string{"closest_available", "my_clusters_first", "my_clusters_only", "no_official"}}}, nil
}

type demoPlacementCluster struct {
	id, name, region, owner string
	class                   placementpb.ClusterClass
	lat, lon                float64
	price                   uint64
}

func demoPlacementClusters() []demoPlacementCluster {
	return []demoPlacementCluster{
		{"cluster_demo_us_west", "Simulated US West official", "us-west", "demo-platform", placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL, 37.77, -122.42, 120000},
		{"cluster_demo_eu_west", "Simulated EU West official", "eu-west", "demo-platform", placementpb.ClusterClass_CLUSTER_CLASS_PLATFORM_OFFICIAL, 52.37, 4.90, 120000},
		{DemoSelfHostedCluster, "Simulated self-hosted Berlin", "eu-central", DemoTenantID, placementpb.ClusterClass_CLUSTER_CLASS_TENANT_PRIVATE, 52.52, 13.40, 0},
		{"demo-placement-marketplace", "Simulated US marketplace", "us-west", "demo-marketplace-owner", placementpb.ClusterClass_CLUSTER_CLASS_THIRD_PARTY_MARKETPLACE, 34.05, -118.24, 60000},
	}
}

func GenerateMediaPlacementOptions(req *placementpb.GetOptionsRequest) (*placementpb.Options, error) {
	if err := ValidateMediaPlacementScope(req.GetScope()); err != nil {
		return nil, err
	}
	if req.GetFirst() < 1 || req.GetFirst() > 100 {
		return nil, status.Error(codes.InvalidArgument, "invalid demo options page size")
	}
	filter := req.GetFilter()
	kind := filter.GetKind()
	if kind == placementpb.OptionKind_OPTION_KIND_UNSPECIFIED {
		kind = placementpb.OptionKind_OPTION_KIND_CLUSTER
	}
	if kind != placementpb.OptionKind_OPTION_KIND_CLUSTER && kind != placementpb.OptionKind_OPTION_KIND_REGION && kind != placementpb.OptionKind_OPTION_KIND_OPERATOR {
		return nil, status.Error(codes.InvalidArgument, "invalid demo option kind")
	}
	var options []*placementpb.Option
	seen := map[string]bool{}
	for _, cluster := range demoPlacementClusters() {
		if len(filter.GetClasses()) != 0 && !slices.Contains(filter.GetClasses(), cluster.class) {
			continue
		}
		id, name := cluster.id, cluster.name
		switch kind {
		case placementpb.OptionKind_OPTION_KIND_REGION:
			id, name = cluster.region, "Simulated "+cluster.region
		case placementpb.OptionKind_OPTION_KIND_OPERATOR:
			id, name = cluster.owner, "Simulated "+cluster.owner
		}
		if seen[id] || !strings.Contains(strings.ToLower(id+" "+name), strings.ToLower(strings.TrimSpace(filter.GetQuery()))) {
			continue
		}
		seen[id] = true
		option := &placementpb.Option{Id: id, Name: name, Kind: kind, Eligible: true, Reason: demoPlacementNotice}
		if kind == placementpb.OptionKind_OPTION_KIND_CLUSTER {
			option.ClusterClass, option.Region, option.OwnerId = cluster.class, cluster.region, cluster.owner
		}
		options = append(options, option)
	}
	slices.SortFunc(options, func(a, b *placementpb.Option) int { return strings.Compare(a.Id, b.Id) })
	// Cursor identity includes scope and filters, not page size. It cannot index
	// another inventory or escape this fixed synthetic list.
	identity, err := (proto.MarshalOptions{Deterministic: true}).Marshal(&placementpb.GetOptionsRequest{Scope: req.Scope, Filter: filter})
	if err != nil {
		return nil, err
	}
	prefix := fmt.Sprintf("demo-placement:%x:", sha256.Sum256(identity))
	start := 0
	if cursor := req.GetAfter(); cursor != "" {
		index := strings.TrimPrefix(cursor, prefix)
		parsed, parseErr := strconv.Atoi(index)
		if !strings.HasPrefix(cursor, prefix) || parseErr != nil || strconv.Itoa(parsed) != index || parsed < 0 || parsed >= len(options) {
			return nil, status.Error(codes.InvalidArgument, "invalid demo options cursor")
		}
		start = parsed + 1
	}
	end := min(start+int(req.First), len(options))
	out := &placementpb.Options{Scope: proto.CloneOf(req.Scope), Nodes: options[start:end], HasPreviousPage: start > 0, HasNextPage: end < len(options)}
	if end > start {
		out.StartCursor, out.EndCursor = prefix+strconv.Itoa(start), prefix+strconv.Itoa(end-1)
	}
	return out, nil
}

func GenerateMediaPlacementReview(req *placementpb.ReviewChangeRequest, now time.Time) (*placementpb.Review, error) {
	if err := placement.ValidateReviewChangeRequest(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := ValidateMediaPlacementScope(req.Scope); err != nil {
		return nil, err
	}
	if req.ExpectedRevision != 0 || req.ExpectedParentRevision != 0 {
		return nil, status.Error(codes.Aborted, "demo policy revision changed")
	}
	updated, err := placement.ApplyUpdates(nil, req.Updates)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	differences, err := placement.DescribePolicyChange(nil, updated)
	if err != nil {
		return nil, err
	}
	tenant, stream := demoPlacementPolicySets(req.Scope, updated)
	digest, err := placement.PolicySetsDigest(tenant, stream)
	if err != nil {
		return nil, err
	}
	return &placementpb.Review{ReviewToken: "demo-preview-only:" + digest, Digest: digest, ExpiresAt: timestamppb.New(now.Add(placement.ReviewLifetime)),
		Differences: differences, Warnings: []*placementpb.Warning{{Id: "demo-simulated", Message: demoPlacementNotice}},
		Impact: &placementpb.Impact{ExistingSessionsRetained: true}}, nil
}

func demoPlacementPolicySets(scope *placementpb.Scope, own *placementpb.PolicySet) (tenant, stream *placementpb.PolicySet) {
	if scope.GetKind() == placementpb.ScopeKind_SCOPE_KIND_TENANT {
		return own, nil
	}
	return nil, own
}

func GenerateMediaCapacityConsent(clusterID string) (*quartermasterpb.ClusterMediaConsentState, error) {
	if clusterID != DemoSelfHostedCluster {
		return nil, status.Error(codes.NotFound, "owned demo cluster not found")
	}
	return &quartermasterpb.ClusterMediaConsentState{ClusterId: clusterID,
		Consent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true}, Rollout: demoPlacementRollout()}, nil
}

func GenerateMediaCapacityReview(req *quartermasterpb.ReviewClusterMediaConsentRequest, now time.Time) (*placementpb.Review, error) {
	if _, err := GenerateMediaCapacityConsent(req.GetClusterId()); err != nil {
		return nil, err
	}
	if req.GetExpectedRevision() != 0 {
		return nil, status.Error(codes.Aborted, "demo consent revision changed")
	}
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(req)
	if err != nil {
		return nil, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(encoded))
	out := &placementpb.Review{ReviewToken: "demo-preview-only:" + digest, Digest: digest, ExpiresAt: timestamppb.New(now.Add(placement.ReviewLifetime)),
		Warnings: []*placementpb.Warning{{Id: "demo-simulated", Message: demoPlacementNotice}}, Impact: &placementpb.Impact{ExistingSessionsRetained: true}}
	for _, field := range []struct {
		path, label string
		value       bool
	}{
		{"allowIngest", "Accept publishers", req.GetAllowIngest()}, {"allowServe", "Serve viewers", req.GetAllowServe()}, {"allowExternalSource", "Pull media from other clusters", req.GetAllowExternalSource()},
	} {
		if !field.value {
			out.Differences = append(out.Differences, &placementpb.Difference{Path: field.path, Label: field.label, Before: "true", After: "false"})
		}
	}
	return out, nil
}
