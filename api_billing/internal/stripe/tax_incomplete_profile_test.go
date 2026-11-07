package stripe

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	api "github.com/stripe/stripe-go/v85"
)

type partialFallbackBackend struct {
	taxBackend
	listed bool
}

func (b *partialFallbackBackend) CallRaw(method, path, key string, body []byte, p *api.Params, out api.LastResponseSetter) error {
	if ids, ok := out.(*api.TaxIDList); ok {
		b.listed = true
		ids.Data = []*api.TaxID{{ID: "first", Type: api.TaxIDTypeEUVAT, Value: "NLold"}, {ID: "later", Type: api.TaxIDTypeEUVAT, Value: "DEold"}, {ID: "non-eu", Type: api.TaxIDTypeUSEIN}}
		return nil
	}
	return b.taxBackend.CallRaw(method, path, key, body, p, out)
}
func (b *partialFallbackBackend) Call(method, path, key string, p api.ParamsContainer, out api.LastResponseSetter) error {
	if buyer, ok := out.(*api.Customer); ok && method == http.MethodGet {
		*buyer = api.Customer{ID: "customer", Name: "Old Name", Email: "kept@example.test", Address: &api.Address{Line1: "Old Street", City: "Kept City", PostalCode: "Kept Zip", Country: "AT"}, Metadata: map[string]string{"tenant_id": "tenant"}, TaxIDs: &api.TaxIDList{ListMeta: api.ListMeta{HasMore: true}, Data: []*api.TaxID{{ID: "first", Type: api.TaxIDTypeEUVAT, Value: "NLold"}}}}
		return nil
	}
	return b.taxBackend.Call(method, path, key, p, out)
}
func TestRecurringFallbackSynchronizesKnownIdentityAndRemovesAllEUVAT(t *testing.T) {
	p := assessedTaxProfile()
	p.Name, p.Company, p.Email, p.Street, p.City, p.PostalCode, p.Country, p.VATNumber = "New Name", "", "", "New Street", "", "", "NL", "invalid-raw-claim"
	p.IncompleteProfile = true
	p.Evidence = fallbackEvidence(p.RateBPS)
	b := &partialFallbackBackend{taxBackend: taxBackend{t: t, owner: "tenant"}}
	installBackend(t, b)
	if _, err := (&Client{}).PrepareCustomerTax(t.Context(), "tenant", "customer", p); err != nil {
		t.Fatal(err)
	}
	update := b.updatedCustomer
	if update == nil || api.StringValue(update.Name) != "New Name" || update.Email != nil || update.Address == nil || api.StringValue(update.Address.Line1) != "New Street" || api.StringValue(update.Address.Country) != "NL" || update.Address.City != nil || update.Address.PostalCode != nil {
		t.Fatalf("known partial identity was skipped or absent fields overwritten: %+v", update)
	}
	if !b.listed || len(b.deleted) != 2 || !strings.HasSuffix(b.deleted[0], "/first") || !strings.HasSuffix(b.deleted[1], "/later") || b.createdVAT != "" {
		t.Fatalf("provisional profile retained/re-added EU VAT or removed non-EU ID: listed=%t deleted=%v added=%s", b.listed, b.deleted, b.createdVAT)
	}
}

func fallbackEvidence(rate int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"review_required":true,"supplier_country":"NL","decision":{"Country":"NL","RateBPS":%d,"ReverseCharge":false,"Source":"eu-vat-official","CheckedOn":"2026-11-07","EffectiveFrom":"2026-01-01"}}`, rate))
}

func TestRecurringFallbackUsesAuthoritativeDomesticRate(t *testing.T) {
	p := assessedTaxProfile()
	p.Name, p.RateBPS, p.IncompleteProfile = "", 2200, true
	p.Company, p.Email, p.Street, p.City, p.PostalCode, p.Country, p.VATNumber = "", "", "", "", "", "", ""
	p.Evidence = fallbackEvidence(p.RateBPS)
	b := &incompleteTaxBackend{taxBackend: taxBackend{t: t, owner: "tenant"}}
	installBackend(t, b)
	if _, err := (&Client{}).PrepareCustomerTax(t.Context(), "tenant", "customer", p); err != nil {
		t.Fatalf("authoritative changed domestic VAT rate rejected: %v", err)
	}
}

func TestRecurringFallbackRequiresMatchingPositiveAuthoritativeProof(t *testing.T) {
	for _, kind := range []string{"zero", "reverse", "mismatched-rate", "foreign-proof", "missing-source", "malformed-date"} {
		t.Run(kind, func(t *testing.T) {
			p := assessedTaxProfile()
			p.IncompleteProfile = true
			p.Evidence = fallbackEvidence(p.RateBPS)
			switch kind {
			case "zero":
				p.RateBPS = 0
			case "reverse":
				p.ReverseCharge = true
			case "mismatched-rate":
				p.RateBPS = 2300
			case "foreign-proof":
				p.SupplierCountry = "FR"
			case "missing-source":
				p.Evidence = json.RawMessage(strings.ReplaceAll(string(p.Evidence), "eu-vat-official", ""))
			case "malformed-date":
				p.Evidence = json.RawMessage(strings.ReplaceAll(string(p.Evidence), "2026-11-07", "not-a-date"))
			}
			if p.recurringTaxFallback() {
				t.Fatal("unproven fallback terms bypassed strict party validation")
			}
		})
	}
}

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
	p.Evidence = fallbackEvidence(p.RateBPS)
	b := &incompleteTaxBackend{taxBackend: taxBackend{t: t, owner: "tenant", currentSub: &api.Subscription{ID: "sub", Customer: &api.Customer{ID: "customer"}, Metadata: map[string]string{"tenant_id": "tenant"}, DefaultTaxRates: []*api.TaxRate{{ID: "old-zero"}}}}}
	installBackend(t, b)
	if err := (&Client{}).SyncSubscriptionTax(t.Context(), "tenant", "customer", "sub", p); err != nil {
		t.Fatalf("incomplete recurring fallback could not synchronize Dutch VAT: %v", err)
	}
	if b.updatedSub == nil || len(b.updatedSub.DefaultTaxRates) != 1 || b.updatedCustomer == nil || len(b.deleted) != 1 || b.createdVAT != "" || b.updatedSub.PauseCollection != nil {
		t.Fatal("fallback did not charge explicit VAT while preserving known customer data and collection")
	}
}

func TestIncompleteFallbackValidatesVATMoneyWithoutClaimingCompleteParties(t *testing.T) {
	p := assessedTaxProfile()
	p.Name, p.Country, p.VATNumber = "", "", ""
	p.IncompleteProfile = true
	p.Evidence = fallbackEvidence(p.RateBPS)
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
