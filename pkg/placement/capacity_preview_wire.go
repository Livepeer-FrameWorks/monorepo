package placement

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func ValidateCapacityPreviewQuery(query *placementpb.CapacityPreviewQuery) error {
	if query == nil || proto.Size(query) > 1<<20 || len(query.GetClusterIds()) == 0 || len(query.GetClusterIds()) > 4096 {
		return fmt.Errorf("bounded capacity preview query is required")
	}
	if err := rejectUnknownWire(query); err != nil {
		return err
	}
	for _, id := range []string{query.GetTenantId(), query.GetControlCellId()} {
		if !validReviewID(id) || len(id) > 100 {
			return fmt.Errorf("capacity preview tenant and cell are required")
		}
	}
	if query.GetInternalName() != "" && !validReviewID(query.GetInternalName()) {
		return fmt.Errorf("invalid capacity preview stream")
	}
	if query.GetVerb() != placementpb.Verb_VERB_INGEST && query.GetVerb() != placementpb.Verb_VERB_SERVE {
		return fmt.Errorf("unsupported capacity preview verb")
	}
	if !validReviewID(query.GetProtocol()) || len(query.GetProtocol()) > 32 || strings.ToLower(query.GetProtocol()) != query.GetProtocol() {
		return fmt.Errorf("canonical capacity preview protocol is required")
	}
	seen := make(map[string]bool, len(query.GetClusterIds()))
	for _, id := range query.GetClusterIds() {
		if !validReviewID(id) || len(id) > 100 || seen[id] {
			return fmt.Errorf("invalid capacity preview cluster census")
		}
		seen[id] = true
	}
	return nil
}

type CapacitySnapshot struct {
	Candidates []Candidate
	Complete   bool
	ObservedAt time.Time
	ExpiresAt  time.Time
	Consents   map[string]*placementpb.CapacityConsent
}

// DecodeCapacityPreviewObservation binds a read-only response to its exact cell
// census. Missing/stale node facts remain assessable; source and billing claims
// are refused because neither is issued by the capacity observation service.
func DecodeCapacityPreviewObservation(query *placementpb.CapacityPreviewQuery, response *placementpb.CapacityPreviewObservation, now time.Time) (CapacitySnapshot, error) {
	if err := ValidateCapacityPreviewQuery(query); err != nil {
		return CapacitySnapshot{}, err
	}
	invalid := fmt.Errorf("capacity preview observation is inconsistent")
	if response == nil || proto.Size(response) > 8<<20 || len(response.GetCandidates()) > maxCandidates || now.IsZero() {
		return CapacitySnapshot{}, invalid
	}
	if err := rejectUnknownWire(response); err != nil {
		return CapacitySnapshot{}, err
	}
	if response.GetTenantId() != query.GetTenantId() || response.GetControlCellId() != query.GetControlCellId() || response.GetVerb() != query.GetVerb() || response.GetProtocol() != query.GetProtocol() || response.GetInternalName() != query.GetInternalName() {
		return CapacitySnapshot{}, invalid
	}
	requested, returned := slices.Clone(query.GetClusterIds()), slices.Clone(response.GetClusterIds())
	slices.Sort(requested)
	slices.Sort(returned)
	if !slices.Equal(requested, returned) {
		return CapacitySnapshot{}, invalid
	}
	if len(response.GetClusterConsents()) != len(requested) {
		return CapacitySnapshot{}, invalid
	}
	consents := make(map[string]*placementpb.CapacityConsent, len(requested))
	for _, id := range requested {
		consent := response.GetClusterConsents()[id]
		if consent == nil || consent.GetRevision() > math.MaxInt64 {
			return CapacitySnapshot{}, invalid
		}
		consents[id] = proto.CloneOf(consent)
	}
	stamp, expiry := response.GetObservedAt(), response.GetExpiresAt()
	if stamp == nil || expiry == nil || !stamp.IsValid() || !expiry.IsValid() || stamp.AsTime().After(now) || !now.Before(expiry.AsTime()) || expiry.AsTime().After(stamp.AsTime().Add(30*time.Second)) {
		return CapacitySnapshot{}, invalid
	}
	out := CapacitySnapshot{Complete: response.GetComplete(), ObservedAt: stamp.AsTime(), ExpiresAt: expiry.AsTime(), Consents: consents}
	verb := Serve
	if query.GetVerb() == placementpb.Verb_VERB_INGEST {
		verb = Ingest
	}
	seen := make(map[string]bool)
	for _, wire := range response.GetCandidates() {
		candidate, err := CandidateFromProto(wire, verb)
		if err != nil || candidate.TenantID != query.GetTenantId() || !slices.Contains(requested, candidate.ClusterID) || candidate.NodeID == "" || seen[candidate.NodeID] || candidate.Presence != "" || candidate.SourceFeasible || wire.GetCommercialFacts() != nil {
			return CapacitySnapshot{}, invalid
		}
		seen[candidate.NodeID] = true
		if candidate.ExpiresAt.After(out.ExpiresAt) {
			candidate.ExpiresAt = out.ExpiresAt
		}
		out.Candidates = append(out.Candidates, candidate)
	}
	return out, nil
}
