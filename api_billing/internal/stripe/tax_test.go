package stripe

import (
	"context"
	"encoding/json"
	"fmt"
	api "github.com/stripe/stripe-go/v85"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestTaxProfileIdentityTracksTermsWithoutEvidenceChurn(t *testing.T) {
	p := assessedTaxProfile()
	p.Evidence = json.RawMessage(`{"assessed_at":"2026-11-05T00:00:00Z","vat_validation":{"checked_at":"2026-11-05","raw_response":"first"},"decision":{"checked_on":"2026-11-05","ip_country":"NL"}}`)
	first := p.Fingerprint()
	p.Evidence = json.RawMessage(`{"assessed_at":"2026-11-06T00:00:00Z","vat_validation":{"checked_at":"2026-11-06","raw_response":"second"},"decision":{"checked_on":"2026-11-06","ip_country":"BE"}}`)
	if p.Fingerprint() != first {
		t.Fatal("daily evidence changed retained legal/rate identity")
	}
	if string(p.Evidence) == "" {
		t.Fatal("fingerprinting removed immutable proof payload")
	}
	p.RateBPS = 0
	if p.Fingerprint() == first {
		t.Fatal("changed charged VAT rate reused old terms")
	}
}

func TestSubscriptionTaxWireOmitsUnchangedItemsAndKeepsExplicitOverrides(t *testing.T) {
	for _, operation := range []string{"sync", "hold"} {
		for _, override := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/override=%t", operation, override), func(t *testing.T) {
				item := &api.SubscriptionItem{ID: "item"}
				if override {
					item.TaxRates = []*api.TaxRate{{ID: "stale_rate"}}
				}
				current := &api.Subscription{ID: "sub_owned", Customer: &api.Customer{ID: "cus_owned"}, Metadata: map[string]string{"tenant_id": "tenant-owned"}, Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{item}}}
				b := &taxBackend{t: t, owner: "tenant-owned", currentSub: current}
				installBackend(t, b)
				var err error
				if operation == "sync" {
					err = (&Client{}).SyncSubscriptionTax(t.Context(), "tenant-owned", "cus_owned", "sub_owned", assessedTaxProfile())
				} else {
					err = (&Client{}).HoldSubscriptionTaxReview(t.Context(), "tenant-owned", "cus_owned", "sub_owned")
				}
				if err != nil {
					t.Fatal(err)
				}
				if b.updatedSub == nil {
					t.Fatal("expected a future-only tax update")
				}
				var wire url.Values
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if err := r.ParseForm(); err != nil {
						t.Error(err)
					}
					wire = r.PostForm
					w.Header().Set("Content-Type", "application/json")
					if _, err := w.Write([]byte(`{"id":"sub_owned"}`)); err != nil {
						t.Error(err)
					}
				}))
				t.Cleanup(server.Close)
				backend := api.GetBackendWithConfig(api.APIBackend, &api.BackendConfig{URL: api.String(server.URL), MaxNetworkRetries: api.Int64(0)})
				if err := backend.Call(http.MethodPost, "/v1/subscriptions/sub_owned", "sk_test", b.updatedSub, &api.Subscription{}); err != nil {
					t.Fatal(err)
				}
				if values := wire["items"]; len(values) > 0 {
					t.Fatalf("unchanged items were sent as an array-clear request: %s", wire.Encode())
				}
				if override {
					if values := wire["items[0][id]"]; len(values) != 1 || values[0] != "item" {
						t.Fatalf("explicit item identity missing from wire: %s", wire.Encode())
					}
					values := wire["items[0][tax_rates][0]"]
					if operation == "sync" && (len(values) != 1 || values[0] != "txr_exclusive") {
						t.Fatalf("explicit tax override missing from wire: %s", wire.Encode())
					}
					if operation == "hold" && len(wire["items[0][tax_rates]"]) != 1 {
						t.Fatalf("explicit tax override was not cleared: %s", wire.Encode())
					}
				}
			})
		}
	}
}

type taxBackend struct {
	fakeBackend
	t                *testing.T
	owner            string
	profile          TaxProfile
	deleted          []string
	createdVAT       string
	updatedCustomer  *api.CustomerParams
	updatedSub       *api.SubscriptionParams
	subUpdates       []*api.SubscriptionParams
	currentVAT       string
	inclusive        bool
	currentSub       *api.Subscription
	listedItems      []*api.SubscriptionItem
	itemListCalls    int
	invoiceListCalls int
	drafts           []*api.Invoice
}

func (b *taxBackend) CallRaw(method, path, key string, body []byte, params *api.Params, out api.LastResponseSetter) error {
	if list, ok := out.(*api.InvoiceList); ok {
		b.invoiceListCalls++
		list.Data = b.drafts
		return nil
	}
	if list, ok := out.(*api.SubscriptionItemList); ok {
		b.itemListCalls++
		list.Data = b.listedItems
		return nil
	}
	return b.fakeBackend.CallRaw(method, path, key, body, params, out)
}

func TestUnsupportedOwnedTaxInvoiceKeepsReviewAfterFutureRenewalsResume(t *testing.T) {
	p := assessedTaxProfile()
	current, unsupported := heldTaxFixture()
	unsupported.Lines.Data[0].Amount = -100
	b := &taxBackend{t: t, owner: "tenant", currentSub: current, drafts: []*api.Invoice{unsupported}}
	installBackend(t, b)
	if err := (&Client{}).ReconcileSubscriptionTax(t.Context(), "tenant", "customer", "sub", p); err == nil {
		t.Fatal("held renewal debt silently cleared from review after profile recovery")
	}
	if b.updatedSub == nil || len(b.updatedSub.UnsetFields) != 1 || b.updatedSub.UnsetFields[0] != api.SubscriptionParamsUnsetFieldPauseCollection {
		t.Fatal("past invoice review kept future tax-correct renewals paused")
	}
}

func TestStripeTaxSyncIncludesPaginatedItemOverrides(t *testing.T) {
	p := assessedTaxProfile()
	p.Country, p.VATNumber, p.RateBPS, p.ReverseCharge = "DE", "DE123456789", 0, true
	current := &api.Subscription{ID: "sub_owned", Customer: &api.Customer{ID: "cus_owned"}, Metadata: map[string]string{"tenant_id": "tenant-owned", "frameworks_tax_profile": p.Fingerprint()}, DefaultTaxRates: []*api.TaxRate{{ID: "txr_exclusive"}}, AutomaticTax: &api.SubscriptionAutomaticTax{Enabled: false}, Items: &api.SubscriptionItemList{ListMeta: api.ListMeta{HasMore: true}, Data: []*api.SubscriptionItem{{ID: "si_inherited"}}}}
	b := &taxBackend{t: t, owner: "tenant-owned", currentSub: current, listedItems: []*api.SubscriptionItem{{ID: "si_inherited"}, {ID: "si_later", TaxRates: []*api.TaxRate{{ID: "txr_old_21"}}}}}
	installBackend(t, b)
	if err := (&Client{}).SyncSubscriptionTax(context.Background(), "tenant-owned", "cus_owned", "sub_owned", p); err != nil {
		t.Fatal(err)
	}
	if b.itemListCalls != 1 || b.updatedSub == nil || len(b.updatedSub.Items) != 1 || *b.updatedSub.Items[0].ID != "si_later" {
		t.Fatal("override beyond embedded item page survived tax synchronization")
	}
}

func assessedTaxProfile() TaxProfile {
	return TaxProfile{Name: "Director", Company: "Buyer B.V.", Email: "billing@example.test", Street: "Street 1", City: "Leiden", PostalCode: "2332ED", Country: "NL", VATNumber: "NL853319145B01", RateBPS: 2100, SupplierCountry: "NL", SupplierIdentity: "DDVTech B.V.; VAT NL853319145B01; Registration 59104694"}
}

func TestStripeTaxSyncUsesLegalProfileAndFutureExclusiveRates(t *testing.T) {
	p := assessedTaxProfile()
	b := &taxBackend{t: t, owner: "tenant-owned", currentVAT: "DE123456789"}
	installBackend(t, b)
	c := &Client{}
	if err := c.SyncSubscriptionTax(context.Background(), "tenant-owned", "cus_owned", "sub_owned", p); err != nil {
		t.Fatal(err)
	}
	if b.updatedCustomer == nil || *b.updatedCustomer.Name != "Buyer B.V." || *b.updatedCustomer.Address.Country != "NL" || *b.updatedCustomer.TaxExempt != "none" {
		t.Fatal("legal customer profile not synchronized")
	}
	if b.createdVAT != p.VATNumber || len(b.deleted) != 1 {
		t.Fatal("domestic EU VAT identity did not replace obsolete VAT number")
	}
	if b.updatedSub == nil || *b.updatedSub.ProrationBehavior != "none" || b.updatedSub.AutomaticTax == nil || *b.updatedSub.AutomaticTax.Enabled || len(b.updatedSub.DefaultTaxRates) != 1 {
		t.Fatal("subscription change must affect future exclusive-tax invoices without proration")
	}
	if b.updatedSub.Metadata["frameworks_tax_profile"] != p.Fingerprint() {
		t.Fatal("subscription lost retained tax proof reference")
	}
	p.VATNumber = ""
	b.createdVAT = ""
	b.deleted = nil
	if _, err := c.PrepareCustomerTax(context.Background(), "tenant-owned", "cus_owned", p); err != nil {
		t.Fatal(err)
	}
	if len(b.deleted) != 1 || b.createdVAT != "" {
		t.Fatal("removed VAT identity remains on provider customer")
	}
}

func TestStripeTaxRefusesForeignTenantAndInclusiveProviderRate(t *testing.T) {
	b := &taxBackend{t: t, owner: "other-tenant"}
	installBackend(t, b)
	c := &Client{}
	p := assessedTaxProfile()
	if _, err := c.PrepareCustomerTax(context.Background(), "tenant-owned", "cus_owned", p); err == nil {
		t.Fatal("cross-tenant Stripe customer mutated")
	}
	if b.calls != 1 || b.updatedCustomer != nil {
		t.Fatal("owner mismatch made provider writes")
	}
	b.owner = "tenant-owned"
	b.inclusive = true
	if _, err := c.PrepareCustomerTax(context.Background(), "tenant-owned", "cus_owned", p); err == nil {
		t.Fatal("provider inclusive rate accepted for exclusive prices")
	}
}
func (b *taxBackend) Call(method, path, key string, params api.ParamsContainer, out api.LastResponseSetter) error {
	b.calls++
	switch v := out.(type) {
	case *api.Customer:
		if method == http.MethodGet {
			*v = api.Customer{ID: "cus_owned", Metadata: map[string]string{"tenant_id": b.owner}, TaxIDs: &api.TaxIDList{Data: []*api.TaxID{{ID: "txi_old", Type: api.TaxIDTypeEUVAT, Value: b.currentVAT}}}}
		} else {
			b.updatedCustomer = params.(*api.CustomerParams)
		}
	case *api.TaxID:
		if method == http.MethodDelete {
			b.deleted = append(b.deleted, path)
		} else {
			b.createdVAT = *params.(*api.TaxIDParams).Value
		}
	case *api.TaxRate:
		p := params.(*api.TaxRateParams)
		if p.Inclusive == nil || *p.Inclusive {
			b.t.Fatal("created inclusive tax rate")
		}
		*v = api.TaxRate{ID: "txr_exclusive", Country: *p.Country, Percentage: *p.Percentage, Inclusive: b.inclusive}
	case *api.Subscription:
		if method == http.MethodGet {
			*v = api.Subscription{ID: "sub_owned", Customer: &api.Customer{ID: "cus_owned"}, Metadata: map[string]string{"tenant_id": b.owner}}
			if b.currentSub != nil {
				*v = *b.currentSub
			}
		} else {
			b.updatedSub = params.(*api.SubscriptionParams)
			b.subUpdates = append(b.subUpdates, b.updatedSub)
		}
	}
	return nil
}

func TestStripeTaxSyncReconcilesItemOverridesBeforeNoOp(t *testing.T) {
	p := assessedTaxProfile()
	p.Country, p.VATNumber, p.RateBPS, p.ReverseCharge = "DE", "DE123456789", 0, true
	p.Wording = "btw verlegd"
	for _, tc := range []struct {
		name, defaultRate, profile string
		itemRates                  []string
		wantUpdate                 bool
	}{
		{"default and profile already zero, item still21", "txr_exclusive", p.Fingerprint(), []string{"txr_old_21"}, true},
		{"profile changes21 to reverse charge0", "txr_old_21", "old-profile", []string{"txr_old_21"}, true},
		{"matching explicit item is unchanged", "txr_exclusive", p.Fingerprint(), []string{"txr_exclusive"}, false},
		{"inherited matching default is unchanged", "txr_exclusive", p.Fingerprint(), nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := &api.SubscriptionItem{ID: "si_existing", Subscription: "sub_owned"}
			for _, rate := range tc.itemRates {
				item.TaxRates = append(item.TaxRates, &api.TaxRate{ID: rate})
			}
			current := &api.Subscription{ID: "sub_owned", Customer: &api.Customer{ID: "cus_owned"}, Metadata: map[string]string{"tenant_id": "tenant-owned", "frameworks_tax_profile": tc.profile}, DefaultTaxRates: []*api.TaxRate{{ID: tc.defaultRate}}, AutomaticTax: &api.SubscriptionAutomaticTax{Enabled: false}, Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{item}}}
			b := &taxBackend{t: t, owner: "tenant-owned", currentVAT: p.VATNumber, currentSub: current}
			installBackend(t, b)
			if err := (&Client{}).SyncSubscriptionTax(context.Background(), "tenant-owned", "cus_owned", "sub_owned", p); err != nil {
				t.Fatal(err)
			}
			if (b.updatedSub != nil) != tc.wantUpdate {
				t.Fatalf("subscription update=%v want%v despite item rates%v", b.updatedSub != nil, tc.wantUpdate, tc.itemRates)
			}
			if b.updatedCustomer == nil || api.StringValue(b.updatedCustomer.TaxExempt) != "none" {
				t.Fatal("customer-global reverse exemption would suppress original taxed credits; explicit current zero rate must drive reverse charge")
			}
			if !tc.wantUpdate {
				return
			}
			if len(b.updatedSub.Items) != 1 || b.updatedSub.Items[0].ID == nil || *b.updatedSub.Items[0].ID != "si_existing" || len(b.updatedSub.Items[0].TaxRates) != 1 || *b.updatedSub.Items[0].TaxRates[0] != "txr_exclusive" {
				t.Fatal("stale explicit item rate survived tax synchronization")
			}
			if b.updatedSub.Items[0].Price != nil || b.updatedSub.Items[0].Quantity != nil || b.updatedSub.Items[0].Deleted != nil {
				t.Fatal("tax synchronization changed subscription commercial terms")
			}
			if b.updatedSub.ProrationBehavior == nil || *b.updatedSub.ProrationBehavior != "none" || len(b.updatedSub.DefaultTaxRates) != 1 || *b.updatedSub.DefaultTaxRates[0] != "txr_exclusive" {
				t.Fatal("tax sync changed paid terms or omitted future default")
			}
		})
	}
}

func TestTaxReviewPreservesManualPauseAndOnlyResumesOwnedHold(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pause  *api.SubscriptionPauseCollection
		owned  bool
		resume bool
	}{
		{"manual pause", &api.SubscriptionPauseCollection{Behavior: api.SubscriptionPauseCollectionBehaviorVoid}, false, false},
		{"owned hold", &api.SubscriptionPauseCollection{Behavior: api.SubscriptionPauseCollectionBehaviorKeepAsDraft}, true, true},
		{"operator changed owned hold", &api.SubscriptionPauseCollection{Behavior: api.SubscriptionPauseCollectionBehaviorKeepAsDraft, ResumesAt: 12345}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := assessedTaxProfile()
			meta := map[string]string{"tenant_id": "tenant-owned", "frameworks_tax_review": "true"}
			if tc.owned {
				meta["frameworks_tax_pause_owned"] = "true"
			}
			current := &api.Subscription{ID: "sub_owned", Customer: &api.Customer{ID: "cus_owned"}, Metadata: meta, PauseCollection: tc.pause, Items: &api.SubscriptionItemList{Data: []*api.SubscriptionItem{{ID: "item"}}}}
			b := &taxBackend{t: t, owner: "tenant-owned", currentSub: current, currentVAT: p.VATNumber}
			installBackend(t, b)
			if err := (&Client{}).HoldSubscriptionTaxReview(t.Context(), "tenant-owned", "cus_owned", "sub_owned"); err != nil {
				t.Fatal(err)
			}
			if b.updatedSub == nil || b.updatedSub.PauseCollection != nil {
				t.Fatal("tax hold overwrote existing operator pause")
			}
			if tc.owned && tc.resume {
				current.Metadata["frameworks_tax_pause_owned"] = "true"
			} else {
				current.Metadata["frameworks_tax_pause_owned"] = ""
			}
			if err := (&Client{}).SyncSubscriptionTax(t.Context(), "tenant-owned", "cus_owned", "sub_owned", p); err != nil {
				t.Fatal(err)
			}
			resumed := false
			for _, f := range b.updatedSub.UnsetFields {
				if f == api.SubscriptionParamsUnsetFieldPauseCollection {
					resumed = true
				}
			}
			if resumed != tc.resume {
				t.Fatalf("manual/changed pause resumed=%v, want%v", resumed, tc.resume)
			}
		})
	}
}
