package handlers

import (
	"encoding/json"

	"frameworks/api_billing/internal/appconfig"
	billingtax "frameworks/api_billing/internal/tax"
)

type DocumentSupplier = billingtax.Supplier

// ConfiguredDocumentSupplier is the supplier identity Purser is configured
// with.
func ConfiguredDocumentSupplier() DocumentSupplier {
	rt := appconfig.Runtime()
	return DocumentSupplier{
		Name: rt.SupplierName, Address: rt.SupplierAddress,
		VATNumber: rt.SupplierVATNumber, RegistrationNumber: rt.SupplierRegistrationNumber,
	}
}

// DocumentSupplierSnapshot is the configured supplier identity as the JSON a
// billing document records when it is issued. It is empty, and the document
// records no supplier, while the identity is incomplete: such a document
// states the supplier configured when it is rendered.
func DocumentSupplierSnapshot() string {
	supplier := ConfiguredDocumentSupplier()
	if !supplier.Complete() {
		return ""
	}
	encoded, err := json.Marshal(supplier)
	if err != nil {
		return ""
	}
	return string(encoded)
}
