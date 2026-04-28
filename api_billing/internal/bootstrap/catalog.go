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

// CatalogTier is the embedded-catalog row that ReconcileBillingTierCatalog upserts
// into purser.billing_tiers. Field shapes match the SQL columns 1:1; JSONB columns
// are marshaled from any-typed nested maps so YAML can express them naturally.
type CatalogTier struct {
	TierName            string         `yaml:"tier_name"`
	DisplayName         string         `yaml:"display_name"`
	Description         string         `yaml:"description"`
	BasePrice           float64        `yaml:"base_price"`
	Currency            string         `yaml:"currency"`
	BillingPeriod       string         `yaml:"billing_period"`
	BandwidthAllocation map[string]any `yaml:"bandwidth_allocation"`
	StorageAllocation   map[string]any `yaml:"storage_allocation"`
	ComputeAllocation   map[string]any `yaml:"compute_allocation"`
	Features            map[string]any `yaml:"features"`
	SupportLevel        string         `yaml:"support_level"`
	SLALevel            string         `yaml:"sla_level"`
	MeteringEnabled     bool           `yaml:"metering_enabled"`
	OverageRates        map[string]any `yaml:"overage_rates"`
	TierLevel           int            `yaml:"tier_level"`
	IsEnterprise        bool           `yaml:"is_enterprise"`
	IsDefaultPrepaid    bool           `yaml:"is_default_prepaid"`
	IsDefaultPostpaid   bool           `yaml:"is_default_postpaid"`
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
