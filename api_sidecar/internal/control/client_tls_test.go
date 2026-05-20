package control

import "testing"

func TestFoghornControlServerName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		addr     string
		override string
		want     string
	}{
		{
			name: "internal fqdn",
			addr: "foghorn.internal:18019",
			want: "foghorn.internal",
		},
		{
			name: "mesh ip uses internal authority",
			addr: "10.88.1.10:18019",
			want: "foghorn.internal",
		},
		{
			name: "external cluster fqdn",
			addr: "foghorn.media-eu-1.frameworks.network:18029",
			want: "foghorn.media-eu-1.frameworks.network",
		},
		{
			name:     "override wins",
			addr:     "foghorn.media-eu-1.frameworks.network:18029",
			override: "custom.foghorn.example",
			want:     "custom.foghorn.example",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := foghornControlServerName(tc.addr, tc.override); got != tc.want {
				t.Fatalf("foghornControlServerName(%q, %q) = %q, want %q", tc.addr, tc.override, got, tc.want)
			}
		})
	}
}
