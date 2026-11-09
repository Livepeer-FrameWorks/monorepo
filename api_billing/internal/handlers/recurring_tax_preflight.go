package handlers

import (
	"encoding/json"
	billingstripe "frameworks/api_billing/internal/stripe"
)

func recurringTaxReviewEvidence(profile *billingstripe.TaxProfile, customer, subscription string) (json.RawMessage, error) {
	var proof CryptoTaxAssessment
	if err := json.Unmarshal(profile.Evidence, &proof); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"review_required": true, "reasons": proof.ReviewReasons, "fallback_rate_bps": profile.RateBPS, "profile_sha": profile.Fingerprint(), "customer_id": customer, "subscription_id": subscription})
}
