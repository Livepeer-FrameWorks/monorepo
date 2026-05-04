package resolvers

import "testing"

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
