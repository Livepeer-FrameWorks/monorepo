package stripe

import (
	"errors"
	"net/http"
	"testing"

	api "github.com/stripe/stripe-go/v85"
)

type incompleteTaxBackend struct{ taxBackend }

func (b *incompleteTaxBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	if buyer, ok := out.(*api.Customer); ok {
		if method == http.MethodGet {
			*buyer = api.Customer{ID: "customer", Name: "Known Buyer", Email: "known@example.test", Address: &api.Address{Line1: "Known Street", Country: "NL"}, Metadata: map[string]string{"tenant_id": "tenant"}, TaxExempt: api.CustomerTaxExemptReverse, TaxIDs: &api.TaxIDList{Data: []*api.TaxID{{ID: "prior-vat", Type: api.TaxIDTypeEUVAT, Value: "NL-prior"}}}}
			return nil
		}
		p := params.(*api.CustomerParams)
		if p.Name != nil || p.Email != nil || p.Address != nil || api.StringValue(p.TaxExempt) != "none" {
			return errors.New("incomplete fallback rewrote known provider identity or retained exemption")
		}
		b.updatedCustomer = p
		return nil
	}
	return b.taxBackend.Call(method, path, key, params, out)
}

func TestIncompleteRecurringTaxSyncPreservesProviderIdentityAndChargesVAT(t *testing.T) {
	p := assessedTaxProfile()
	p.Name, p.Company, p.Email, p.Street, p.City, p.PostalCode, p.Country, p.VATNumber = "", "", "", "", "", "", "", ""
	p.IncompleteProfile = true
	b := &incompleteTaxBackend{taxBackend: taxBackend{t: t, owner: "tenant", currentSub: &api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Metadata: map[string]string{"tenant_id": "tenant"}, DefaultTaxRates: []*api.TaxRate{{ID: "old-zero"}}}}}
	installBackend(t, b)
	if err := (&Client{}).SyncSubscriptionTax(t.Context(), "tenant", "customer", "sub", p); err != nil {
		t.Fatalf("incomplete recurring fallback could not synchronize Dutch VAT: %v", err)
	}
	if b.updatedSub == nil || len(b.updatedSub.DefaultTaxRates) != 1 || b.updatedCustomer == nil || len(b.deleted) != 0 || b.createdVAT != "" || b.updatedSub.PauseCollection != nil {
		t.Fatal("fallback did not charge explicit VAT while preserving known customer data and collection")
	}
}

func TestIncompleteFallbackValidatesVATMoneyWithoutClaimingCompleteParties(t *testing.T) {
	p := assessedTaxProfile()
	p.Name, p.Country, p.VATNumber = "", "", ""
	p.IncompleteProfile = true
	net, vat := int64(10000), int64(2100)
	doc := ProviderTaxDocument{ID: "invoice", ProfileSHA: p.Fingerprint(), Net: &net, VAT: &vat, Gross: 12100}
	if err := ValidateProviderTaxTerms(doc, p); err != nil {
		t.Fatalf("marked Dutch fallback rejected correct VAT money because parties need review: %v", err)
	}
	vat = 0
	doc.Gross = 10000
	if err := ValidateProviderTaxTerms(doc, p); err == nil {
		t.Fatal("incomplete fallback accepted an untaxed invoice")
	}
}
