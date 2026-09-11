package pricing

import (
	"encoding/json"
	"math"
	"slices"
	"testing"
	"time"

	"frameworks/api_billing/internal/rating"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/proto"
)

func placementCommercialFixture() PlacementCommercialInput {
	now := time.Date(2026, 9, 8, 14, 0, 0, 0, time.UTC)
	metering := true
	return PlacementCommercialInput{
		UsageMeteringEnabled: &metering,
		SourceRevision:       "owner-snapshot-1", ObservedAt: now, ExpiresAt: now.Add(30 * time.Second),
		Resolved: &ClusterPricing{Kind: KindThirdPartyMarketplace, Model: ModelMetered, PricingSource: SourceClusterMetered, Currency: "EUR", MeteredRules: []rating.Rule{
			{Meter: rating.MeterDeliveredMinutes, Model: rating.ModelAllUsage, Currency: "EUR", UnitPrice: decimal.RequireFromString("0.02")},
			{Meter: rating.MeterEgressGB, Model: rating.ModelAllUsage, Currency: "EUR", UnitPrice: decimal.RequireFromString("0.03")},
			{Meter: rating.MeterIngressGB, Model: rating.ModelAllUsage, Currency: "EUR", UnitPrice: decimal.RequireFromString("0.01")},
		}},
		Ingest: &PlacementQuoteBasis{Additional: map[rating.Meter]decimal.Decimal{rating.MeterIngressGB: decimal.NewFromInt(2)}},
		Serve:  &PlacementQuoteBasis{Additional: map[rating.Meter]decimal.Decimal{rating.MeterDeliveredMinutes: decimal.NewFromInt(1), rating.MeterEgressGB: decimal.NewFromInt(2)}},
	}
}

func TestPlacementCommercialMultipleComparisonBases(t *testing.T) {
	input := placementCommercialFixture()
	a, err := ParsePlacementQuoteBasis(placementpb.Verb_VERB_SERVE, "serve:minutes=60;gib=2")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParsePlacementQuoteBasis(placementpb.Verb_VERB_SERVE, "serve:minutes=1;gib=0")
	if err != nil {
		t.Fatal(err)
	}
	ingest, err := ParsePlacementQuoteBasis(placementpb.Verb_VERB_INGEST, "ingest:gib=1")
	if err != nil {
		t.Fatal(err)
	}
	input.ServeBases = []*PlacementQuoteBasis{a, b}
	input.IngestBases = []*PlacementQuoteBasis{ingest}
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := PlacementCommercialFacts(input)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]uint64{"serve:minutes=60;gib=2": 1260000, "serve:minutes=1;gib=0": 20000}
	if len(facts.GetServePrices()) != len(want) || len(facts.GetIngestPrices()) != 1 || facts.GetIngestPrices()[0].GetAmountMicros() != 10000 || facts.GetServePrice().GetAmountMicros() != 80000 {
		t.Fatalf("lost comparison prices: %+v", facts)
	}
	for _, quote := range facts.GetServePrices() {
		amount, ok := want[quote.GetUnit()]
		if !ok || quote.GetAmountMicros() != amount {
			t.Fatalf("wrong bundle quote: %+v", quote)
		}
	}
	after, err := json.Marshal(input)
	if err != nil || string(before) != string(after) {
		t.Fatal("collection reordered input")
	}
	slices.Reverse(input.ServeBases)
	input.ObservedAt = input.ObservedAt.Add(time.Second)
	input.ExpiresAt = input.ExpiresAt.Add(time.Second)
	reordered, err := PlacementCommercialFacts(input)
	if err != nil || reordered.GetRevision() != facts.GetRevision() {
		t.Fatalf("ordering/renewal changed revision: %+v %v", reordered, err)
	}
	reordered.ExpiresAt = facts.ExpiresAt
	if !proto.Equal(reordered, facts) {
		t.Fatal("ordering changed quote projection")
	}
	b.Additional[rating.MeterDeliveredMinutes] = decimal.NewFromInt(3)
	changed, err := PlacementCommercialFacts(input)
	if err != nil || changed.GetRevision() == facts.GetRevision() {
		t.Fatalf("comparison basis change did not revise commercial evidence: %+v %v", changed, err)
	}
	input.UsageMeteringEnabled = nil
	unknown, err := PlacementCommercialFacts(input)
	if err != nil || len(unknown.GetServePrices()) != 0 || unknown.GetCharging() != placementpb.Charging_CHARGING_RATED {
		t.Fatalf("unknown metering became a free quote: %+v %v", unknown, err)
	}
}

func TestPlacementCommercialUnknownBasisDoesNotBorrowUsageFromAnother(t *testing.T) {
	input := placementCommercialFixture()
	input.Resolved.MeteredRules[0].Model = rating.ModelTieredGraduated
	input.Resolved.MeteredRules[0].IncludedQuantity = decimal.NewFromInt(10)
	known, err := ParsePlacementQuoteBasis(placementpb.Verb_VERB_SERVE, "serve:minutes=60;gib=2")
	if err != nil {
		t.Fatal(err)
	}
	known.Consumed = map[rating.Meter]decimal.Decimal{rating.MeterDeliveredMinutes: decimal.NewFromInt(10)}
	input.ServeBases = []*PlacementQuoteBasis{known}
	facts, err := PlacementCommercialFacts(input)
	if err != nil || facts.GetServePrice() != nil || len(facts.GetServePrices()) != 1 || facts.GetServePrices()[0].GetAmountMicros() != 1260000 || facts.GetCharging() != placementpb.Charging_CHARGING_RATED {
		t.Fatalf("unknown usage was filled from a different basis: %+v %v", facts, err)
	}
}

func TestPlacementCommercialRejectsAmbiguousComparisonCollections(t *testing.T) {
	for name, mutate := range map[string]func(*PlacementCommercialInput){
		"nil entry":          func(in *PlacementCommercialInput) { in.ServeBases = []*PlacementQuoteBasis{nil} },
		"duplicate singular": func(in *PlacementCommercialInput) { in.ServeBases = []*PlacementQuoteBasis{in.Serve} },
		"duplicate collection": func(in *PlacementCommercialInput) {
			in.ServeBases = []*PlacementQuoteBasis{in.Serve, in.Serve}
			in.Serve = nil
		},
		"too many": func(in *PlacementCommercialInput) { in.ServeBases = make([]*PlacementQuoteBasis, 16) },
		"unbounded decimal": func(in *PlacementCommercialInput) {
			in.ServeBases = []*PlacementQuoteBasis{{Additional: map[rating.Meter]decimal.Decimal{rating.MeterEgressGB: decimal.New(1, 1000000000)}}}
		},
		"inconsistent consumption": func(in *PlacementCommercialInput) {
			in.Serve.Consumed = map[rating.Meter]decimal.Decimal{rating.MeterEgressGB: decimal.NewFromInt(2)}
			in.ServeBases = []*PlacementQuoteBasis{{Additional: map[rating.Meter]decimal.Decimal{rating.MeterEgressGB: decimal.NewFromInt(1), rating.MeterDeliveredMinutes: decimal.NewFromInt(1)}, Consumed: map[rating.Meter]decimal.Decimal{rating.MeterEgressGB: decimal.NewFromInt(3)}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := placementCommercialFixture()
			mutate(&input)
			if _, err := PlacementCommercialFacts(input); err == nil {
				t.Fatal("invalid comparison collection accepted")
			}
		})
	}
}

func TestPlacementCommercialQuoteIncludesEveryRelevantMeter(t *testing.T) {
	input := placementCommercialFixture()
	before, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := PlacementCommercialFacts(input)
	if err != nil || facts.GetCharging() != placementpb.Charging_CHARGING_RATED || facts.GetServePrice().GetAmountMicros() != 80000 || facts.GetIngestPrice().GetAmountMicros() != 20000 {
		t.Fatalf("multi-meter quote: %+v, %v", facts, err)
	}
	if len(facts.GetRevision()) != 64 || len(facts.GetServePrice().GetUnit()) > 64 || facts.GetServePrice().GetUnit() == facts.GetIngestPrice().GetUnit() {
		t.Fatalf("invalid quote binding: %+v", facts)
	}
	after, err := json.Marshal(input)
	if err != nil || string(before) != string(after) {
		t.Fatal("pricing projection mutated input")
	}
	input.Resolved.MeteredRules[0].UnitPrice = decimal.RequireFromString("0.05")
	changed, err := PlacementCommercialFacts(input)
	if err != nil || changed.GetServePrice().GetUnit() != facts.GetServePrice().GetUnit() || changed.GetServePrice().GetAmountMicros() != 110000 || changed.GetRevision() == facts.GetRevision() {
		t.Fatalf("tariff change: %+v, %v", changed, err)
	}
	input.Serve.Additional[rating.MeterEgressGB] = decimal.NewFromInt(3)
	otherBundle, err := PlacementCommercialFacts(input)
	if err != nil || otherBundle.GetServePrice().GetUnit() == facts.GetServePrice().GetUnit() {
		t.Fatalf("different consumption ratio compared as equal: %+v, %v", otherBundle, err)
	}
}

func TestPlacementCommercialClassificationIsNotTemporaryDiscount(t *testing.T) {
	for _, test := range []struct {
		name       string
		kind       ClusterKind
		model      Model
		source     PricingSource
		free, zero bool
	}{
		{"self hosted", KindTenantPrivate, ModelFreeUnmetered, SourceSelfHosted, true, true},
		{"explicitly free official", KindPlatformOfficial, ModelFreeUnmetered, SourceFreeUnmetered, true, true},
		{"monthly access", KindThirdPartyMarketplace, ModelMonthly, SourceIncludedSubscription, false, true},
		{"zero tier rates", KindPlatformOfficial, ModelTierInherit, SourceTier, false, true},
		{"beta waiver", KindPlatformOfficial, ModelMetered, SourceBetaFree, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := placementCommercialFixture()
			input.Resolved.Kind, input.Resolved.Model, input.Resolved.PricingSource = test.kind, test.model, test.source
			if test.zero {
				for index := range input.Resolved.MeteredRules {
					input.Resolved.MeteredRules[index].UnitPrice = decimal.Zero
				}
			}
			facts, err := PlacementCommercialFacts(input)
			if err != nil || (facts.GetCharging() == placementpb.Charging_CHARGING_PERMANENTLY_FREE) != test.free || facts.GetServePrice() == nil || (facts.GetServePrice().GetAmountMicros() == 0) != test.zero {
				t.Fatalf("classification: %+v, %v", facts, err)
			}
		})
	}
}

func TestPlacementQuoteRequiresKnownAllowanceConsumption(t *testing.T) {
	input := placementCommercialFixture()
	input.Resolved.MeteredRules[0].Model = rating.ModelTieredGraduated
	input.Resolved.MeteredRules[0].IncludedQuantity = decimal.NewFromInt(10)
	input.Serve.Additional[rating.MeterDeliveredMinutes] = decimal.NewFromInt(2)
	input.Serve.Additional[rating.MeterEgressGB] = decimal.Zero
	facts, err := PlacementCommercialFacts(input)
	if err != nil || facts.GetServePrice() != nil || facts.GetIngestPrice() == nil {
		t.Fatalf("unknown consumed usage treated as zero: %+v, %v", facts, err)
	}
	input.Serve.Consumed = map[rating.Meter]decimal.Decimal{rating.MeterDeliveredMinutes: decimal.NewFromInt(9)}
	facts, err = PlacementCommercialFacts(input)
	if err != nil || facts.GetServePrice() == nil || facts.GetServePrice().GetAmountMicros() != 20000 {
		t.Fatalf("allowance crossing quote: %+v, %v", facts, err)
	}
	unit, revision := facts.GetServePrice().GetUnit(), facts.GetRevision()
	input.Serve.Consumed[rating.MeterDeliveredMinutes] = decimal.NewFromInt(100)
	facts, err = PlacementCommercialFacts(input)
	if err != nil || facts.GetServePrice().GetAmountMicros() != 40000 || facts.GetServePrice().GetUnit() != unit || facts.GetRevision() == revision {
		t.Fatalf("post-allowance quote: %+v, %v", facts, err)
	}
	input.Serve.Consumed[rating.MeterDeliveredMinutes] = decimal.Zero
	facts, err = PlacementCommercialFacts(input)
	if err != nil || facts.GetServePrice() == nil || facts.GetServePrice().GetAmountMicros() != 0 || facts.GetCharging() != placementpb.Charging_CHARGING_RATED {
		t.Fatalf("known included quantity: %+v, %v", facts, err)
	}
}

func TestPlacementQuoteMissingOrUnsupportedEvidenceIsNotFree(t *testing.T) {
	for name, mutate := range map[string]func(*PlacementCommercialInput){
		"unknown metering switch": func(v *PlacementCommercialInput) { v.UsageMeteringEnabled = nil },
		"rounded unit conversion": func(v *PlacementCommercialInput) {
			v.Resolved.MeteredRules[0].Config = map[string]any{"rated_quantity_divisor": "1e30"}
		},
		"no estimate":   func(v *PlacementCommercialInput) { v.Serve = nil },
		"missing bytes": func(v *PlacementCommercialInput) { delete(v.Serve.Additional, rating.MeterEgressGB) },
		"unrelated quantity": func(v *PlacementCommercialInput) {
			v.Serve.Additional[rating.MeterMediaSeconds] = decimal.NewFromInt(1)
		},
		"negative estimate": func(v *PlacementCommercialInput) { v.Serve.Additional[rating.MeterEgressGB] = decimal.NewFromInt(-1) },
		"negative usage": func(v *PlacementCommercialInput) {
			v.Serve.Consumed = map[rating.Meter]decimal.Decimal{rating.MeterEgressGB: decimal.NewFromInt(-1)}
		},
		"zero bundle": func(v *PlacementCommercialInput) {
			v.Serve.Additional[rating.MeterEgressGB] = decimal.Zero
			v.Serve.Additional[rating.MeterDeliveredMinutes] = decimal.Zero
		},
		"missing dimensions": func(v *PlacementCommercialInput) { v.Resolved.MeteredRules[0].Model = rating.ModelDimensioned },
		"unknown meter role": func(v *PlacementCommercialInput) {
			v.Resolved.MeteredRules = append(v.Resolved.MeteredRules, rating.Rule{Meter: "new_media_meter", Model: rating.ModelAllUsage, Currency: "EUR", UnitPrice: decimal.NewFromInt(1)})
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := placementCommercialFixture()
			mutate(&input)
			facts, err := PlacementCommercialFacts(input)
			if err != nil || facts.GetServePrice() != nil || facts.GetCharging() != placementpb.Charging_CHARGING_RATED {
				t.Fatalf("incomplete evidence manufactured price: %+v, %v", facts, err)
			}
		})
	}
}

func TestPlacementCommercialRevisionIsOrderIndependent(t *testing.T) {
	input := placementCommercialFixture()
	first, err := PlacementCommercialFacts(input)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(input.Resolved.MeteredRules)
	input.Serve.Additional = map[rating.Meter]decimal.Decimal{rating.MeterEgressGB: decimal.RequireFromString("2.000"), rating.MeterDeliveredMinutes: decimal.RequireFromString("1.0")}
	input.ObservedAt, input.ExpiresAt = input.ObservedAt.Add(time.Second), input.ExpiresAt.Add(time.Second)
	second, err := PlacementCommercialFacts(input)
	if err != nil || second.GetRevision() != first.GetRevision() || second.GetServePrice().GetUnit() != first.GetServePrice().GetUnit() {
		t.Fatalf("ordering/renewal changed semantic revision: %+v, %v", second, err)
	}
}

func TestPlacementQuoteMicrosNeverTruncateOrOverflow(t *testing.T) {
	for _, test := range []struct {
		rate   string
		known  bool
		micros uint64
	}{
		{"0.0000001", false, 0}, {"0.000001", true, 1},
		{"18446744073709.551615", true, math.MaxUint64}, {"18446744073709.551616", false, 0},
	} {
		input := placementCommercialFixture()
		input.Resolved.MeteredRules[0].UnitPrice = decimal.RequireFromString(test.rate)
		input.Serve.Additional[rating.MeterEgressGB] = decimal.Zero
		facts, err := PlacementCommercialFacts(input)
		if err != nil || (facts.GetServePrice() != nil) != test.known || facts.GetServePrice().GetAmountMicros() != test.micros {
			t.Fatalf("rate %s: %+v, %v", test.rate, facts, err)
		}
	}
}

func TestPlacementCommercialMalformedOwnerFactsFail(t *testing.T) {
	for name, mutate := range map[string]func(*PlacementCommercialInput){
		"unbounded decimal exponent": func(v *PlacementCommercialInput) {
			v.Serve.Additional[rating.MeterEgressGB] = decimal.New(1, 2147483647)
		},
		"missing resolved":       func(v *PlacementCommercialInput) { v.Resolved = nil },
		"missing owner revision": func(v *PlacementCommercialInput) { v.SourceRevision = "" },
		"expired":                func(v *PlacementCommercialInput) { v.ExpiresAt = v.ObservedAt },
		"unbounded lifetime":     func(v *PlacementCommercialInput) { v.ExpiresAt = v.ObservedAt.Add(time.Hour) },
		"unknown model":          func(v *PlacementCommercialInput) { v.Resolved.Model = "unknown" },
		"free by label only":     func(v *PlacementCommercialInput) { v.Resolved.Model = ModelFreeUnmetered },
		"currency":               func(v *PlacementCommercialInput) { v.Resolved.Currency = "eur" },
		"mixed currency":         func(v *PlacementCommercialInput) { v.Resolved.MeteredRules[0].Currency = "USD" },
		"negative rate":          func(v *PlacementCommercialInput) { v.Resolved.MeteredRules[0].UnitPrice = decimal.NewFromInt(-1) },
		"duplicate meter": func(v *PlacementCommercialInput) {
			v.Resolved.MeteredRules = append(v.Resolved.MeteredRules, v.Resolved.MeteredRules[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := placementCommercialFixture()
			mutate(&input)
			if _, err := PlacementCommercialFacts(input); err == nil {
				t.Fatal("malformed pricing facts accepted")
			}
		})
	}
}

func TestPlacementCommercialKnownDisabledMeteringIsNotPermanentFree(t *testing.T) {
	input := placementCommercialFixture()
	disabled := false
	input.UsageMeteringEnabled = &disabled
	facts, err := PlacementCommercialFacts(input)
	if err != nil || facts.GetServePrice() == nil || facts.GetServePrice().GetAmountMicros() != 0 || facts.GetCharging() != placementpb.Charging_CHARGING_RATED {
		t.Fatalf("disabled subscription metering: %+v, %v", facts, err)
	}
}

func TestPlacementQuoteBasisRoundTripsWithoutInventingConsumption(t *testing.T) {
	for _, test := range []struct {
		verb placementpb.Verb
		unit string
	}{
		{placementpb.Verb_VERB_INGEST, "ingest:gib=2"},
		{placementpb.Verb_VERB_SERVE, "serve:minutes=60;gib=2.25"},
		{placementpb.Verb_VERB_SERVE, "serve:minutes=1;gib=0"},
	} {
		basis, err := ParsePlacementQuoteBasis(test.verb, test.unit)
		if err != nil || basis.Consumed != nil || placementBasisUnit(test.verb, basis.Additional) != test.unit {
			t.Fatalf("basis %q: %+v, %v", test.unit, basis, err)
		}
	}
	for _, unit := range []string{"", "GiB", "serve:minutes=1;gib=", "serve:minutes=01;gib=2", "serve:minutes=1.0;gib=2", "serve:minutes=1e0;gib=2", "serve:minutes=1;gib=-1", "serve:minutes=0;gib=0", "serve:minutes=1;gib=1e2147483647", "serve:minutes=1;gib=2;extra=1", "ingest:gib=1"} {
		if _, err := ParsePlacementQuoteBasis(placementpb.Verb_VERB_SERVE, unit); err == nil {
			t.Fatalf("ambiguous/noncanonical basis accepted: %q", unit)
		}
	}
	if _, err := ParsePlacementQuoteBasis(placementpb.Verb_VERB_UNSPECIFIED, "ingest:gib=1"); err == nil {
		t.Fatal("unknown verb accepted")
	}
}
