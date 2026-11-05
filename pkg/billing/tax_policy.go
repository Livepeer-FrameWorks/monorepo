package billing

// Crypto document policy identifiers distinguish retained settlement formats.
const (
	CurrentInvoiceTaxPolicy             = "exclusive-service-invoice-v1"
	LegacyCryptoTopupTaxPolicy          = "vat-at-confirmed-topup-v1"
	FrozenInclusiveCryptoTopupTaxPolicy = "vat-at-confirmed-topup-v2"
	CurrentCryptoTopupTaxPolicy         = "exclusive-crypto-advance-v3"
)

func IsLegacyCryptoTaxPolicy(policy string) bool {
	return policy == "" || policy == LegacyCryptoTopupTaxPolicy
}

func IsSupportedCryptoTaxPolicy(policy string) bool {
	return IsLegacyCryptoTaxPolicy(policy) || policy == FrozenInclusiveCryptoTopupTaxPolicy || policy == CurrentCryptoTopupTaxPolicy
}
