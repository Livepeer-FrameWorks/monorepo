package placement

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CanonicalCommercialQuoteRequest validates scope and detaches/sorts unordered
// cluster and comparison sets. The digest binds every semantic request field.
func CanonicalCommercialQuoteRequest(request *placementpb.CommercialQuoteRequest) (*placementpb.CommercialQuoteRequest, string, error) {
	if request == nil || proto.Size(request) > 1<<20 || len(request.GetClusterIds()) == 0 || len(request.GetClusterIds()) > 4096 || len(request.GetBases()) > MaxPricesPerVerb {
		return nil, "", fmt.Errorf("invalid commercial quote bounds")
	}
	if err := rejectUnknownWire(request); err != nil {
		return nil, "", err
	}
	tenant, err := uuid.Parse(request.GetTenantId())
	if err != nil || tenant == uuid.Nil || tenant.String() != request.GetTenantId() || !validReviewID(request.GetObjectId()) || request.GetPolicyRevision() > math.MaxInt64 || request.GetParentRevision() > math.MaxInt64 {
		return nil, "", fmt.Errorf("invalid commercial quote scope")
	}
	if request.GetVerb() != placementpb.Verb_VERB_INGEST && request.GetVerb() != placementpb.Verb_VERB_SERVE {
		return nil, "", fmt.Errorf("invalid commercial quote verb")
	}
	policyDigest, err := hex.DecodeString(request.GetPolicyDigest())
	if err != nil || len(policyDigest) != 32 || hex.EncodeToString(policyDigest) != request.GetPolicyDigest() {
		return nil, "", fmt.Errorf("invalid commercial policy digest")
	}
	out := proto.CloneOf(request)
	slices.Sort(out.ClusterIds)
	for index, id := range out.ClusterIds {
		if !validReviewID(id) || len(id) > 100 || index > 0 && id == out.ClusterIds[index-1] {
			return nil, "", fmt.Errorf("invalid or duplicate commercial quote cluster")
		}
	}
	for _, basis := range out.Bases {
		if basis == nil || !validPriceBasis(basis.GetCurrency(), basis.GetUnit()) {
			return nil, "", fmt.Errorf("invalid comparison basis")
		}
	}
	slices.SortFunc(out.Bases, func(a, b *placementpb.ComparisonBasis) int {
		if n := strings.Compare(a.GetCurrency(), b.GetCurrency()); n != 0 {
			return n
		}
		return strings.Compare(a.GetUnit(), b.GetUnit())
	})
	for index := 1; index < len(out.Bases); index++ {
		if proto.Equal(out.Bases[index-1], out.Bases[index]) {
			return nil, "", fmt.Errorf("duplicate comparison basis")
		}
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(out)
	if err != nil {
		return nil, "", err
	}
	hash := sha256.Sum256(append([]byte("frameworks/placement/commercial-request/v1\x00"), encoded...))
	return out, hex.EncodeToString(hash[:]), nil
}

// ValidateCommercialQuoteResponse binds complete billing evidence to the exact
// requested object/policy/bases and enforces its original lifetime on receipt.
func ValidateCommercialQuoteResponse(request *placementpb.CommercialQuoteRequest, response *placementpb.CommercialQuoteResponse, now time.Time) error {
	scope, digest, err := CanonicalCommercialQuoteRequest(request)
	if err != nil {
		return err
	}
	if response == nil || proto.Size(response) > 16<<20 || now.IsZero() {
		return fmt.Errorf("invalid commercial response bounds")
	}
	if err := rejectUnknownWire(response); err != nil {
		return err
	}
	if !proto.Equal(scope, response.GetScope()) || digest != response.GetRequestDigest() || len(response.GetClusters()) != len(scope.GetClusterIds()) {
		return fmt.Errorf("commercial response does not match requested scope")
	}
	entitlement, decodeErr := hex.DecodeString(response.GetEntitlementDigest())
	if decodeErr != nil || len(entitlement) != 32 || hex.EncodeToString(entitlement) != response.GetEntitlementDigest() {
		return fmt.Errorf("invalid commercial entitlement digest")
	}
	if response.GetObservedAt() == nil || !response.GetObservedAt().IsValid() || response.GetExpiresAt() == nil || !response.GetExpiresAt().IsValid() {
		return fmt.Errorf("commercial response timestamps required")
	}
	observed, expiry := response.GetObservedAt().AsTime(), response.GetExpiresAt().AsTime()
	if observed.After(now.Add(time.Second)) || !expiry.After(now) || !expiry.After(observed) || expiry.After(observed.Add(time.Minute)) {
		return fmt.Errorf("commercial response lifetime invalid")
	}
	usage := response.GetUsage()
	if usage == nil || usage.GetStatus() < placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_NOT_REQUIRED || usage.GetStatus() > placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_COVERED_THROUGH_CUTOFF {
		return fmt.Errorf("commercial usage status required")
	}
	for _, stamp := range []*timestamppb.Timestamp{usage.GetPeriodStart(), usage.GetPeriodEnd(), usage.GetThrough()} {
		if stamp != nil && !stamp.IsValid() {
			return fmt.Errorf("invalid commercial usage timestamp")
		}
	}
	covered := usage.GetStatus() == placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_COVERED_THROUGH_CUTOFF
	if covered && (usage.GetPeriodStart() == nil || usage.GetPeriodEnd() == nil || usage.GetThrough() == nil || !usage.GetThrough().AsTime().After(usage.GetPeriodStart().AsTime()) || usage.GetThrough().AsTime().After(observed) || !usage.GetPeriodEnd().AsTime().After(observed)) {
		return fmt.Errorf("invalid covered commercial usage interval")
	}
	requested := make(map[[2]string]bool, len(scope.GetBases()))
	for _, basis := range scope.GetBases() {
		requested[[2]string{basis.GetCurrency(), basis.GetUnit()}] = true
	}
	for index, cluster := range response.GetClusters() {
		if cluster == nil || cluster.GetClusterId() != scope.GetClusterIds()[index] {
			return fmt.Errorf("commercial cluster census differs")
		}
		facts := cluster.GetFacts()
		if facts == nil || facts.GetRevision() == "" || len(facts.GetRevision()) > 255 || facts.GetExpiresAt() == nil || !facts.GetExpiresAt().IsValid() || facts.GetExpiresAt().AsTime().Before(expiry) || facts.GetExpiresAt().AsTime().After(observed.Add(time.Minute)) {
			return fmt.Errorf("invalid commercial cluster facts")
		}
		revision, decodeErr := hex.DecodeString(facts.GetRevision())
		if decodeErr != nil || len(revision) != 32 || hex.EncodeToString(revision) != facts.GetRevision() {
			return fmt.Errorf("invalid commercial facts revision")
		}
		if facts.GetCharging() != placementpb.Charging_CHARGING_RATED && facts.GetCharging() != placementpb.Charging_CHARGING_PERMANENTLY_FREE {
			return fmt.Errorf("invalid commercial charging class")
		}
		if err := ValidateCommercialPrices(facts); err != nil {
			return err
		}
		prices := facts.GetServePrices()
		if facts.GetServePrice() != nil || facts.GetIngestPrice() != nil {
			return fmt.Errorf("quote RPC requires explicit comparison collection")
		}
		if scope.GetVerb() == placementpb.Verb_VERB_INGEST {
			if len(prices) != 0 {
				return fmt.Errorf("quote for wrong media verb")
			}
			prices = facts.GetIngestPrices()
		} else if len(facts.GetIngestPrices()) != 0 {
			return fmt.Errorf("quote for wrong media verb")
		}
		accounted, priced := map[[2]string]bool{}, map[[2]string]bool{}
		for _, price := range prices {
			key := [2]string{price.GetCurrency(), price.GetUnit()}
			if !requested[key] || accounted[key] {
				return fmt.Errorf("unrequested or repeated commercial price")
			}
			accounted[key], priced[key] = true, true
		}
		if len(cluster.GetUnavailable()) > len(requested) || len(cluster.GetRecordedUsageBases()) > len(requested) {
			return fmt.Errorf("commercial comparison evidence exceeds request")
		}
		for _, absent := range cluster.GetUnavailable() {
			key := [2]string{absent.GetBasis().GetCurrency(), absent.GetBasis().GetUnit()}
			if !requested[key] || accounted[key] || absent.GetReason() < placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_CURRENCY_MISMATCH || absent.GetReason() > placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_TARIFF_UNSUPPORTED {
				return fmt.Errorf("invalid unavailable commercial price")
			}
			accounted[key] = true
		}
		if len(accounted) != len(requested) {
			return fmt.Errorf("commercial quote silently omitted requested basis")
		}
		recorded := map[[2]string]bool{}
		for _, basis := range cluster.GetRecordedUsageBases() {
			key := [2]string{basis.GetCurrency(), basis.GetUnit()}
			if !covered || !priced[key] || recorded[key] {
				return fmt.Errorf("invalid recorded-usage price evidence")
			}
			recorded[key] = true
		}
		if len(recorded) != 0 && facts.GetExpiresAt().AsTime().After(usage.GetPeriodEnd().AsTime()) {
			return fmt.Errorf("recorded-usage quote outlives billing period")
		}
	}
	return nil
}
