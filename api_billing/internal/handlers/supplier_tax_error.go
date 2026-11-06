package handlers

// Missing local configuration cannot invalidate a provider's issued document.
// New purchases still fail assessment until the operator completes it.
type supplierTaxConfigurationError struct{}

func (*supplierTaxConfigurationError) Error() string {
	return "complete supplier identity required before checkout"
}
