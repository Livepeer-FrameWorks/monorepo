package billing

import (
	"fmt"
	"strings"
)

const (
	// ExternalCollectionFloorCents is the economic floor for creating a fiat
	// collection. Smaller postpaid overages remain owed and carry forward.
	ExternalCollectionFloorCents int64 = 500
	// CryptoTopupFloorCents is one cent because prepaid balances are credited
	// in integer cents even though the received asset has finer precision.
	CryptoTopupFloorCents int64 = 1
	// MaximumTopupCents caps a single prepaid funding operation at 100,000
	// currency units across every public surface.
	MaximumTopupCents int64 = 10_000_000
)

// FiatTopupMinimumCents returns the provider-aware minimum accepted by Purser.
// The economic floor currently dominates each provider's technical minimum,
// but keeping both inputs explicit prevents a new provider or currency from
// accidentally inheriting an invalid amount.
func FiatTopupMinimumCents(provider, currency string) (int64, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	currency = strings.ToUpper(strings.TrimSpace(currency))

	var technicalMinimum int64
	switch provider {
	case "stripe":
		switch currency {
		case "EUR", "USD":
			technicalMinimum = 50
		default:
			return 0, fmt.Errorf("unsupported Stripe top-up currency %s (EUR or USD only)", currency)
		}
	case "mollie":
		switch currency {
		case "EUR", "USD":
			technicalMinimum = 1
		default:
			return 0, fmt.Errorf("unsupported Mollie top-up currency %s (EUR or USD only)", currency)
		}
	default:
		return 0, fmt.Errorf("unsupported fiat payment provider %s", provider)
	}

	if technicalMinimum > ExternalCollectionFloorCents {
		return technicalMinimum, nil
	}
	return ExternalCollectionFloorCents, nil
}

// InvoiceCollectionMinimumCents applies the same economic floor to automatic
// provider collection. The provider/currency validation remains shared with
// prepaid fiat top-ups.
func InvoiceCollectionMinimumCents(provider, currency string) (int64, error) {
	return FiatTopupMinimumCents(provider, currency)
}
