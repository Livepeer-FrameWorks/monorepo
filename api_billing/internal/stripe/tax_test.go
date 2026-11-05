package stripe

import (
	"context"
	api "github.com/stripe/stripe-go/v85"
	"net/http"
	"testing"
)

type taxBackend struct {
	fakeBackend
	t               *testing.T
	owner           string
	profile         TaxProfile
	deleted         []string
	createdVAT      string
	updatedCustomer *api.CustomerParams
	updatedSub      *api.SubscriptionParams
	currentVAT      string
	inclusive       bool
	currentSub      *api.Subscription
	listedItems     []*api.SubscriptionItem
	itemListCalls   int
}

func (b *taxBackend) CallRaw(method, path, key string, body []byte, params *api.Params, out api.LastResponseSetter) error {
	if list, ok := out.(*api.SubscriptionItemList); ok {
		b.itemListCalls++
		list.Data = b.listedItems
		return nil
	}
	return b.fakeBackend.CallRaw(method, path, key, body, params, out)
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
