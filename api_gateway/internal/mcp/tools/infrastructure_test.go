package tools

import (
	"testing"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func TestParseClusterVisibility(t *testing.T) {
	tests := []struct {
		in      string
		want    quartermasterpb.ClusterVisibility
		wantErr bool
	}{
		{"PUBLIC", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PUBLIC, false},
		{"unlisted", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_UNLISTED, false},
		{"  private  ", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_PRIVATE, false},
		{"protected", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_UNSPECIFIED, true},
		{"", quartermasterpb.ClusterVisibility_CLUSTER_VISIBILITY_UNSPECIFIED, true},
	}
	for _, tt := range tests {
		got, err := parseClusterVisibility(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseClusterVisibility(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("parseClusterVisibility(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseClusterPricingModel(t *testing.T) {
	tests := []struct {
		in      string
		want    quartermasterpb.ClusterPricingModel
		wantErr bool
	}{
		{"free_unmetered", quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED, false},
		{"METERED", quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_METERED, false},
		{"Monthly", quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_MONTHLY, false},
		{" tier_inherit ", quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_TIER_INHERIT, false},
		{"custom", quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_CUSTOM, false},
		{"enterprise", quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_UNSPECIFIED, true},
		{"", quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_UNSPECIFIED, true},
	}
	for _, tt := range tests {
		got, err := parseClusterPricingModel(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseClusterPricingModel(%q) err=%v, wantErr=%v", tt.in, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("parseClusterPricingModel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
