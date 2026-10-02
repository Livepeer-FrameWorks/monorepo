package handlers

import (
	"encoding/json"
	"strings"

	"frameworks/api_billing/internal/appconfig"
)

// DocumentSupplier is the supplier identity a billing document states.
type DocumentSupplier struct {
	Name               string `json:"name"`
	Address            string `json:"address"`
	VATNumber          string `json:"vat_number"`
	RegistrationNumber string `json:"registration_number"`
}

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
	for _, value := range []string{supplier.Name, supplier.Address, supplier.VATNumber, supplier.RegistrationNumber} {
		if strings.TrimSpace(value) == "" {
			return ""
		}
	}
	encoded, err := json.Marshal(supplier)
	if err != nil {
		return ""
	}
	return string(encoded)
}
