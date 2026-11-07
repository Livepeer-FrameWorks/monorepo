package stripe

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTaxProfileAcceptedPolicyRenewalChangesIdentity(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	until := from.AddDate(1, 0, 0)
	profile := TaxProfile{Country: "US", RateBPS: 0}
	profile.Evidence = json.RawMessage(`{"decision":{"policy_reference":"policy-2026","policy_valid_from":"2026-01-01T00:00:00Z","policy_valid_until":"2027-01-01T00:00:00Z"}}`)
	setTaxProfilePolicy(t, &profile, "policy-2026", from, until)
	original := profile.Fingerprint()
	renewed := profile
	setTaxProfilePolicy(t, &renewed, "policy-2027", until, until.AddDate(1, 0, 0))
	if renewed.Fingerprint() == original {
		t.Fatal("renewed accepted policy retained the expired policy profile identity")
	}
	if profile.Fingerprint() != original {
		t.Fatal("renewal changed the original policy profile identity")
	}
}

func TestTaxProfilePolicyIdentityIgnoresDailyEvidenceRefresh(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	profile := TaxProfile{Country: "US", RateBPS: 0}
	setTaxProfilePolicy(t, &profile, "policy-2026", from, from.AddDate(1, 0, 0))
	profile.Evidence = json.RawMessage(`{"assessed_at":"2026-05-01T00:00:00Z","ip_country":"US"}`)
	original := profile.Fingerprint()
	profile.Evidence = json.RawMessage(`{"assessed_at":"2026-05-02T00:00:00Z","ip_country":"CA","vat_validation":{"checked_at":"2026-05-02T00:00:00Z"}}`)
	if profile.Fingerprint() != original {
		t.Fatal("daily validation or IP evidence changed the accepted policy identity")
	}
}

// JSON exercises the accepted profile's serialized policy inputs.
func setTaxProfilePolicy(t *testing.T, profile *TaxProfile, reference string, from, until time.Time) {
	t.Helper()
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	fields["policy_reference"] = reference
	fields["policy_valid_from"] = from
	fields["policy_valid_until"] = until
	encoded, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	var accepted TaxProfile
	if err = json.Unmarshal(encoded, &accepted); err != nil {
		t.Fatal(err)
	}
	*profile = accepted
}
