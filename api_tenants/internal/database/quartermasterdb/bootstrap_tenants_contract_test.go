package quartermasterdb

import (
	"strings"
	"testing"
)

func TestInsertBootstrapTenantStartsWithObservedFailClosedDNSEntitlements(t *testing.T) {
	for _, fragment := range []string{
		"custom_subdomain_enabled, custom_domain_enabled",
		"billing_entitlements_observed_at",
		"false, false, NOW()",
	} {
		if !strings.Contains(insertBootstrapTenant, fragment) {
			t.Fatalf("bootstrap tenant insert missing %q", fragment)
		}
	}
}
