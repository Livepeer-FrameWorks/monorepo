package webhooks

import "testing"

func TestIsProviderAllowed(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		service  string
		provider string
		allowed  bool
	}{
		{
			name:     "billing stripe",
			service:  "billing",
			provider: "stripe",
			allowed:  true,
		},
		{
			name:     "billing mollie",
			service:  "billing",
			provider: "mollie",
			allowed:  true,
		},
		{
			name:     "billing unknown provider",
			service:  "billing",
			provider: "paypal",
			allowed:  false,
		},
		{
			name:     "unknown service",
			service:  "payments",
			provider: "stripe",
			allowed:  false,
		},
		{
			name:     "empty service",
			service:  "",
			provider: "stripe",
			allowed:  false,
		},
		{
			name:     "empty provider",
			service:  "billing",
			provider: "",
			allowed:  false,
		},
		{
			name:     "empty service and provider",
			service:  "",
			provider: "",
			allowed:  false,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isProviderAllowed(tc.service, tc.provider); got != tc.allowed {
				t.Fatalf("isProviderAllowed(%q, %q) = %v, want %v", tc.service, tc.provider, got, tc.allowed)
			}
		})
	}
}
