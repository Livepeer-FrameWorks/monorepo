package bootstrap

// DesiredState is the slice of the rendered bootstrap-desired-state file Purser
// consumes. The CLI's renderer (cli/pkg/bootstrap) writes the full document; each
// service parses only its top-level section.
//
// YAML structure:
//
//	purser:
//	  billing_tiers: [...]      # overlay-only catalog additions/overrides
//	  cluster_pricing: [...]    # one row per cluster declaring pricing
//	  customer_billing: [...]   # one entry per customer tenant
//
// The bootstrap subcommand decodes with KnownFields(true), so typos and stale
// fields fail parse — that's the only schema-evolution check we want at this
// stage. Other top-level sections (quartermaster, accounts, ...) are ignored;
// those are not Purser's concern.
type DesiredState struct {
	Purser PurserSection `yaml:"purser,omitempty"`
}

// PurserSection mirrors cli/pkg/bootstrap.PurserSection's wire format. Field shapes
// are duplicated rather than imported across modules so api_billing stays free of a
// cli/* dependency; the YAML schema is the cross-service contract.
type PurserSection struct {
	BillingTiers    []BillingTier     `yaml:"billing_tiers,omitempty"`
	ClusterPricing  []ClusterPricing  `yaml:"cluster_pricing,omitempty"`
	CustomerBilling []CustomerBilling `yaml:"customer_billing,omitempty"`
}

// BillingTier overlay entries. The embedded catalog is the baseline; overlay
// entries are merged in by Purser at apply time. Stable key: ID.
//
// Fields not exposed here are deliberate:
//   - billing_period (monthly/yearly) is owned by the embedded catalog;
//     overlays don't get to redefine cycle semantics.
//   - is_active is a runtime lifecycle concern (admin tools deactivate
//     tiers); bootstrap is for desired-state, not enable/disable churn.
type BillingTier struct {
	ID                  string         `yaml:"id"`
	DisplayName         string         `yaml:"display_name,omitempty"`
	TierLevel           int32          `yaml:"tier_level,omitempty"`
	BasePriceMonthly    string         `yaml:"base_price_monthly,omitempty"`
	Currency            string         `yaml:"currency,omitempty"`
	BandwidthAllocation map[string]any `yaml:"bandwidth_allocation,omitempty"`
	StorageAllocation   map[string]any `yaml:"storage_allocation,omitempty"`
	ComputeAllocation   map[string]any `yaml:"compute_allocation,omitempty"`
	Features            []string       `yaml:"features,omitempty"`
	OverageRates        map[string]any `yaml:"overage_rates,omitempty"`
	Override            bool           `yaml:"override,omitempty"`
}

// ClusterPricing is one row reconciled into purser.cluster_pricing. Stable key:
// ClusterID. Stripe IDs (stripe_product_id, stripe_price_id_*, stripe_meter_id)
// are not in this shape — the Stripe sync owns them.
type ClusterPricing struct {
	ClusterID         string         `yaml:"cluster_id"`
	PricingModel      string         `yaml:"pricing_model"`
	RequiredTierLevel *int32         `yaml:"required_tier_level,omitempty"`
	AllowFreeTier     *bool          `yaml:"allow_free_tier,omitempty"`
	BasePrice         string         `yaml:"base_price,omitempty"`
	Currency          string         `yaml:"currency,omitempty"`
	MeteredRates      map[string]any `yaml:"metered_rates,omitempty"`
	DefaultQuotas     map[string]any `yaml:"default_quotas,omitempty"`
}

// CustomerBilling is a per-customer-tenant subscription row. Tenant references
// the QM tenant by alias; ReconcileCustomerBilling resolves alias → UUID via
// Quartermaster's ResolveTenantAliases gRPC.
type CustomerBilling struct {
	Tenant        TenantRef `yaml:"tenant"`
	Model         string    `yaml:"model"`
	Tier          string    `yaml:"tier"`
	ClusterAccess string    `yaml:"cluster_access,omitempty"`
}

// TenantRef mirrors cli/pkg/bootstrap.TenantRef's wire format.
type TenantRef struct {
	Ref string `yaml:"ref"`
}
