package bootstrap

import (
	"strings"
	"testing"
)

// Self-serve tiers sell no SLA and no feature the platform does not ship.
// SLAs exist only as Enterprise contract terms. Stripe checkout and the
// dashboard show these descriptions verbatim.
func TestEmbeddedCatalogSellsNoSelfServeSLAOrUnshippedFeature(t *testing.T) {
	tiers, err := EmbeddedTiers()
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range tiers {
		description := strings.ToLower(tier.Description)
		for _, claim := range []string{"watermark", "self-hosted", "24/7"} {
			if strings.Contains(description, claim) {
				t.Errorf("tier %s description %q claims %q", tier.TierName, tier.Description, claim)
			}
		}
		if tier.IsEnterprise {
			continue
		}
		if tier.SLALevel != "" {
			t.Errorf("self-serve tier %s has sla_level %q", tier.TierName, tier.SLALevel)
		}
		if _, ok := tier.Features["sla"]; ok {
			t.Errorf("self-serve tier %s has an sla feature", tier.TierName)
		}
		if strings.Contains(description, "sla") {
			t.Errorf("self-serve tier %s description %q mentions an SLA", tier.TierName, tier.Description)
		}
	}
}
