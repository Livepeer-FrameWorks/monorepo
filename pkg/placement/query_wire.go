package placement

import (
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func ValidateCandidateQuery(query *placementpb.CandidateQuery) error {
	if query == nil {
		return fmt.Errorf("placement query is required")
	}
	if err := rejectUnknownWire(query); err != nil {
		return err
	}
	if proto.Size(query) > 1<<20 || len(query.GetClusterIds()) == 0 || len(query.GetClusterIds()) > 4096 {
		return fmt.Errorf("placement query exceeds inventory bound")
	}
	for _, value := range []string{query.GetTenantId(), query.GetObjectId(), query.GetInternalName(), query.GetProtocol()} {
		if !validReviewID(value) {
			return fmt.Errorf("placement query identity and protocol are required")
		}
	}
	if query.GetVerb() != placementpb.Verb_VERB_INGEST && query.GetVerb() != placementpb.Verb_VERB_SERVE {
		return fmt.Errorf("unsupported placement verb")
	}
	if strings.ToLower(query.GetProtocol()) != query.GetProtocol() || len(query.GetProtocol()) > 32 {
		return fmt.Errorf("placement protocol must be canonical")
	}
	if query.GetSourceGeneration() != "" && !validReviewID(query.GetSourceGeneration()) {
		return fmt.Errorf("invalid source generation")
	}
	if query.GetPolicyRevision() > math.MaxInt64 || query.GetParentRevision() > math.MaxInt64 {
		return fmt.Errorf("invalid placement revision")
	}
	digest, err := hex.DecodeString(query.GetPolicyDigest())
	if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != query.GetPolicyDigest() {
		return fmt.Errorf("canonical placement policy digest is required")
	}
	seen := make(map[string]bool, len(query.GetClusterIds()))
	for _, cluster := range query.GetClusterIds() {
		if !validReviewID(cluster) || seen[cluster] {
			return fmt.Errorf("invalid or duplicate placement cluster")
		}
		seen[cluster] = true
	}
	if location := query.GetClientLocation(); location != nil {
		if !finite(location.GetLatitude()) || !finite(location.GetLongitude()) || math.Abs(location.GetLatitude()) > 90 || math.Abs(location.GetLongitude()) > 180 {
			return fmt.Errorf("invalid trusted client location")
		}
	}
	return nil
}

func ValidatePreparationRequest(request *placementpb.PreparePlacementRequest) error {
	if request == nil {
		return fmt.Errorf("placement preparation is required")
	}
	if err := rejectUnknownWire(request); err != nil {
		return err
	}
	if err := ValidateCandidateQuery(request.GetQuery()); err != nil {
		return err
	}
	if request.GetExpiresAt() == nil || !request.GetExpiresAt().IsValid() {
		return fmt.Errorf("placement decision expiry is required")
	}
	if !validReviewID(request.GetNodeId()) || !slices.Contains(request.GetQuery().GetClusterIds(), request.GetClusterId()) {
		return fmt.Errorf("exact destination node and requested cluster are required")
	}
	if _, err := PreparationIssuedAt(request.GetAttemptId()); err != nil {
		return err
	}
	return nil
}

// ValidatePreparationDeadline checks the request's bounded validity window before
// side effects and acknowledgement. Durable idempotency must additionally bind
// this deadline so a changed request cannot renew an existing attempt.
func ValidatePreparationDeadline(request *placementpb.PreparePlacementRequest, now time.Time) error {
	if err := ValidatePreparationRequest(request); err != nil {
		return err
	}
	until := request.GetExpiresAt().AsTime()
	issued, err := PreparationIssuedAt(request.GetAttemptId())
	if err != nil || now.IsZero() || now.Add(PreparationClockSkew).Before(issued) || !now.Before(until) || until.After(issued.Add(PreparationLifetime)) {
		return fmt.Errorf("placement decision is expired or exceeds its lifetime")
	}
	return nil
}

func ValidatePreparationResponse(request *placementpb.PreparePlacementRequest, response *placementpb.Preparation, now time.Time) error {
	if err := ValidatePreparationDeadline(request, now); err != nil {
		return err
	}
	if response == nil || proto.Size(response) > 1<<20 {
		return fmt.Errorf("placement acknowledgement is missing or oversized")
	}
	if err := rejectUnknownWire(response); err != nil {
		return err
	}
	query := request.GetQuery()
	if response.TenantAuthorityVersion < 0 || response.ObjectAuthorityVersion < 0 || (response.TenantAuthorityVersion == 0) != (response.ObjectAuthorityVersion == 0) {
		return fmt.Errorf("placement acknowledgement has incomplete authority binding")
	}
	if response.GetTenantId() != query.GetTenantId() || response.GetObjectId() != query.GetObjectId() || response.GetSourceGeneration() != query.GetSourceGeneration() ||
		response.GetClusterId() != request.GetClusterId() || response.GetNodeId() != request.GetNodeId() || response.GetAttemptId() != request.GetAttemptId() ||
		response.GetProtocol() != query.GetProtocol() || response.GetPolicyDigest() != query.GetPolicyDigest() || response.GetPolicyRevision() != query.GetPolicyRevision() || response.GetParentRevision() != query.GetParentRevision() {
		return fmt.Errorf("placement acknowledgement does not match the exact request")
	}
	if response.GetExpiresAt() == nil || !response.GetExpiresAt().IsValid() || !now.Before(response.GetExpiresAt().AsTime()) || response.GetExpiresAt().AsTime().After(request.GetExpiresAt().AsTime()) {
		return fmt.Errorf("placement acknowledgement exceeds the decision lifetime")
	}
	switch response.GetOutcome() {
	case placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED:
		if response.GetEndpoint() == "" {
			return fmt.Errorf("accepted placement requires an endpoint")
		}
		if query.GetVerb() == placementpb.Verb_VERB_INGEST && (response.GetReady() || !mist.ValidIngestEndpointTemplate(response.GetEndpoint(), query.GetProtocol()) ||
			response.GetPublicBaseUrl() == "" || mist.IngestPublicOrigin(response.GetPublicBaseUrl()) != response.GetPublicBaseUrl()) {
			return fmt.Errorf("ingest preparation requires a credential-free listener template, not publisher readiness")
		}
	case placementpb.PreparationOutcome_PREPARATION_OUTCOME_CAPACITY_EXHAUSTED,
		placementpb.PreparationOutcome_PREPARATION_OUTCOME_NODE_UNAVAILABLE,
		placementpb.PreparationOutcome_PREPARATION_OUTCOME_SOURCE_UNAVAILABLE:
		if response.GetEndpoint() != "" || response.GetPublicBaseUrl() != "" || response.GetReady() {
			return fmt.Errorf("refused placement cannot advertise an endpoint or readiness")
		}
	default:
		return fmt.Errorf("placement acknowledgement has an unknown outcome")
	}
	return nil
}

func ValidateReviewChangeRequest(request *placementpb.ReviewChangeRequest) error {
	if request == nil {
		return fmt.Errorf("placement change is required")
	}
	if err := rejectUnknownWire(request); err != nil {
		return err
	}
	if proto.Size(request) > 1<<20 || request.GetExpectedRevision() >= math.MaxInt64 || request.GetExpectedParentRevision() > math.MaxInt64 {
		return fmt.Errorf("placement change exceeds size or revision bound")
	}
	scope := request.GetScope()
	switch scope.GetKind() {
	case placementpb.ScopeKind_SCOPE_KIND_TENANT:
		if scope.GetStreamId() != "" || request.GetExpectedParentRevision() != 0 {
			return fmt.Errorf("tenant placement cannot include a stream or parent revision")
		}
	case placementpb.ScopeKind_SCOPE_KIND_STREAM:
		if !validReviewID(scope.GetStreamId()) {
			return fmt.Errorf("stream placement scope is required")
		}
	default:
		return fmt.Errorf("unsupported placement scope")
	}
	_, err := ApplyUpdates(&placementpb.PolicySet{Revision: request.GetExpectedRevision()}, request.GetUpdates())
	return err
}

func ValidateApplyChangeRequest(request *placementpb.ApplyChangeRequest) error {
	if request == nil {
		return fmt.Errorf("placement apply is required")
	}
	if err := rejectUnknownWire(request); err != nil {
		return err
	}
	if err := ValidateReviewChangeRequest(request.GetChange()); err != nil {
		return err
	}
	if !validReviewID(request.GetIdempotencyKey()) || len(request.GetIdempotencyKey()) > 128 || len(request.GetReviewToken()) > 16384 || len(request.GetAcknowledgedWarningIds()) > 64 {
		return fmt.Errorf("placement apply identifiers exceed bounds")
	}
	for _, id := range request.GetAcknowledgedWarningIds() {
		if !validReviewID(id) {
			return fmt.Errorf("invalid placement acknowledgement ID")
		}
	}
	return nil
}
