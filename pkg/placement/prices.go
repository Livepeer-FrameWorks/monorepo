package placement

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

// MaxPricesPerVerb bounds distinct comparison bases to the policy group limit.
const MaxPricesPerVerb = maxGroups

func validPriceBasis(currency, unit string) bool {
	return len(currency) == 3 && strings.IndexFunc(currency, func(r rune) bool { return r < 'A' || r > 'Z' }) < 0 &&
		unit != "" && len(unit) <= 64 && strings.TrimSpace(unit) == unit && strings.IndexFunc(unit, unicode.IsControl) < 0
}

// ValidateCommercialPrices checks both verbs, including the singular compatibility
// slots. Duplicate bases are ambiguous even when their amounts happen to agree.
// Revision, classification and lifetime validation belong to the enclosing facts.
func ValidateCommercialPrices(facts *placementpb.CommercialFacts) error {
	if facts == nil {
		return nil
	}
	if err := rejectUnknownWire(facts); err != nil {
		return err
	}
	for _, quotes := range []struct {
		one  *placementpb.Price
		many []*placementpb.Price
	}{{facts.GetIngestPrice(), facts.GetIngestPrices()}, {facts.GetServePrice(), facts.GetServePrices()}} {
		count := len(quotes.many)
		if quotes.one != nil {
			count++
		}
		if count > MaxPricesPerVerb {
			return fmt.Errorf("too many placement price bases")
		}
		seen := make(map[[2]string]bool, count)
		check := func(price *placementpb.Price) error {
			if price == nil || !validPriceBasis(price.GetCurrency(), price.GetUnit()) {
				return fmt.Errorf("invalid placement price basis")
			}
			key := [2]string{price.GetCurrency(), price.GetUnit()}
			if seen[key] {
				return fmt.Errorf("duplicate placement price basis")
			}
			seen[key] = true
			if facts.GetCharging() == placementpb.Charging_CHARGING_PERMANENTLY_FREE && price.GetAmountMicros() != 0 {
				return fmt.Errorf("permanently free capacity cannot carry a positive price")
			}
			return nil
		}
		if quotes.one != nil {
			if err := check(quotes.one); err != nil {
				return err
			}
		}
		for _, price := range quotes.many {
			if err := check(price); err != nil {
				return err
			}
		}
	}
	return nil
}

func comparablePrice(candidate Candidate, group Group, now time.Time) *Price {
	count := len(candidate.Prices)
	if candidate.Price != nil {
		count++
	}
	if count > MaxPricesPerVerb {
		return nil
	}
	seen := make(map[[2]string]bool, count)
	var selected *Price
	check := func(price *Price) bool {
		key := [2]string{price.Currency, price.Unit}
		if seen[key] || !validPriceBasis(price.Currency, price.Unit) || candidate.Charging == PermanentlyFree && price.AmountMicros != 0 {
			return false
		}
		seen[key] = true
		if price.Currency == group.PriceCurrency && price.Unit == group.PriceUnit {
			if price.Revision == "" || !price.ExpiresAt.After(now) {
				return false
			}
			selected = price
		}
		return true
	}
	if candidate.Price != nil && !check(candidate.Price) {
		return nil
	}
	for i := range candidate.Prices {
		if !check(&candidate.Prices[i]) {
			return nil
		}
	}
	return selected
}
