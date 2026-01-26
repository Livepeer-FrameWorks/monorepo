package billing

import "frameworks/pkg/config"

const (
	defaultCurrencyEnv      = "BILLING_CURRENCY"
	defaultCurrencyFallback = "EUR"
)

// DefaultCurrency returns the billing ledger currency used when no currency is specified.
func DefaultCurrency() string {
	return config.GetEnv(defaultCurrencyEnv, defaultCurrencyFallback)
}
