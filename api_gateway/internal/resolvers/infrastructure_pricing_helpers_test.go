package resolvers

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func TestParsePriceToCents(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},        // absent price → 0
		{"abc", 0},     // unparseable → 0, never panics
		{"0", 0},       // zero
		{"1", 100},     // whole units → cents
		{"9.99", 999},  // typical price
		{"0.01", 1},    // smallest cent
		{"2.005", 201}, // half-cent rounds to nearest (math.Round)
		{"10.004", 1000},
	}
	for _, tt := range tests {
		if got := parsePriceToCents(tt.in); got != tt.want {
			t.Errorf("parsePriceToCents(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// pricingModelStringToProto and pricingModelProtoToString must round-trip for
// every known model; an unknown string must NOT silently map to a billable
// model — it falls back to the free tier.
func TestPricingModelRoundTrip(t *testing.T) {
	models := []string{"free_unmetered", "metered", "monthly", "tier_inherit", "custom"}
	for _, m := range models {
		p := pricingModelStringToProto(m)
		if back := pricingModelProtoToString(p); back != m {
			t.Errorf("round-trip %q → %v → %q", m, p, back)
		}
	}
	if got := pricingModelStringToProto("nonsense"); got != quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED {
		t.Errorf("unknown pricing model = %v, want FREE_UNMETERED fallback", got)
	}
}

func TestNormalizeFilterID(t *testing.T) {
	str := func(s string) *string { return &s }

	// nil / blank → empty filter (no error): "no filter applied".
	for _, in := range []*string{nil, str(""), str("   ")} {
		got, err := normalizeFilterID(in, globalid.TypeCluster)
		if err != nil || got != "" {
			t.Errorf("normalizeFilterID(%v) = (%q,%v), want (\"\",nil)", in, got, err)
		}
	}

	// A valid global ID of the expected type decodes to its raw backing ID.
	gid := globalid.Encode(globalid.TypeCluster, "cluster-7")
	got, err := normalizeFilterID(&gid, globalid.TypeCluster)
	if err != nil || got != "cluster-7" {
		t.Errorf("normalizeFilterID(valid) = (%q,%v), want (cluster-7,nil)", got, err)
	}

	// A global ID of the WRONG type must error — never cross-decode one node
	// type's ID as another (would filter the wrong resource).
	wrong := globalid.Encode(globalid.TypeStream, "stream-1")
	if _, wrongErr := normalizeFilterID(&wrong, globalid.TypeCluster); wrongErr == nil {
		t.Error("normalizeFilterID(wrong-type) err = nil, want type-mismatch error")
	}

	// A non-global-id string is passed through as a raw ID (DecodeExpected contract).
	raw := "plain-cluster-id"
	got, err = normalizeFilterID(&raw, globalid.TypeCluster)
	if err != nil || got != raw {
		t.Errorf("normalizeFilterID(raw) = (%q,%v), want (%q,nil)", got, err, raw)
	}
}
