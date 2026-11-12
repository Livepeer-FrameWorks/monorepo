package grpc

import (
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestAuthAuditMasksSourceIP(t *testing.T) {
	for _, test := range []struct{ source, want string }{
		{"203.0.113.147", "203.0.113.0/24"},
		{"::ffff:203.0.113.147", "203.0.113.0/24"},
		{"2001:db8:1234:5678::abcd", "2001:db8:1234::/48"},
		{"", ""},
		{"untrusted-text", ""},
		{"203.0.113.147, 192.0.2.1", ""},
	} {
		t.Run(test.source, func(t *testing.T) {
			ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("x-client-ip", test.source))
			event := (&CommodoreServer{}).buildAuthEvent(ctx, eventAuthLoginFailed, "user", "tenant", "password", "", "", "rejected")
			if got := event.GetAuthEvent().GetIpBucket(); got != test.want {
				t.Fatalf("audit source=%q, want %q", got, test.want)
			}
		})
	}
}
