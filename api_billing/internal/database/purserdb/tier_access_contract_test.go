package purserdb

import (
	"strings"
	"testing"
)

func TestEffectiveDNSEntitlementSQLPrecedenceAndFailClosedDefault(t *testing.T) {
	for name, query := range map[string]string{
		"single tenant": loadEffectiveDNSEntitlements,
		"sweep":         listSubscriptionTierNames,
	} {
		t.Run(name, func(t *testing.T) {
			override := strings.Index(query, "subscription_entitlement_overrides")
			tierDefault := strings.Index(query, "tier_entitlements")
			denyDefault := strings.Index(query, "'false'::jsonb")
			if override < 0 || tierDefault < 0 || denyDefault < 0 || override >= tierDefault || tierDefault >= denyDefault {
				t.Fatalf("DNS entitlement precedence must be override > tier > false")
			}
		})
	}
	if !strings.Contains(listSubscriptionTierNames, "CASE WHEN ts.status = 'active'") ||
		!strings.Contains(listSubscriptionTierNames, "ELSE false") {
		t.Fatal("cancelled subscriptions must materialize both DNS grants as false")
	}
}
