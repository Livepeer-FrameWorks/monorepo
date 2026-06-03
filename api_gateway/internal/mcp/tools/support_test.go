package tools

import "testing"

func TestIsGenericSupportHistoryQuery(t *testing.T) {
	tests := []struct {
		query string
		want  bool
	}{
		{"Search my past support tickets", true},
		{"my past support tickets", true},
		{"show me my tickets", true},
		{"  SUPPORT   TICKETS  ", true},
		{"billing", false},
		{"buffering issue", false},
	}
	for _, tc := range tests {
		t.Run(tc.query, func(t *testing.T) {
			if got := isGenericSupportHistoryQuery(tc.query); got != tc.want {
				t.Fatalf("isGenericSupportHistoryQuery(%q) = %v, want %v", tc.query, got, tc.want)
			}
		})
	}
}
