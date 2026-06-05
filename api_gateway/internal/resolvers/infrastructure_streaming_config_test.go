package resolvers

import (
	"testing"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func TestStreamingConfigDomainNormalizesBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "domain",
			baseURL: "frameworks.network",
			want:    "edge-ingest.media-central-primary.frameworks.network",
		},
		{
			name:    "https URL",
			baseURL: "https://frameworks.network",
			want:    "edge-ingest.media-central-primary.frameworks.network",
		},
		{
			name:    "URL with path",
			baseURL: "https://frameworks.network/platform",
			want:    "edge-ingest.media-central-primary.frameworks.network",
		},
		{
			name:    "domain with port",
			baseURL: "frameworks.network:443",
			want:    "edge-ingest.media-central-primary.frameworks.network",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := streamingConfigDomain("edge-ingest", "media-central-primary", tt.baseURL)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestTenantAliasEligibleForStreaming(t *testing.T) {
	cases := []struct {
		name   string
		tenant *quartermasterpb.Tenant
		want   bool
	}{
		{name: "nil", tenant: nil, want: false},
		{name: "inactive paid", tenant: &quartermasterpb.Tenant{IsActive: false, DeploymentTier: "pro"}, want: false},
		{name: "active free", tenant: &quartermasterpb.Tenant{IsActive: true, DeploymentTier: "free"}, want: false},
		{name: "active missing tier", tenant: &quartermasterpb.Tenant{IsActive: true}, want: false},
		{name: "active paid", tenant: &quartermasterpb.Tenant{IsActive: true, DeploymentTier: "creator"}, want: true},
		{name: "active paid with spaces", tenant: &quartermasterpb.Tenant{IsActive: true, DeploymentTier: " Pro "}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tenantAliasEligibleForStreaming(tc.tenant); got != tc.want {
				t.Fatalf("tenantAliasEligibleForStreaming() = %v, want %v", got, tc.want)
			}
		})
	}
}
