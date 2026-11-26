package grpc

import (
	"encoding/json"
	"fmt"
	"math"
)

// Known runtime grants must retain the scalar shape their readers enforce.
// Missing grants inherit the tier; explicit zero and false keep their meaning.
func validateEntitlementOverride(key, value string) error {
	if !json.Valid([]byte(value)) {
		return fmt.Errorf("must be valid JSON")
	}
	switch key {
	case "recording_retention_days", "max_concurrent_streams", "max_concurrent_viewers", "dvr_default_window_seconds", "dvr_max_window_seconds", "dvr_default_segment_duration_seconds", "dvr_max_entries", "storage_limit_gb":
		var integer *int64
		if err := json.Unmarshal([]byte(value), &integer); err != nil || integer == nil {
			return fmt.Errorf("must be a non-negative JSON integer")
		}
		maximum := int64(math.MaxInt32)
		if key == "storage_limit_gb" {
			maximum = math.MaxInt64 / (1 << 30)
		}
		if *integer < 0 || *integer > maximum {
			return fmt.Errorf("must be between 0 and %d", maximum)
		}
	case "billing_prepaid_grace_seconds", "billing_prepaid_grace_credit_cents", "billing_prepaid_grace_rearm_seconds":
		var integer *int64
		if err := json.Unmarshal([]byte(value), &integer); err != nil || integer == nil {
			return fmt.Errorf("must be a JSON integer")
		}
		minimum, maximum := int64(0), int64(604800)
		if key == "billing_prepaid_grace_credit_cents" {
			maximum = 1000
		}
		if key == "billing_prepaid_grace_rearm_seconds" {
			minimum = 2592000
			maximum = 31536000
		}
		if *integer < minimum || *integer > maximum {
			return fmt.Errorf("must be between %d and %d", minimum, maximum)
		}
	case "custom_domain_enabled", "custom_subdomain_enabled", "dvr_allow_cluster_extension":
		var enabled *bool
		if err := json.Unmarshal([]byte(value), &enabled); err != nil || enabled == nil {
			return fmt.Errorf("must be a JSON boolean")
		}
	}
	return nil
}
