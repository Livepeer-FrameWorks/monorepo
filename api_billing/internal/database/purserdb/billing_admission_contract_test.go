package purserdb

import (
	"strings"
	"testing"
)

func TestTenantAdmissionQueryStaysBounded(t *testing.T) {
	for _, forbidden := range []string{
		"tier_entitlements",
		"subscription_entitlement_overrides",
		"usage_records",
		"usage_adjustments",
		"tier_pricing_rules",
	} {
		if strings.Contains(getTenantAdmissionStatus, forbidden) {
			t.Fatalf("admission query unexpectedly references %s", forbidden)
		}
	}
	for _, required := range []string{"tenant_subscriptions", "prepaid_balances", "usage_reservations"} {
		if !strings.Contains(getTenantAdmissionStatus, required) {
			t.Fatalf("admission query missing %s", required)
		}
	}
}
