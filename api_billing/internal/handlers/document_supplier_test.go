package handlers

import "testing"

func TestDocumentSupplierCompleteRequiresEveryLegalField(t *testing.T) {
	complete := DocumentSupplier{Name: "DDVTech B.V.", Address: "Leiden NL", VATNumber: "NL853319145B01", RegistrationNumber: "59104694"}
	if !complete.Complete() {
		t.Fatal("complete legal identity rejected")
	}
	if err := validateCryptoAdvanceSupplier(CryptoBillingProfile{TaxAssessment: &CryptoTaxAssessment{Supplier: complete}}); err != nil {
		t.Fatalf("complete crypto supplier rejected: %v", err)
	}
	for _, field := range []string{"name", "address", "vat", "registration"} {
		for _, value := range []string{"", " \t\n "} {
			t.Run(field+value, func(t *testing.T) {
				incomplete := complete
				switch field {
				case "name":
					incomplete.Name = value
				case "address":
					incomplete.Address = value
				case "vat":
					incomplete.VATNumber = value
				case "registration":
					incomplete.RegistrationNumber = value
				}
				if incomplete.Complete() {
					t.Fatal("incomplete retained legal identity accepted")
				}
				if err := validateCryptoAdvanceSupplier(CryptoBillingProfile{TaxAssessment: &CryptoTaxAssessment{Supplier: incomplete}}); err == nil {
					t.Fatal("crypto payment instructions accepted an incomplete legal supplier")
				}
			})
		}
	}
}
