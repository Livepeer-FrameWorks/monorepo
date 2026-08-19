package handlers

import "testing"

func TestEmailPricingLabel(t *testing.T) {
	cases := []struct {
		source, kind, want string
	}{
		{"tier", "", "Subscription tier"},
		{"cluster_metered", "third_party_marketplace", "Marketplace metered"},
		{"cluster_metered", "private", "Cluster metered"},
		{"cluster_monthly", "", "Cluster monthly"},
		{"cluster_custom", "", "Custom contract"},
		{"free_unmetered", "", "Free (no charge)"},
		{"self_hosted", "", "Self-hosted (no charge)"},
		{"included_subscription", "", "Included in subscription"},
		{"beta_free", "", "Usage is on us during beta"},
		{"unknown_source", "", ""},
	}
	for _, tc := range cases {
		if got := emailPricingLabel(tc.source, tc.kind); got != tc.want {
			t.Fatalf("emailPricingLabel(%q,%q) = %q, want %q", tc.source, tc.kind, got, tc.want)
		}
	}
}

func TestTokenDecimals(t *testing.T) {
	for _, asset := range []string{"ETH", "LPT"} {
		if d, ok := TokenDecimals(asset); !ok || d != 18 {
			t.Fatalf("%s should be 18-decimal, got (%d,%v)", asset, d, ok)
		}
	}
	if d, ok := TokenDecimals("USDC"); !ok || d != 6 {
		t.Fatalf("USDC should be 6-decimal, got (%d,%v)", d, ok)
	}
	if d, ok := TokenDecimals("DOGE"); ok || d != 0 {
		t.Fatalf("unknown asset should be (0,false), got (%d,%v)", d, ok)
	}
}
