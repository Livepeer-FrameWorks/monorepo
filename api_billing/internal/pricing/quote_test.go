package pricing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

func commercialProjectionFixture() (*PlacementTariffSnapshot, *placementpb.CommercialQuoteRequest, map[string]time.Time) {
	input := placementCommercialFixture()
	snapshot := &PlacementTariffSnapshot{TenantID: snapshotTenant, AsOf: input.ObservedAt,
		Tier:     &billing.EffectiveTier{MeteringEnabled: true, Currency: "EUR", Rules: input.Resolved.MeteredRules},
		Clusters: map[string]*ClusterPricing{"cluster": input.Resolved}, AllowanceUsage: PlacementAllowanceUsage{Status: PlacementUsageNotRequired}}
	request := &placementpb.CommercialQuoteRequest{TenantId: snapshotTenant, ObjectId: "live_stream:stream", Verb: placementpb.Verb_VERB_SERVE, PolicyRevision: 7, ParentRevision: 3, PolicyDigest: strings.Repeat("a", 64), ClusterIds: []string{"cluster"}, Bases: []*placementpb.ComparisonBasis{{Currency: "EUR", Unit: "serve:minutes=1;gib=2"}}}
	return snapshot, request, map[string]time.Time{"cluster": input.ExpiresAt}
}

func TestCommercialQuoteProjectionBindsScopeAndPreservesInputs(t *testing.T) {
	snapshot, request, access := commercialProjectionFixture()
	before, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	requestBefore := proto.CloneOf(request)
	response, err := ProjectPlacementQuote(snapshot, request, strings.Repeat("b", 64), access, snapshot.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if validateErr := placement.ValidateCommercialQuoteResponse(request, response, snapshot.AsOf); validateErr != nil {
		t.Fatal(validateErr)
	}
	if got := response.GetClusters()[0].GetFacts().GetServePrices()[0].GetAmountMicros(); got != 80000 {
		t.Fatalf("quote amount = %d", got)
	}
	after, err := json.Marshal(snapshot)
	if err != nil || string(before) != string(after) || !proto.Equal(requestBefore, request) {
		t.Fatal("projection changed caller evidence")
	}
	originalRevision := response.GetClusters()[0].GetFacts().GetRevision()
	snapshot.AsOf = snapshot.AsOf.Add(time.Second)
	access["cluster"] = access["cluster"].Add(time.Second)
	renewed, err := ProjectPlacementQuote(snapshot, request, strings.Repeat("b", 64), access, snapshot.AsOf)
	if err != nil || renewed.GetClusters()[0].GetFacts().GetRevision() != originalRevision {
		t.Fatalf("lease renewal revised unchanged tariff facts: %+v %v", renewed, err)
	}
	request.PolicyRevision++
	changed, err := ProjectPlacementQuote(snapshot, request, strings.Repeat("b", 64), access, snapshot.AsOf)
	if err != nil || changed.GetClusters()[0].GetFacts().GetRevision() == originalRevision {
		t.Fatal("policy revision not bound to quote")
	}
	if err := placement.ValidateCommercialQuoteResponse(requestBefore, changed, snapshot.AsOf); err == nil {
		t.Fatal("quote used for another policy revision")
	}
}

func TestCommercialQuoteProjectionDisclosesUsageAndUnavailableBases(t *testing.T) {
	snapshot, request, access := commercialProjectionFixture()
	cluster := snapshot.Clusters["cluster"]
	cluster.MeteredRules[1].Model, cluster.MeteredRules[1].IncludedQuantity = rating.ModelTieredGraduated, decimal.NewFromInt(10)
	snapshot.AllowanceUsage.Status = PlacementUsageMissingSources
	request.Bases = append(request.Bases, &placementpb.ComparisonBasis{Currency: "EUR", Unit: "serve:minutes=1;gib=0"}, &placementpb.ComparisonBasis{Currency: "USD", Unit: "serve:minutes=1;gib=2"}, &placementpb.ComparisonBasis{Currency: "EUR", Unit: "legacy_unit"})
	response, err := ProjectPlacementQuote(snapshot, request, strings.Repeat("b", 64), access, snapshot.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if validateErr := placement.ValidateCommercialQuoteResponse(request, response, snapshot.AsOf); validateErr != nil {
		t.Fatal(validateErr)
	}
	quotes := response.GetClusters()[0]
	if len(quotes.GetFacts().GetServePrices()) != 1 || quotes.GetFacts().GetServePrices()[0].GetAmountMicros() != 20000 || len(quotes.GetRecordedUsageBases()) != 0 || len(quotes.GetUnavailable()) != 3 {
		t.Fatalf("unknown consumption treated as free or zero consumption needed evidence: %+v", quotes)
	}
	reasons := map[placementpb.QuoteUnavailableReason]bool{}
	for _, unavailable := range quotes.GetUnavailable() {
		reasons[unavailable.GetReason()] = true
	}
	for _, want := range []placementpb.QuoteUnavailableReason{placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_USAGE_UNKNOWN, placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_CURRENCY_MISMATCH, placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_UNSUPPORTED_BASIS} {
		if !reasons[want] {
			t.Fatalf("missing unavailable reason %v", want)
		}
	}
	start, end := snapshot.AsOf.Add(-time.Hour), snapshot.AsOf.Add(time.Hour)
	snapshot.Subscription.PeriodStart, snapshot.Subscription.PeriodEnd = &start, &end
	snapshot.AllowanceUsage = PlacementAllowanceUsage{Status: PlacementUsageCovered, PeriodStart: start, PeriodEnd: end, Through: snapshot.AsOf.Add(-3 * time.Minute), Totals: map[string]map[rating.Meter]decimal.Decimal{"cluster": {rating.MeterEgressGB: decimal.NewFromInt(9)}}}
	covered, err := ProjectPlacementQuote(snapshot, request, strings.Repeat("b", 64), access, snapshot.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if err := placement.ValidateCommercialQuoteResponse(request, covered, snapshot.AsOf); err != nil {
		t.Fatal(err)
	}
	quotes = covered.GetClusters()[0]
	if len(quotes.GetFacts().GetServePrices()) != 2 || len(quotes.GetRecordedUsageBases()) != 1 || quotes.GetRecordedUsageBases()[0].GetUnit() != "serve:minutes=1;gib=2" || !covered.GetUsage().GetThrough().AsTime().Equal(snapshot.AllowanceUsage.Through) {
		t.Fatalf("recorded-usage estimate not explicit: %+v", covered)
	}
	for _, price := range quotes.GetFacts().GetServePrices() {
		if price.GetUnit() == "serve:minutes=1;gib=2" && price.GetAmountMicros() != 50000 {
			t.Fatalf("allowance crossing quote = %d", price.GetAmountMicros())
		}
	}
}

func TestCommercialQuoteProjectionRejectsUnboundedSourceBeforeHashing(t *testing.T) {
	for _, field := range []string{"base price", "tier rule", "cluster rule", "usage"} {
		t.Run(field, func(t *testing.T) {
			snapshot, request, access := commercialProjectionFixture()
			unbounded := decimal.New(1, 1000000000)
			switch field {
			case "base price":
				snapshot.Tier.BasePrice = unbounded
			case "tier rule":
				snapshot.Tier.Rules[0].UnitPrice = unbounded
			case "cluster rule":
				snapshot.Clusters["cluster"].MeteredRules[0].IncludedQuantity = unbounded
			case "usage":
				snapshot.AllowanceUsage.Totals = map[string]map[rating.Meter]decimal.Decimal{"cluster": {rating.MeterEgressGB: unbounded}}
			}
			if response, err := ProjectPlacementQuote(snapshot, request, strings.Repeat("b", 64), access, snapshot.AsOf); err == nil || response != nil {
				t.Fatal("unbounded decimal reached snapshot hash")
			}
		})
	}
}

func TestCommercialQuoteProjectionRequiresExactAllowancePeriodAndMeter(t *testing.T) {
	snapshot, request, access := commercialProjectionFixture()
	cluster := snapshot.Clusters["cluster"]
	cluster.MeteredRules[1].Model, cluster.MeteredRules[1].IncludedQuantity = rating.ModelTieredGraduated, decimal.NewFromInt(10)
	start, end := snapshot.AsOf.Add(-time.Hour), snapshot.AsOf.Add(time.Hour)
	snapshot.Subscription.PeriodStart, snapshot.Subscription.PeriodEnd = &start, &end
	snapshot.AllowanceUsage = PlacementAllowanceUsage{Status: PlacementUsageCovered, PeriodStart: start, PeriodEnd: end, Through: snapshot.AsOf.Add(-5 * time.Minute)}
	response, err := ProjectPlacementQuote(snapshot, request, strings.Repeat("b", 64), access, snapshot.AsOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Clusters[0].Facts.ServePrices) != 0 || len(response.Clusters[0].Unavailable) != 1 || response.Clusters[0].Unavailable[0].Reason != placementpb.QuoteUnavailableReason_QUOTE_UNAVAILABLE_REASON_USAGE_UNKNOWN {
		t.Fatalf("covered flag without required meter treated as known consumption: %+v", response)
	}
	snapshot.AllowanceUsage.PeriodStart = start.Add(-time.Hour)
	if response, err = ProjectPlacementQuote(snapshot, request, strings.Repeat("b", 64), access, snapshot.AsOf); err == nil || response != nil {
		t.Fatal("different allowance period accepted")
	}
}
