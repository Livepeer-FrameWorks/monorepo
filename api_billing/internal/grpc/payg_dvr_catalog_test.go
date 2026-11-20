package grpc

import (
	"database/sql"
	"encoding/json"
	"testing"

	"frameworks/api_billing/internal/bootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/dvrpolicy"
)

func TestPAYGCatalogResolvesProductionDVRWindows(t *testing.T) {
	tiers, err := bootstrap.EmbeddedTiers()
	if err != nil {
		t.Fatal(err)
	}
	for _, tier := range tiers {
		if tier.TierName != "payg" {
			continue
		}
		encoded, err := json.Marshal(tier.Entitlements)
		if err != nil {
			t.Fatal(err)
		}
		policy := parseDVRPolicy(sql.NullString{String: string(encoded), Valid: true})
		limits := dvrpolicy.Tier{
			DefaultWindowSeconds: int(policy.GetDefaultWindowSeconds()), MaxWindowSeconds: int(policy.GetMaxWindowSeconds()),
			DefaultSegmentDurationSeconds: int(policy.GetDefaultSegmentDurationSeconds()), MaxEntries: int(policy.GetMaxEntries()),
			AllowClusterExtension: policy.GetAllowClusterExtension(),
		}
		defaults := dvrpolicy.Resolve(dvrpolicy.Request{}, limits, dvrpolicy.Cluster{})
		maximum := dvrpolicy.Resolve(dvrpolicy.Request{DVRWindowSeconds: 86400}, limits, dvrpolicy.Cluster{})
		if defaults.DVRWindowSeconds != 14400 || maximum.DVRWindowSeconds != 86400 || defaults.UsedDefaultFallback {
			t.Fatalf("PAYG DVR default=%d max=%d fallback=%v; want 14400/86400 from catalog", defaults.DVRWindowSeconds, maximum.DVRWindowSeconds, defaults.UsedDefaultFallback)
		}
		return
	}
	t.Fatal("PAYG tier missing")
}

func TestPAYGDVRIsAtLeastEverySelfServeTier(t *testing.T) {
	tiers, err := bootstrap.EmbeddedTiers()
	if err != nil {
		t.Fatal(err)
	}
	policies := map[string]map[string]int{}
	for _, tier := range tiers {
		if tier.TierName == "enterprise" {
			continue
		}
		encoded, err := json.Marshal(tier.Entitlements)
		if err != nil {
			t.Fatal(err)
		}
		policy := parseDVRPolicy(sql.NullString{String: string(encoded), Valid: true})
		policies[tier.TierName] = map[string]int{"default": int(policy.GetDefaultWindowSeconds()), "maximum": int(policy.GetMaxWindowSeconds()), "segments": int(policy.GetDefaultSegmentDurationSeconds()), "entries": int(policy.GetMaxEntries())}
		if tier.TierName == "payg" && policy.GetAllowClusterExtension() {
			t.Fatal("PAYG must not enable cluster extension")
		}
	}
	payg := policies["payg"]
	if len(payg) == 0 {
		t.Fatal("PAYG tier missing")
	}
	for tier, fields := range policies {
		for field, value := range fields {
			if payg[field] < value {
				t.Errorf("PAYG DVR %s=%d is lower than %s=%d", field, payg[field], tier, value)
			}
		}
	}
	for field, value := range policies["production"] {
		if payg[field] != value {
			t.Errorf("PAYG %s=%d, want Production %d", field, payg[field], value)
		}
	}
}
