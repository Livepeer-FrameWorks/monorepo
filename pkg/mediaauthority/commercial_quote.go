package mediaauthority

import (
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

// PlacementCommercialRequest derives the complete comparison set from one
// effective policy. Runtime health and preference matching cannot narrow grants.
func PlacementCommercialRequest(tenant *mediapb.TenantAuthority, object *mediapb.MediaObjectAuthority, verb placement.Verb) (*pb.CommercialQuoteRequest, error) {
	if !IsPlacementSchema(tenant.GetSchemaVersion()) || tenant.GetSchemaVersion() != object.GetSchemaVersion() {
		return nil, fmt.Errorf("%w: commercial policy requires a placement schema", ErrUnknownSchema)
	}
	if len(tenant.GetEffectiveClusterGrants()) == 0 || len(tenant.GetEffectiveClusterGrants()) > 4096 {
		return nil, fmt.Errorf("%w: invalid commercial grant census", ErrMalformed)
	}
	policy, err := EffectivePlacement(tenant, object, verb)
	if err != nil {
		return nil, err
	}
	digest, err := placement.Digest(policy)
	if err != nil {
		return nil, err
	}
	request := &pb.CommercialQuoteRequest{TenantId: object.GetTenantId(), ObjectId: placementCommercialObjectID(object), PolicyRevision: object.GetMediaPlacement().GetRevision(), ParentRevision: object.GetPlacementTenantRevision(), PolicyDigest: digest}
	switch verb {
	case placement.Ingest:
		if object.GetObjectKind() != mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM {
			return nil, fmt.Errorf("%w: artifact ingest quote", ErrMalformed)
		}
		request.Verb = pb.Verb_VERB_INGEST
	case placement.Serve:
		request.Verb = pb.Verb_VERB_SERVE
	default:
		return nil, fmt.Errorf("%w: unsupported commercial verb", ErrMalformed)
	}
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		request.ClusterIds = append(request.ClusterIds, grant.GetClusterId())
	}
	seen := map[[2]string]bool{}
	if policy != nil {
		for _, group := range policy.Groups {
			if group.Order != placement.PriceFirst {
				continue
			}
			key := [2]string{group.PriceCurrency, group.PriceUnit}
			if seen[key] {
				continue
			}
			seen[key] = true
			request.Bases = append(request.Bases, &pb.ComparisonBasis{Currency: key[0], Unit: key[1]})
		}
	}
	canonical, _, err := placement.CanonicalCommercialQuoteRequest(request)
	return canonical, err
}

func placementCommercialObjectID(object *mediapb.MediaObjectAuthority) string {
	switch object.GetObjectKind() {
	case mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM:
		if object.GetLiveStream().GetStreamId() != "" {
			return LiveStreamAuthorityID(object.GetLiveStream().GetStreamId())
		}
	case mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT:
		if object.GetArtifact().GetArtifactId() != "" {
			return ArtifactAuthorityID(object.GetArtifact().GetArtifactId())
		}
	}
	return ""
}

// PlacementCommercialQuote rebinds signed billing evidence to the joined tenant
// and object. A missing quote leaves prices unknown; it never selects tenant-wide
// prices or another object's comparison ratio. The result does not alias authority.
func PlacementCommercialQuote(tenant *mediapb.TenantAuthority, object *mediapb.MediaObjectAuthority, verb placement.Verb, now time.Time) (*pb.CommercialQuoteResponse, error) {
	if verb != placement.Ingest && verb != placement.Serve {
		return nil, fmt.Errorf("%w: invalid commercial verb", ErrMalformed)
	}
	if len(object.GetCommercialQuotes()) > 2 {
		return nil, fmt.Errorf("%w: too many commercial verb quotes", ErrMalformed)
	}
	var selected *pb.CommercialQuoteResponse
	seen := map[pb.Verb]bool{}
	for _, quote := range object.GetCommercialQuotes() {
		quoteVerb := quote.GetScope().GetVerb()
		if seen[quoteVerb] {
			return nil, fmt.Errorf("%w: duplicate commercial verb quote", ErrMalformed)
		}
		seen[quoteVerb] = true
		var mediaVerb placement.Verb
		switch quoteVerb {
		case pb.Verb_VERB_INGEST:
			mediaVerb = placement.Ingest
		case pb.Verb_VERB_SERVE:
			mediaVerb = placement.Serve
		default:
			return nil, fmt.Errorf("%w: invalid commercial verb quote", ErrMalformed)
		}
		request, err := PlacementCommercialRequest(tenant, object, mediaVerb)
		if err != nil {
			return nil, err
		}
		if err := placement.ValidateCommercialQuoteResponse(request, quote, now); err != nil {
			return nil, fmt.Errorf("%w: commercial quote: %w", ErrMalformed, err)
		}
		entitlement, entitlementErr := PlacementCommercialEntitlementDigest(tenant)
		if entitlementErr != nil || quote.GetEntitlementDigest() != entitlement {
			return nil, fmt.Errorf("%w: commercial entitlement differs from joined grants", ErrMalformed)
		}
		if mediaVerb == verb {
			selected = quote
		}
	}
	return proto.CloneOf(selected), nil
}

// PlacementCommercialEntitlementDigest reproduces the billing owner's permission
// projection from signed grants, including explicit expiry and consent flags.
func PlacementCommercialEntitlementDigest(tenant *mediapb.TenantAuthority) (string, error) {
	peers := make([]*clusterpb.TenantClusterPeer, 0, len(tenant.GetEffectiveClusterGrants()))
	for _, grant := range tenant.GetEffectiveClusterGrants() {
		peers = append(peers, &clusterpb.TenantClusterPeer{ClusterId: grant.GetClusterId(), ClusterClass: grant.GetClusterClass(), OwnerTenantId: grant.GetOwnerTenantId(), AccessActive: grant.GetSubscriptionStatus() == "active", SubscriptionStatus: grant.GetSubscriptionStatus(), AccessSource: grant.GetAccessSource(), AccessExpiresAt: proto.CloneOf(grant.GetExpiresAt()), MediaConsent: proto.CloneOf(grant.GetMediaConsent())})
	}
	return placement.CommercialEntitlementDigest(tenant.GetTenantId(), peers)
}

func validateObjectCommercialQuotes(envelope *mediapb.AuthorityEnvelope, object *mediapb.MediaObjectAuthority) error {
	if len(object.GetCommercialQuotes()) > 2 {
		return fmt.Errorf("%w: too many commercial quotes", ErrMalformed)
	}
	seen := map[pb.Verb]bool{}
	for _, quote := range object.GetCommercialQuotes() {
		scope := quote.GetScope()
		if seen[scope.GetVerb()] || scope.GetTenantId() != object.GetTenantId() || scope.GetObjectId() != placementCommercialObjectID(object) || scope.GetPolicyRevision() != object.GetMediaPlacement().GetRevision() || scope.GetParentRevision() != object.GetPlacementTenantRevision() {
			return fmt.Errorf("%w: commercial quote object scope differs", ErrMalformed)
		}
		seen[scope.GetVerb()] = true
		if object.GetObjectKind() != mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM && scope.GetVerb() == pb.Verb_VERB_INGEST {
			return fmt.Errorf("%w: artifact ingest quote", ErrMalformed)
		}
		if err := placement.ValidateCommercialQuoteResponse(scope, quote, envelope.GetIssuedAt().AsTime()); err != nil {
			return fmt.Errorf("%w: commercial quote: %w", ErrMalformed, err)
		}
		if envelope.GetValidUntil().AsTime().After(quote.GetExpiresAt().AsTime()) {
			return fmt.Errorf("%w: object authority outlives commercial quote", ErrMalformed)
		}
	}
	return nil
}
