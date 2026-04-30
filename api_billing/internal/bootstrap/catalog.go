// Package bootstrap holds Purser's reconcilers for the bootstrap-desired-state
// schema (see docs/architecture/bootstrap-desired-state.md). Both `purser bootstrap`
// and the gRPC handler delegate to these reconcilers so there is one source of truth
// per Purser-owned table.
package bootstrap

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

//go:embed catalog/billing_tiers.yaml
var embeddedBillingTiersYAML []byte

// CatalogPricingRule is one priced behavior for a meter on a tier. Fields map
// 1:1 to columns in purser.tier_pricing_rules.
type CatalogPricingRule struct {
	Meter            string         `yaml:"meter"`
	Model            string         `yaml:"model"`
	Currency         string         `yaml:"currency,omitempty"`
	IncludedQuantity float64        `yaml:"included_quantity,omitempty"`
	UnitPrice        string         `yaml:"unit_price"` // string for decimal precision
	Config           map[string]any `yaml:"config,omitempty"`
}

// CatalogTier is the embedded-catalog row that ReconcileBillingTierCatalog upserts
// into purser.billing_tiers (+ tier_entitlements + tier_pricing_rules). Field
// shapes match the SQL columns 1:1 where applicable; the entitlement map and
// rule slice are flattened into normalized rows by the reconciler.
type CatalogTier struct {
	TierName          string               `yaml:"tier_name"`
	DisplayName       string               `yaml:"display_name"`
	Description       string               `yaml:"description"`
	BasePrice         float64              `yaml:"base_price"`
	Currency          string               `yaml:"currency"`
	BillingPeriod     string               `yaml:"billing_period"`
	Features          map[string]any       `yaml:"features"`
	SupportLevel      string               `yaml:"support_level"`
	SLALevel          string               `yaml:"sla_level"`
	MeteringEnabled   bool                 `yaml:"metering_enabled"`
	Entitlements      map[string]any       `yaml:"entitlements"`
	PricingRules      []CatalogPricingRule `yaml:"pricing_rules"`
	TierLevel         int                  `yaml:"tier_level"`
	IsEnterprise      bool                 `yaml:"is_enterprise"`
	IsDefaultPrepaid  bool                 `yaml:"is_default_prepaid"`
	IsDefaultPostpaid bool                 `yaml:"is_default_postpaid"`
	// processes_live/processes_vod arrive as YAML strings carrying raw JSON.
	// MistServer reads them verbatim, so we keep them as strings end-to-end.
	ProcessesLive string `yaml:"processes_live"`
	ProcessesVOD  string `yaml:"processes_vod"`
}

// catalog is the parsed shape of catalog/billing_tiers.yaml.
type catalog struct {
	Tiers []CatalogTier `yaml:"tiers"`
}

// EmbeddedTiers parses the binary-embedded catalog. Production callers typically
// use this to seed ReconcileBillingTierCatalog; tests can replace tiers via
// ReconcileBillingTiersWith.
func EmbeddedTiers() ([]CatalogTier, error) {
	var c catalog
	if err := yaml.Unmarshal(embeddedBillingTiersYAML, &c); err != nil {
		return nil, fmt.Errorf("parse embedded billing tier catalog: %w", err)
	}
	if len(c.Tiers) == 0 {
		return nil, fmt.Errorf("embedded billing tier catalog has zero tiers")
	}
	for i, t := range c.Tiers {
		if t.TierName == "" {
			return nil, fmt.Errorf("embedded catalog tier[%d] missing tier_name", i)
		}
	}
	return c.Tiers, nil
}

// jsonBytes marshals an any-typed JSONB value into a `[]byte` ready for the SQL
// driver. nil maps become a literal `{}` to match the column DEFAULT.
func jsonBytes(v map[string]any) ([]byte, error) {
	if v == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(v)
}
