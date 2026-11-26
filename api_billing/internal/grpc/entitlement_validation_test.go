package grpc

import "testing"

func TestEntitlementOverridesRejectMalformedRuntimeCaps(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"billing_prepaid_grace_credit_cents", "1001"}, {"billing_prepaid_grace_seconds", "null"}, {"billing_prepaid_grace_rearm_seconds", "0"}, {"max_concurrent_streams", `"3"`}, {"max_concurrent_streams", "-1"}, {"max_concurrent_viewers", "2147483648"}, {"recording_retention_days", "null"}, {"storage_limit_gb", "1.5"}, {"storage_limit_gb", "8589934592"}, {"custom_domain_enabled", `"false"`}, {"dvr_allow_cluster_extension", "1"},
	} {
		if err := validateEntitlementOverride(tc.key, tc.value); err == nil {
			t.Errorf("malformed runtime policy admitted: %s=%s", tc.key, tc.value)
		}
	}
	for _, tc := range []struct{ key, value string }{{"billing_prepaid_grace_credit_cents", "0"}, {"billing_prepaid_grace_seconds", "259200"}, {"max_concurrent_streams", "0"}, {"storage_limit_gb", "10"}, {"recording_retention_days", "0"}, {"custom_domain_enabled", "false"}, {"extension", `{"value":1}`}} {
		if err := validateEntitlementOverride(tc.key, tc.value); err != nil {
			t.Errorf("valid entitlement %s=%s: %v", tc.key, tc.value, err)
		}
	}
}
