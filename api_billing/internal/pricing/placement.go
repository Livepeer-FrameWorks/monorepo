package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
	"unicode"

	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var ErrPlacementQuoteUnavailable = errors.New("placement quote requires complete comparable usage evidence")

// PlacementQuoteBasis describes one explicit additional consumption bundle in
// canonical meter units (minutes and GiB, despite the *_gb meter names).
// Consumed is the current allowance-period usage for each relevant meter; an
// omitted entry is unknown, not zero. It is unnecessary for allowance-free rates.
type PlacementQuoteBasis struct {
	Additional map[rating.Meter]decimal.Decimal
	Consumed   map[rating.Meter]decimal.Decimal
}

type PlacementCommercialInput struct {
	Resolved *ClusterPricing
	// Nil means the effective subscription's metering switch was not observed.
	UsageMeteringEnabled *bool
	// SourceRevision binds the owner's tier/subscription/history/usage snapshot.
	SourceRevision string
	ObservedAt     time.Time
	ExpiresAt      time.Time
	Ingest         *PlacementQuoteBasis
	Serve          *PlacementQuoteBasis
	IngestBases    []*PlacementQuoteBasis
	ServeBases     []*PlacementQuoteBasis
}

// PlacementCommercialFacts projects an already resolved tenant tariff. It does
// not infer entitlement, read databases, apply beta waivers, or estimate usage.
// Unknown quote inputs retain the charging classification but omit the price.
func PlacementCommercialFacts(input PlacementCommercialInput) (*placementpb.CommercialFacts, error) {
	if input.Resolved == nil || len(input.Resolved.MeteredRules) > 128 || !placementPriceIdentifier(input.SourceRevision, 100) || input.ObservedAt.IsZero() || !input.ExpiresAt.After(input.ObservedAt) || input.ExpiresAt.Sub(input.ObservedAt) > time.Minute || !timestamppb.New(input.ExpiresAt).IsValid() {
		return nil, fmt.Errorf("invalid placement commercial snapshot")
	}
	if !placementBasisBounded(input.Ingest) || !placementBasisBounded(input.Serve) {
		return nil, fmt.Errorf("placement usage basis exceeds bounds")
	}
	ingestBases, err := canonicalPlacementBases(input.Ingest, input.IngestBases)
	if err != nil {
		return nil, err
	}
	serveBases, err := canonicalPlacementBases(input.Serve, input.ServeBases)
	if err != nil {
		return nil, err
	}
	copyResolved := *input.Resolved
	copyResolved.MeteredRules = slices.Clone(input.Resolved.MeteredRules)
	slices.SortFunc(copyResolved.MeteredRules, func(a, b rating.Rule) int { return strings.Compare(string(a.Meter), string(b.Meter)) })
	resolved := &copyResolved
	if len(resolved.Currency) != 3 || strings.IndexFunc(resolved.Currency, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
		return nil, fmt.Errorf("invalid placement pricing currency")
	}
	charging, err := placementCharging(resolved)
	if err != nil {
		return nil, err
	}
	for index, rule := range resolved.MeteredRules {
		if !placementDecimalBounded(rule.UnitPrice) || !placementDecimalBounded(rule.IncludedQuantity) {
			return nil, fmt.Errorf("placement tariff decimal exceeds bounds")
		}
		if index > 0 && rule.Meter == resolved.MeteredRules[index-1].Meter {
			return nil, fmt.Errorf("duplicate placement tariff meter")
		}
		if ruleErr := rating.ValidateRule(rule); ruleErr != nil {
			return nil, ruleErr
		}
		if rule.Currency != resolved.Currency {
			return nil, fmt.Errorf("placement tariff mixes currencies")
		}
	}
	facts := &placementpb.CommercialFacts{Charging: charging, ExpiresAt: timestamppb.New(input.ExpiresAt)}
	for _, request := range []struct {
		verb   placementpb.Verb
		basis  *PlacementQuoteBasis
		output **placementpb.Price
		bases  []*PlacementQuoteBasis
		quotes *[]*placementpb.Price
	}{
		{placementpb.Verb_VERB_INGEST, input.Ingest, &facts.IngestPrice, ingestBases, &facts.IngestPrices},
		{placementpb.Verb_VERB_SERVE, input.Serve, &facts.ServePrice, serveBases, &facts.ServePrices},
	} {
		price, quoteErr := placementQuote(resolved, input.UsageMeteringEnabled, request.verb, request.basis)
		if quoteErr != nil && !errors.Is(quoteErr, ErrPlacementQuoteUnavailable) {
			return nil, quoteErr
		}
		*request.output = price
		for _, basis := range request.bases {
			quote, basisErr := placementQuote(resolved, input.UsageMeteringEnabled, request.verb, basis)
			if basisErr != nil && !errors.Is(basisErr, ErrPlacementQuoteUnavailable) {
				return nil, basisErr
			}
			if quote != nil {
				*request.quotes = append(*request.quotes, quote)
			}
		}
	}
	if err = placement.ValidateCommercialPrices(facts); err != nil {
		return nil, err
	}
	// JSON map keys are canonical; decimal values retain exact decimal strings.
	encoded, err := json.Marshal(struct {
		Resolved                *ClusterPricing
		UsageMeteringEnabled    *bool
		SourceRevision          string
		Ingest, Serve           *PlacementQuoteBasis
		IngestBases, ServeBases []*PlacementQuoteBasis
	}{resolved, input.UsageMeteringEnabled, input.SourceRevision, input.Ingest, input.Serve, ingestBases, serveBases})
	if err != nil {
		return nil, fmt.Errorf("encode placement commercial snapshot: %w", err)
	}
	hash := sha256.Sum256(append([]byte("frameworks/placement/commercial/v1\x00"), encoded...))
	facts.Revision = hex.EncodeToString(hash[:])
	return facts, nil
}

func canonicalPlacementBases(single *PlacementQuoteBasis, bases []*PlacementQuoteBasis) ([]*PlacementQuoteBasis, error) {
	count := len(bases)
	if single != nil {
		count++
	}
	if count > placement.MaxPricesPerVerb {
		return nil, fmt.Errorf("too many placement comparison bases")
	}
	type encodedBasis struct {
		basis *PlacementQuoteBasis
		key   string
	}
	var encoded []encodedBasis
	seen := make(map[string]bool, count)
	consumed := make(map[rating.Meter]decimal.Decimal)
	check := func(basis *PlacementQuoteBasis) error {
		data, err := json.Marshal(basis.Additional)
		if err != nil {
			return err
		}
		if seen[string(data)] {
			return fmt.Errorf("duplicate placement comparison basis")
		}
		seen[string(data)] = true
		for meter, quantity := range basis.Consumed {
			if previous, ok := consumed[meter]; ok && !previous.Equal(quantity) {
				return fmt.Errorf("placement comparison bases disagree on observed usage")
			}
			consumed[meter] = quantity
		}
		return nil
	}
	if single != nil {
		if err := check(single); err != nil {
			return nil, err
		}
	}
	for _, basis := range bases {
		if basis == nil || !placementBasisBounded(basis) {
			return nil, fmt.Errorf("invalid or unbounded placement comparison basis")
		}
		if err := check(basis); err != nil {
			return nil, err
		}
		data, err := json.Marshal(basis)
		if err != nil {
			return nil, fmt.Errorf("encode placement comparison basis: %w", err)
		}
		encoded = append(encoded, encodedBasis{basis, string(data)})
	}
	slices.SortFunc(encoded, func(a, b encodedBasis) int { return strings.Compare(a.key, b.key) })
	var sorted []*PlacementQuoteBasis
	for _, entry := range encoded {
		sorted = append(sorted, entry.basis)
	}
	return sorted, nil
}

func placementCharging(resolved *ClusterPricing) (placementpb.Charging, error) {
	if resolved.Kind != KindTenantPrivate && resolved.Kind != KindPlatformOfficial && resolved.Kind != KindThirdPartyMarketplace {
		return 0, fmt.Errorf("unknown tenant-relative cluster kind")
	}
	if resolved.Kind == KindTenantPrivate && (resolved.Model != ModelFreeUnmetered || resolved.PricingSource != SourceSelfHosted) {
		return 0, fmt.Errorf("inconsistent self-hosted tariff")
	}
	switch resolved.Model {
	case ModelFreeUnmetered:
		if resolved.PricingSource == SourceSelfHosted && resolved.Kind == KindTenantPrivate || resolved.PricingSource == SourceFreeUnmetered && resolved.Kind != KindTenantPrivate {
			return placementpb.Charging_CHARGING_PERMANENTLY_FREE, nil
		}
	case ModelTierInherit:
		if resolved.PricingSource == SourceTier || resolved.PricingSource == SourceBetaFree {
			return placementpb.Charging_CHARGING_RATED, nil
		}
	case ModelMonthly:
		if resolved.PricingSource == SourceIncludedSubscription || resolved.PricingSource == SourceClusterMonthly {
			return placementpb.Charging_CHARGING_RATED, nil
		}
	case ModelMetered:
		if resolved.PricingSource == SourceClusterMetered || resolved.PricingSource == SourceBetaFree {
			return placementpb.Charging_CHARGING_RATED, nil
		}
	case ModelCustom:
		if resolved.PricingSource == SourceClusterCustom || resolved.PricingSource == SourceBetaFree {
			return placementpb.Charging_CHARGING_RATED, nil
		}
	}
	return 0, fmt.Errorf("unknown or inconsistent cluster pricing model/source")
}

func placementQuote(resolved *ClusterPricing, metering *bool, verb placementpb.Verb, basis *PlacementQuoteBasis) (*placementpb.Price, error) {
	if basis == nil || metering == nil {
		return nil, ErrPlacementQuoteUnavailable
	}
	meters := []rating.Meter{rating.MeterIngressGB}
	if verb == placementpb.Verb_VERB_SERVE {
		meters = []rating.Meter{rating.MeterDeliveredMinutes, rating.MeterEgressGB}
	}
	if len(basis.Additional) != len(meters) || len(basis.Consumed) > len(meters) {
		return nil, ErrPlacementQuoteUnavailable
	}
	known := map[rating.Meter]bool{}
	positive := false
	for _, meter := range meters {
		known[meter] = true
		quantity, present := basis.Additional[meter]
		if !present || quantity.IsNegative() || !placementDecimalBounded(quantity) {
			return nil, ErrPlacementQuoteUnavailable
		}
		positive = positive || quantity.IsPositive()
	}
	if !positive {
		return nil, ErrPlacementQuoteUnavailable
	}
	for meter, consumed := range basis.Consumed {
		if !known[meter] || consumed.IsNegative() || !placementDecimalBounded(consumed) {
			return nil, ErrPlacementQuoteUnavailable
		}
	}
	unit := placementBasisUnit(verb, basis.Additional)
	if len(unit) > 64 {
		return nil, ErrPlacementQuoteUnavailable
	}
	price := &placementpb.Price{Currency: resolved.Currency, Unit: unit}
	// Fixed access fees are already paid for entitled monthly capacity. They are
	// still rated commercial access, but not an incremental media usage charge.
	if !*metering || resolved.Model == ModelFreeUnmetered || resolved.Model == ModelMonthly {
		return price, nil
	}
	var rules []rating.Rule
	seen := map[rating.Meter]bool{}
	for _, rule := range resolved.MeteredRules {
		if seen[rule.Meter] {
			return nil, fmt.Errorf("duplicate placement tariff meter")
		}
		seen[rule.Meter] = true
		switch rule.Meter {
		case rating.MeterStorageGBSecondsHot, rating.MeterStorageGBSecondsCld, rating.MeterMediaSeconds:
			continue
		case rating.MeterIngressGB, rating.MeterDeliveredMinutes, rating.MeterEgressGB:
		default:
			// A custom meter's role in ingest/viewing cannot be inferred from its name.
			return nil, ErrPlacementQuoteUnavailable
		}
		if !known[rule.Meter] {
			continue
		}
		if rule.Model != rating.ModelAllUsage && rule.Model != rating.ModelTieredGraduated {
			return nil, ErrPlacementQuoteUnavailable
		}
		divisor, divisorErr := decimalField(rule.Config, "rated_quantity_divisor")
		if divisorErr != nil || !placementDecimalBounded(divisor) {
			return nil, ErrPlacementQuoteUnavailable
		}
		if divisor.IsPositive() {
			for _, quantity := range []decimal.Decimal{basis.Consumed[rule.Meter], basis.Consumed[rule.Meter].Add(basis.Additional[rule.Meter])} {
				// Do not turn a rounded unit conversion into an exact comparison quote.
				if !quantity.Div(divisor).Mul(divisor).Equal(quantity) {
					return nil, ErrPlacementQuoteUnavailable
				}
			}
		}
		if rule.Model == rating.ModelTieredGraduated && rule.IncludedQuantity.IsPositive() && rule.UnitPrice.IsPositive() && basis.Additional[rule.Meter].IsPositive() {
			if _, observed := basis.Consumed[rule.Meter]; !observed {
				return nil, ErrPlacementQuoteUnavailable
			}
		}
		rules = append(rules, rule)
	}
	before, after := map[rating.Meter]decimal.Decimal{}, map[rating.Meter]decimal.Decimal{}
	for _, meter := range meters {
		before[meter] = basis.Consumed[meter]
		after[meter] = before[meter].Add(basis.Additional[meter])
	}
	old, err := rating.Rate(rating.Input{Currency: resolved.Currency, Rules: rules, Usage: before})
	if err != nil {
		return nil, err
	}
	next, err := rating.Rate(rating.Input{Currency: resolved.Currency, Rules: rules, Usage: after})
	if err != nil {
		return nil, err
	}
	micros := next.GrossUsageAmount.Sub(old.GrossUsageAmount).Mul(decimal.NewFromInt(1_000_000))
	if micros.IsNegative() || !micros.Equal(micros.Truncate(0)) {
		return nil, ErrPlacementQuoteUnavailable
	}
	integer := micros.BigInt()
	if integer.Sign() < 0 || integer.Cmp(new(big.Int).SetUint64(^uint64(0))) > 0 {
		return nil, ErrPlacementQuoteUnavailable
	}
	price.AmountMicros = integer.Uint64()
	return price, nil
}

func placementPriceIdentifier(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func placementDecimalBounded(value decimal.Decimal) bool {
	return value.Exponent() >= -30 && value.Exponent() <= 30 && value.Coefficient().BitLen() <= 256
}

func placementBasisBounded(basis *PlacementQuoteBasis) bool {
	if basis == nil {
		return true
	}
	if len(basis.Additional) > 128 || len(basis.Consumed) > 128 {
		return false
	}
	for _, quantities := range []map[rating.Meter]decimal.Decimal{basis.Additional, basis.Consumed} {
		for meter, value := range quantities {
			if !rating.ValidMeter(meter) || !placementDecimalBounded(value) {
				return false
			}
		}
	}
	return true
}

// ParsePlacementQuoteBasis reconstructs an explicit consumption bundle from a
// persisted policy price unit. No observed allowance consumption is implied.
func ParsePlacementQuoteBasis(verb placementpb.Verb, unit string) (*PlacementQuoteBasis, error) {
	if !placementPriceIdentifier(unit, 64) {
		return nil, ErrPlacementQuoteUnavailable
	}
	quantities := map[rating.Meter]string{}
	switch verb {
	case placementpb.Verb_VERB_INGEST:
		value, ok := strings.CutPrefix(unit, "ingest:gib=")
		if !ok {
			return nil, ErrPlacementQuoteUnavailable
		}
		quantities[rating.MeterIngressGB] = value
	case placementpb.Verb_VERB_SERVE:
		value, ok := strings.CutPrefix(unit, "serve:minutes=")
		if !ok {
			return nil, ErrPlacementQuoteUnavailable
		}
		minutes, gib, ok := strings.Cut(value, ";gib=")
		if !ok {
			return nil, ErrPlacementQuoteUnavailable
		}
		quantities[rating.MeterDeliveredMinutes], quantities[rating.MeterEgressGB] = minutes, gib
	default:
		return nil, ErrPlacementQuoteUnavailable
	}
	basis := &PlacementQuoteBasis{Additional: map[rating.Meter]decimal.Decimal{}}
	positive := false
	for meter, text := range quantities {
		quantity, err := decimal.NewFromString(text)
		if err != nil || !placementDecimalBounded(quantity) || quantity.IsNegative() {
			return nil, ErrPlacementQuoteUnavailable
		}
		basis.Additional[meter] = quantity
		positive = positive || quantity.IsPositive()
	}
	if !positive || placementBasisUnit(verb, basis.Additional) != unit {
		return nil, ErrPlacementQuoteUnavailable
	}
	return basis, nil
}

func placementBasisUnit(verb placementpb.Verb, quantities map[rating.Meter]decimal.Decimal) string {
	if verb == placementpb.Verb_VERB_INGEST {
		return "ingest:gib=" + quantities[rating.MeterIngressGB].String()
	}
	return "serve:minutes=" + quantities[rating.MeterDeliveredMinutes].String() + ";gib=" + quantities[rating.MeterEgressGB].String()
}
