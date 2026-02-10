package dns

import "testing"

func TestSanitizeLabel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"us_west_1", "us-west-1"},
		{"US-East-2", "us-east-2"},
		{"  spaces  ", "spaces"},
		{"under_score", "under-score"},
		{"special!@#chars", "special---chars"},
		{"already-clean", "already-clean"},
		{"UPPER", "upper"},
		{"", "default"},
		{"   ", "default"},
		{"___", "default"},
		{"-leading-trailing-", "leading-trailing"},
		{"prod.cluster.1", "prod-cluster-1"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeLabel(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeLabel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
