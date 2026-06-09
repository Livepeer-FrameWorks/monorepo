package bootstrap

import "testing"

func validCustomerBilling() CustomerBilling {
	return CustomerBilling{
		Tenant: TenantRef{Ref: "quartermaster.tenants[acme]"},
		Model:  "prepaid",
		Tier:   "payg",
	}
}

func TestValidateCustomerBilling(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*CustomerBilling)
		wantErr bool
	}{
		{"valid prepaid", nil, false},
		{"valid postpaid", func(e *CustomerBilling) { e.Model = "postpaid" }, false},
		{"empty tenant ref", func(e *CustomerBilling) { e.Tenant.Ref = "" }, true},
		{"unknown model", func(e *CustomerBilling) { e.Model = "invoice_me" }, true},
		{"empty model", func(e *CustomerBilling) { e.Model = "" }, true},
		{"empty tier", func(e *CustomerBilling) { e.Tier = "" }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := validCustomerBilling()
			if c.mutate != nil {
				c.mutate(&e)
			}
			err := validateCustomerBilling(e)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}

// validSection is a desired-state that Check must accept: an overlay tier, a
// cluster_pricing row, and a customer billed against a catalog tier.
func validSection() PurserSection {
	return PurserSection{
		BillingTiers: []BillingTier{
			{ID: "enterprise", BasePriceMonthly: "499.00"},
		},
		ClusterPricing: []ClusterPricing{samplePricing()},
		CustomerBilling: []CustomerBilling{
			validCustomerBilling(), // tier "payg" from the embedded catalog
			{Tenant: TenantRef{Ref: "quartermaster.system_tenant"}, Model: "postpaid", Tier: "enterprise"}, // tier from the overlay
		},
	}
}

func TestCheck(t *testing.T) {
	embedded := twoTierFixture() // provides "payg" and "free"

	t.Run("valid section passes", func(t *testing.T) {
		if err := Check(validSection(), embedded); err != nil {
			t.Fatalf("valid section rejected: %v", err)
		}
	})

	cases := []struct {
		name   string
		mutate func(*PurserSection)
	}{
		{"empty overlay tier id", func(s *PurserSection) { s.BillingTiers[0].ID = "" }},
		{"overlay tier bad base price", func(s *PurserSection) { s.BillingTiers[0].BasePriceMonthly = "10oops" }},
		{"empty cluster id", func(s *PurserSection) { s.ClusterPricing[0].ClusterID = "" }},
		{"invalid pricing model", func(s *PurserSection) { s.ClusterPricing[0].PricingModel = "bogus" }},
		{"cluster bad base price", func(s *PurserSection) { s.ClusterPricing[0].BasePrice = "1e3" }},
		{"customer invalid model", func(s *PurserSection) { s.CustomerBilling[0].Model = "barter" }},
		{"tenant ref not quartermaster", func(s *PurserSection) { s.CustomerBilling[0].Tenant.Ref = "vault.tenants[x]" }},
		{"tier not in catalog or overlay", func(s *PurserSection) { s.CustomerBilling[0].Tier = "platinum" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := validSection()
			c.mutate(&s)
			if err := Check(s, embedded); err == nil {
				t.Fatalf("expected Check to reject %s", c.name)
			}
		})
	}
}
