package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ProjectPlacementQuote binds a consistent tariff/usage snapshot to the policy
// owner's request and a revalidated entitlement revision. Access deadlines must
// come from that entitlement read, never from a user-supplied quote request.
func ProjectPlacementQuote(snapshot *PlacementTariffSnapshot, request *placementpb.CommercialQuoteRequest, entitlementRevision string, accessUntil map[string]time.Time, now time.Time) (*placementpb.CommercialQuoteResponse, error) {
	scope, digest, err := placement.CanonicalCommercialQuoteRequest(request)
	if err != nil {
		return nil, err
	}
	decoded, err := hex.DecodeString(entitlementRevision)
	if err != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != entitlementRevision || snapshot == nil || snapshot.Tier == nil || snapshot.TenantID != scope.GetTenantId() || now.IsZero() {
		return nil, fmt.Errorf("invalid commercial quote evidence")
	}
	usage, err := quoteUsageEvidence(snapshot)
	if err != nil {
		return nil, err
	}
	sourceRevision, err := placementQuoteSourceRevision(snapshot, digest, entitlementRevision)
	if err != nil {
		return nil, err
	}
	response := &placementpb.CommercialQuoteResponse{Scope: scope, RequestDigest: digest, EntitlementDigest: entitlementRevision, ObservedAt: timestamppb.New(snapshot.AsOf), Usage: usage}
	for _, clusterID := range scope.GetClusterIds() {
		cluster := snapshot.Clusters[clusterID]
		expiry, expiryErr := snapshot.QuoteExpiry(clusterID, now, accessUntil[clusterID])
		if expiryErr != nil || cluster == nil {
			return nil, ErrPlacementQuoteUnavailable
		}
		input := PlacementCommercialInput{Resolved: cluster, UsageMeteringEnabled: &snapshot.Tier.MeteringEnabled, SourceRevision: sourceRevision, ObservedAt: snapshot.AsOf, ExpiresAt: expiry}
		output := &placementpb.ClusterCommercialQuote{ClusterId: clusterID}
		neededUsage := make(map[[2]string]bool)
		knownUsage := make(map[[2]string]bool)
		addUnavailable := func(basis *placementpb.ComparisonBasis, reason placementpb.QuoteUnavailableReason) {
			output.Unavailable = append(output.Unavailable, &placementpb.UnavailableQuote{Basis: proto.CloneOf(basis), Reason: reason})
		}
		for _, comparison := range scope.GetBases() {
			if comparison.GetCurrency() != cluster.Currency {
				addUnavailable(comparison, placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_CURRENCY_MISMATCH)
				continue
			}
			basis, parseErr := ParsePlacementQuoteBasis(scope.GetVerb(), comparison.GetUnit())
			if parseErr != nil {
				addUnavailable(comparison, placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_UNSUPPORTED_BASIS)
				continue
			}
			requiresUsage := quoteBasisNeedsUsage(cluster, basis) && snapshot.Tier.MeteringEnabled
			neededUsage[[2]string{comparison.GetCurrency(), comparison.GetUnit()}] = requiresUsage
			if requiresUsage && snapshot.AllowanceUsage.Status == PlacementUsageCovered {
				consumed := snapshot.AllowanceUsage.Totals[clusterID]
				basis.Consumed = make(map[rating.Meter]decimal.Decimal, len(basis.Additional))
				for meter := range basis.Additional {
					if quantity, known := consumed[meter]; known {
						basis.Consumed[meter] = quantity
					}
				}
				knownUsage[[2]string{comparison.GetCurrency(), comparison.GetUnit()}] = quoteBasisUsageKnown(cluster, basis)
			}
			if scope.GetVerb() == placementpb.Verb_VERB_INGEST {
				input.IngestBases = append(input.IngestBases, basis)
			} else {
				input.ServeBases = append(input.ServeBases, basis)
			}
		}
		facts, projectionErr := PlacementCommercialFacts(input)
		if projectionErr != nil {
			return nil, projectionErr
		}
		output.Facts = facts
		prices := facts.GetServePrices()
		if scope.GetVerb() == placementpb.Verb_VERB_INGEST {
			prices = facts.GetIngestPrices()
		}
		found := make(map[[2]string]bool, len(prices))
		for _, price := range prices {
			key := [2]string{price.GetCurrency(), price.GetUnit()}
			found[key] = true
			if neededUsage[key] {
				output.RecordedUsageBases = append(output.RecordedUsageBases, &placementpb.ComparisonBasis{Currency: key[0], Unit: key[1]})
			}
		}
		for _, basis := range scope.GetBases() {
			key := [2]string{basis.GetCurrency(), basis.GetUnit()}
			needs, requested := neededUsage[key]
			if !requested || found[key] {
				continue
			}
			reason := placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_TARIFF_UNSUPPORTED
			if needs && !knownUsage[key] {
				reason = placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_USAGE_UNKNOWN
			}
			addUnavailable(basis, reason)
		}
		response.Clusters = append(response.Clusters, output)
		if response.ExpiresAt == nil || expiry.Before(response.ExpiresAt.AsTime()) {
			response.ExpiresAt = timestamppb.New(expiry)
		}
	}
	return response, nil
}

func quoteBasisNeedsUsage(cluster *ClusterPricing, basis *PlacementQuoteBasis) bool {
	if cluster.Model == ModelMonthly || cluster.Model == ModelFreeUnmetered {
		return false
	}
	for _, rule := range cluster.MeteredRules {
		if rule.Model == rating.ModelTieredGraduated && rule.IncludedQuantity.IsPositive() && rule.UnitPrice.IsPositive() && basis.Additional[rule.Meter].IsPositive() {
			return true
		}
	}
	return false
}

func quoteBasisUsageKnown(cluster *ClusterPricing, basis *PlacementQuoteBasis) bool {
	for _, rule := range cluster.MeteredRules {
		if rule.Model == rating.ModelTieredGraduated && rule.IncludedQuantity.IsPositive() && rule.UnitPrice.IsPositive() && basis.Additional[rule.Meter].IsPositive() {
			if _, known := basis.Consumed[rule.Meter]; !known {
				return false
			}
		}
	}
	return true
}

func quoteUsageEvidence(snapshot *PlacementTariffSnapshot) (*placementpb.QuoteUsageEvidence, error) {
	statuses := map[PlacementUsageStatus]placementpb.QuoteUsageStatus{
		PlacementUsageNotRequired:    placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_NOT_REQUIRED,
		PlacementUsageUnknownPeriod:  placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_UNKNOWN_PERIOD,
		PlacementUsageNoClosedWindow: placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_NO_CLOSED_WINDOW,
		PlacementUsageMissingSources: placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_MISSING_SOURCES,
		PlacementUsageMissingWindows: placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_MISSING_WINDOWS,
		PlacementUsageOpenAnomalies:  placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_OPEN_ANOMALIES,
		PlacementUsageUnattributed:   placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_UNATTRIBUTED,
		PlacementUsageInvalid:        placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_INVALID,
		PlacementUsageCovered:        placementpb.QuoteUsageStatus_QUOTE_USAGE_STATUS_COVERED_THROUGH_CUTOFF,
	}
	data := snapshot.AllowanceUsage
	status, known := statuses[data.Status]
	if !known {
		return nil, fmt.Errorf("unknown commercial usage evidence")
	}
	if data.Status == PlacementUsageCovered && (!data.Through.After(data.PeriodStart) || data.Through.After(snapshot.AsOf) || !data.PeriodEnd.After(data.Through)) {
		return nil, fmt.Errorf("invalid commercial usage cutoff")
	}
	if data.Status == PlacementUsageCovered {
		start, end, current := snapshot.AllowancePeriod()
		if !current || !start.Equal(data.PeriodStart) || !end.Equal(data.PeriodEnd) {
			return nil, fmt.Errorf("commercial usage belongs to another billing period")
		}
	}
	out := &placementpb.QuoteUsageEvidence{Status: status}
	for _, field := range []struct {
		value  time.Time
		output **timestamppb.Timestamp
	}{{data.PeriodStart, &out.PeriodStart}, {data.PeriodEnd, &out.PeriodEnd}, {data.Through, &out.Through}} {
		if field.value.IsZero() {
			continue
		}
		stamp := timestamppb.New(field.value)
		if !stamp.IsValid() {
			return nil, fmt.Errorf("invalid commercial usage time")
		}
		*field.output = stamp
	}
	return out, nil
}

func placementQuoteSourceRevision(snapshot *PlacementTariffSnapshot, requestDigest, entitlementRevision string) (string, error) {
	if len(snapshot.Clusters) > maxPlacementTariffClusters || len(snapshot.AllowanceUsage.Totals) > maxPlacementTariffClusters || !placementDecimalBounded(snapshot.Tier.BasePrice) || !quoteRulesBounded(snapshot.Tier.Rules) {
		return "", fmt.Errorf("commercial snapshot exceeds bounds")
	}
	for _, totals := range snapshot.AllowanceUsage.Totals {
		if len(totals) > 3 {
			return "", fmt.Errorf("commercial usage exceeds meter bound")
		}
		for _, quantity := range totals {
			if !placementDecimalBounded(quantity) {
				return "", fmt.Errorf("commercial usage exceeds decimal bounds")
			}
		}
	}
	copySnapshot := *snapshot
	copySnapshot.AsOf = time.Time{}
	tier := *snapshot.Tier
	tier.Rules = canonicalQuoteRules(tier.Rules)
	copySnapshot.Tier = &tier
	copySnapshot.Clusters = make(map[string]*ClusterPricing, len(snapshot.Clusters))
	for id, cluster := range snapshot.Clusters {
		if cluster == nil || !quoteRulesBounded(cluster.MeteredRules) {
			return "", fmt.Errorf("missing or unbounded commercial cluster tariff")
		}
		copyCluster := *cluster
		copyCluster.MeteredRules = canonicalQuoteRules(cluster.MeteredRules)
		copySnapshot.Clusters[id] = &copyCluster
	}
	encoded, err := json.Marshal(struct {
		Snapshot                           *PlacementTariffSnapshot
		RequestDigest, EntitlementRevision string
	}{&copySnapshot, requestDigest, entitlementRevision})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("frameworks/placement/quote-source/v1\x00"), encoded...))
	return hex.EncodeToString(digest[:]), nil
}

func quoteRulesBounded(rules []rating.Rule) bool {
	if len(rules) > 128 {
		return false
	}
	for _, rule := range rules {
		if !placementDecimalBounded(rule.UnitPrice) || !placementDecimalBounded(rule.IncludedQuantity) {
			return false
		}
	}
	return true
}

func canonicalQuoteRules(rules []rating.Rule) []rating.Rule {
	out := slices.Clone(rules)
	slices.SortFunc(out, func(a, b rating.Rule) int { return strings.Compare(string(a.Meter), string(b.Meter)) })
	return out
}
