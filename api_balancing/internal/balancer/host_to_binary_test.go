package balancer

import (
	"bytes"
	"context"
	"net"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Intent: hostToBinary encodes an IP into MistServer's 16-byte host key. IPv4
// must land in the IPv4-mapped IPv6 slot (::ffff:a.b.c.d) with the 0xff,0xff
// marker at bytes 10-11; IPv6 fills the full 16 bytes. The IP-literal path is
// deterministic (no DNS). Unparseable input -> zero-filled key.
func TestHostToBinaryIPLiteral(t *testing.T) {
	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))

	t.Run("ipv4 maps into v4-mapped v6 slot", func(t *testing.T) {
		got := lb.hostToBinary(context.Background(), "1.2.3.4")
		var want [16]byte
		want[10], want[11] = 0xff, 0xff
		copy(want[12:], net.IPv4(1, 2, 3, 4).To4())
		if !bytes.Equal(got[:], want[:]) {
			t.Fatalf("hostToBinary(1.2.3.4) = % x, want % x", got, want)
		}
	})

	t.Run("ipv6 fills all 16 bytes", func(t *testing.T) {
		got := lb.hostToBinary(context.Background(), "2001:db8::1")
		var want [16]byte
		copy(want[:], net.ParseIP("2001:db8::1").To16())
		if !bytes.Equal(got[:], want[:]) {
			t.Fatalf("hostToBinary(2001:db8::1) = % x, want % x", got, want)
		}
		if want[10] == 0xff && want[11] == 0xff {
			t.Fatal("ipv6 should not carry the v4-mapped marker")
		}
	})

	t.Run("unparseable empty host -> zero key", func(t *testing.T) {
		got := lb.hostToBinary(context.Background(), "")
		var zero [16]byte
		if !bytes.Equal(got[:], zero[:]) {
			t.Fatalf("hostToBinary(\"\") = % x, want zero-filled", got)
		}
	})

	// A non-IP hostname takes the DNS-resolution branch. "localhost" resolves
	// deterministically via the system hosts file (no network), so it yields a
	// populated key. We assert only non-zero — whether it lands on 127.0.0.1 or
	// ::1 is platform-dependent (resolver order), and the branch's job is just
	// to encode whatever it resolved.
	t.Run("resolvable hostname -> non-zero key", func(t *testing.T) {
		got := lb.hostToBinary(context.Background(), "localhost")
		var zero [16]byte
		if bytes.Equal(got[:], zero[:]) {
			t.Fatal("hostToBinary(localhost) returned zero key; expected a resolved address")
		}
	})
}
