package billing

import "strings"

// Presentment currencies a tenant can be invoiced and charged in. Amounts are
// converted to LedgerCurrency before they reach the ledger.
const (
	PresentmentEUR = "EUR"
	PresentmentUSD = "USD"
	PresentmentGBP = "GBP"
)

// eurPresentmentCountries are the EU member states plus Iceland,
// Liechtenstein, and Norway. The v0.3.11 Purser postdeploy migration that
// derives existing tenants' presentment currency lists the same codes.
var eurPresentmentCountries = map[string]struct{}{
	"AT": {}, "BE": {}, "BG": {}, "CY": {}, "CZ": {}, "DE": {}, "DK": {}, "EE": {}, "ES": {}, "FI": {},
	"FR": {}, "GR": {}, "HR": {}, "HU": {}, "IE": {}, "IT": {}, "LT": {}, "LU": {}, "LV": {}, "MT": {},
	"NL": {}, "PL": {}, "PT": {}, "RO": {}, "SE": {}, "SI": {}, "SK": {},
	"IS": {}, "LI": {}, "NO": {},
}

// PresentmentCurrencyForCountry returns the presentment currency for an ISO
// 3166-1 alpha-2 billing country: EUR for the EEA, GBP for the United Kingdom,
// and USD for every other or unknown country.
func PresentmentCurrencyForCountry(country string) string {
	code := strings.ToUpper(strings.TrimSpace(country))
	if _, ok := eurPresentmentCountries[code]; ok {
		return PresentmentEUR
	}
	if code == "GB" {
		return PresentmentGBP
	}
	return PresentmentUSD
}
