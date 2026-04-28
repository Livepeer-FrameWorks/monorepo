package clusterderive

import (
	"slices"
	"testing"
)

func TestWildcardBundleDomains(t *testing.T) {
	tests := []struct {
		name       string
		rootDomain string
		want       []string
	}{
		{
			name:       "apex root",
			rootDomain: "frameworks.network",
			want:       []string{"frameworks.network", "*.frameworks.network"},
		},
		{
			name:       "cluster-scoped root",
			rootDomain: "core-central-primary.frameworks.network",
			want:       []string{"core-central-primary.frameworks.network", "*.core-central-primary.frameworks.network"},
		},
		{
			name:       "trims surrounding whitespace",
			rootDomain: "  frameworks.network  ",
			want:       []string{"frameworks.network", "*.frameworks.network"},
		},
		{
			name:       "empty input",
			rootDomain: "",
			want:       nil,
		},
		{
			name:       "whitespace-only input",
			rootDomain: "   ",
			want:       nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WildcardBundleDomains(tt.rootDomain)
			if !slices.Equal(got, tt.want) {
				t.Errorf("WildcardBundleDomains(%q) = %v, want %v", tt.rootDomain, got, tt.want)
			}
		})
	}
}
