package cmd

import "testing"

func TestNormalizeDuration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		input       string
		want        string
		expectError bool
	}{
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "hours preserved",
			input: "24h",
			want:  "24h",
		},
		{
			name:  "compound duration preserved",
			input: "1h30m",
			want:  "1h30m",
		},
		{
			name:  "days normalized to hours",
			input: "7d",
			want:  "168h",
		},
		{
			name:  "larger days normalized to hours",
			input: "30d",
			want:  "720h",
		},
		{
			name:        "invalid day format",
			input:       "1.5d",
			expectError: true,
		},
		{
			name:        "zero day format",
			input:       "0d",
			expectError: true,
		},
		{
			name:        "negative day format",
			input:       "-1d",
			expectError: true,
		},
		{
			name:        "unknown unit",
			input:       "5w",
			expectError: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeDuration(tc.input)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error for %q, got none", tc.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeDuration(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
