package tieraccess

import (
	"slices"
	"testing"
)

func TestPrimaryCandidates(t *testing.T) {
	canBePrimary := map[string]bool{"central-primary": false, "edge-b": true, "edge-a": true, "edge-low": true}
	tests := []struct {
		name     string
		eligible []EligibleCluster
		want     []string
	}{
		{name: "none", want: nil},
		{
			name:     "control cluster ranked first is never a candidate",
			eligible: []EligibleCluster{{"central-primary", 0}, {"edge-b", 0}, {"edge-a", 0}},
			want:     []string{"edge-a", "edge-b"},
		},
		{
			name:     "control cluster at a higher level does not hide the edges",
			eligible: []EligibleCluster{{"central-primary", 3}, {"edge-low", 0}},
			want:     []string{"edge-low"},
		},
		{
			name:     "highest edge level wins",
			eligible: []EligibleCluster{{"edge-low", 0}, {"edge-b", 2}, {"edge-a", 1}},
			want:     []string{"edge-b"},
		},
		{
			name:     "only control clusters",
			eligible: []EligibleCluster{{"central-primary", 0}, {"unknown", 0}},
			want:     nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PrimaryCandidates(tt.eligible, canBePrimary); !slices.Equal(got, tt.want) {
				t.Fatalf("PrimaryCandidates = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSelectPrimary(t *testing.T) {
	candidates := []string{"edge-a", "edge-b"}
	if got := SelectPrimary(nil, "edge-a"); got != "" {
		t.Fatalf("no candidates = %q, want empty", got)
	}
	if got := SelectPrimary(candidates, "edge-b"); got != "edge-b" {
		t.Fatalf("current candidate = %q, want edge-b kept", got)
	}
	if got := SelectPrimary(candidates, "central-primary"); got != "edge-a" {
		t.Fatalf("non-candidate current = %q, want edge-a", got)
	}
	if got := SelectPrimary(candidates, ""); got != "edge-a" {
		t.Fatalf("no current = %q, want edge-a", got)
	}
}
