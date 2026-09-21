package restream

import (
	"context"
	"errors"
	"net"
	"net/url"
	"testing"
)

func TestDestinationPolicy(t *testing.T) {
	private := net.ParseIP("10.20.30.40")
	public := net.ParseIP("1.1.1.1")
	policy := DestinationPolicy{LookupIP: func(_ context.Context, host string) ([]net.IP, error) {
		if host == "mixed.example" {
			return []net.IP{public, private}, nil
		}
		return []net.IP{public}, nil
	}}

	for _, raw := range []string{"rtmp://127.0.0.1/live", "rtmp://169.254.169.254/live", "rtmp://169.254.170.2/live", "rtmp://100.100.100.200/live", "rtmp://[fd00:ec2::254]/live", "rtmp://metadata.google.internal/live", "rtmp://10.20.30.40/live", "rtmp://[::1]/live", "rtmp://mixed.example/live"} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := policy.ValidateURI(context.Background(), parsed); err == nil {
			t.Errorf("ValidateURI(%q) succeeded, want rejection", raw)
		}
	}

	parsed, _ := url.Parse("rtmps://public.example/live")
	if err := policy.ValidateURI(context.Background(), parsed); err != nil {
		t.Fatalf("public destination rejected: %v", err)
	}

	_, allowed, _ := net.ParseCIDR("10.20.0.0/16")
	policy.AllowedCIDRs = []*net.IPNet{allowed}
	parsed, _ = url.Parse("rtmp://10.20.30.40/live")
	if err := policy.ValidateURI(context.Background(), parsed); err != nil {
		t.Fatalf("explicit CIDR exception rejected: %v", err)
	}
	parsed, _ = url.Parse("rtmp://169.254.169.254/live")
	if err := policy.ValidateURI(context.Background(), parsed); err == nil {
		t.Fatal("metadata endpoint must remain forbidden even with a broad exception")
	}

	_, deniedPublic, _ := net.ParseCIDR("1.1.1.0/24")
	policy.DeniedCIDRs = []*net.IPNet{deniedPublic, allowed}
	for _, raw := range []string{"rtmp://1.1.1.1/live", "rtmp://10.20.30.40/live"} {
		parsed, _ = url.Parse(raw)
		if err := policy.ValidateURI(context.Background(), parsed); err == nil {
			t.Fatalf("operator-denied destination %q was accepted", raw)
		}
	}
}

func TestDestinationPolicyRejectsSharedAndThisNetworkRanges(t *testing.T) {
	policy := DestinationPolicy{}
	for _, raw := range []string{"100.64.0.1", "100.127.255.254", "0.1.2.3", "::ffff:100.64.0.1"} {
		if err := policy.ValidateHost(raw); err == nil {
			t.Errorf("ValidateHost(%q) succeeded, want rejection", raw)
		}
	}
	for _, raw := range []string{"100.63.255.255", "100.128.0.1"} {
		if err := policy.ValidateHost(raw); err != nil {
			t.Errorf("ValidateHost(%q) rejected a public address: %v", raw, err)
		}
	}
	_, shared, _ := net.ParseCIDR("100.64.0.0/10")
	policy.AllowedCIDRs = []*net.IPNet{shared}
	if err := policy.ValidateHost("100.64.0.1"); err != nil {
		t.Fatalf("explicit CIDR exception rejected: %v", err)
	}
}

// TestDestinationPolicyRejectsTranslatedAndReservedIPv6 covers the IPv6
// forms Go classifies as global unicast that reach a non-public or unknown
// IPv4 destination, and the reserved ranges no tenant destination lives in.
func TestDestinationPolicyRejectsTranslatedAndReservedIPv6(t *testing.T) {
	policy := DestinationPolicy{}
	for _, raw := range []string{
		"64:ff9b::7f00:1",        // NAT64 well-known prefix embedding 127.0.0.1
		"64:ff9b::a9fe:a9fe",     // NAT64 embedding the metadata address
		"64:ff9b::10.1.2.3",      // NAT64 embedding a private address
		"64:ff9b::100.64.0.1",    // NAT64 embedding shared address space
		"64:ff9b:1::1.1.1.1",     // NAT64 local-use prefix
		"::10.1.2.3",             // IPv4-compatible
		"::1.1.1.1",              // IPv4-compatible, public embedded address
		"::7f00:1",               // IPv4-compatible loopback
		"fec0::1",                // site-local
		"2002:0a00:0001::1",      // 6to4 embedding 10.0.0.1
		"2002:7f00:0001::1",      // 6to4 embedding 127.0.0.1
		"2002:a9fe:a9fe::1",      // 6to4 embedding the metadata address
		"2001:0:4136:e378::1",    // Teredo
		"2001:db8::1",            // documentation
		"3fff::1",                // documentation
		"100::1",                 // discard-only
		"2001:2::1",              // benchmarking
		"192.0.2.1",              // documentation
		"198.51.100.1",           // documentation
		"203.0.113.1",            // documentation
		"198.18.0.1",             // benchmarking
		"192.0.0.1",              // IETF protocol assignments
		"240.0.0.1",              // reserved
		"::ffff:10.1.2.3",        // IPv4-mapped private
		"::ffff:169.254.169.254", // IPv4-mapped metadata
	} {
		if err := policy.ValidateHost(raw); err == nil {
			t.Errorf("ValidateHost(%q) succeeded, want rejection", raw)
		}
	}
	for _, raw := range []string{
		"64:ff9b::1.1.1.1",  // NAT64 to a public address (DNS64 on an IPv6-only host)
		"2002:0101:0101::1", // 6to4 embedding 1.1.1.1
		"2606:4700:4700::1111",
		"1.1.1.1",
	} {
		if err := policy.ValidateHost(raw); err != nil {
			t.Errorf("ValidateHost(%q) rejected a public destination: %v", raw, err)
		}
	}

	_, private, _ := net.ParseCIDR("10.0.0.0/8")
	allowPrivate := DestinationPolicy{AllowedCIDRs: []*net.IPNet{private}}
	if err := allowPrivate.ValidateHost("64:ff9b::10.1.2.3"); err != nil {
		t.Fatalf("an exception for the embedded IPv4 must apply to its NAT64 form: %v", err)
	}
	if err := allowPrivate.ValidateHost("64:ff9b::a9fe:a9fe"); err == nil {
		t.Fatal("the metadata address must stay forbidden through NAT64 even with an exception")
	}

	dial := policy.DialControl()
	if err := dial("tcp6", "[64:ff9b::7f00:1]:443", nil); !errors.Is(err, ErrDialBlocked) {
		t.Fatalf("dial to NAT64 loopback = %v, want ErrDialBlocked", err)
	}
}

// TestDialControlRefusesRebindingAtConnect models DNS rebinding: the host
// resolves to a public address when validated and to loopback when dialed.
// ValidateURI accepts it; the dialer's Control hook refuses the connect.
func TestDialControlRefusesRebindingAtConnect(t *testing.T) {
	listener, listenErr := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatal(listenErr)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(listener.Addr().String())

	policy := DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}}
	parsed, _ := url.Parse("https://rebind.example:" + port + "/hook")
	if err := policy.ValidateURI(context.Background(), parsed); err != nil {
		t.Fatalf("validation-time answer is public and must pass: %v", err)
	}

	dialer := &net.Dialer{Control: policy.DialControl()}
	// The second resolution answers loopback, as a rebinding server would.
	_, err := dialer.DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
	if !errors.Is(err, ErrDialBlocked) {
		t.Fatalf("dial to the rebound loopback address = %v, want ErrDialBlocked", err)
	}

	allowed := DestinationPolicy{AllowedCIDRs: []*net.IPNet{{IP: net.ParseIP("127.0.0.1").To4(), Mask: net.CIDRMask(32, 32)}}}
	conn, err := (&net.Dialer{Control: allowed.DialControl()}).DialContext(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("positive control: an explicitly allowed address must connect: %v", err)
	}
	_ = conn.Close()
}

// The webhook policy has no exceptions and resolves through the system
// resolver: an IP literal needs no lookup, and private or translated-private
// answers are refused.
func TestPublicDestinationPolicy(t *testing.T) {
	policy := PublicDestinationPolicy()
	if policy.AllowPrivate || len(policy.AllowedCIDRs) != 0 || len(policy.DeniedCIDRs) != 0 || policy.LookupIP == nil {
		t.Fatalf("webhook policy = %+v, want no exceptions and a resolver", policy)
	}
	for _, raw := range []string{"https://10.0.0.1/x", "https://[64:ff9b::a00:1]/x", "https://[2002:a00:1::1]/x"} {
		parsed, _ := url.Parse(raw)
		if err := policy.ValidateURI(context.Background(), parsed); err == nil {
			t.Errorf("ValidateURI(%q) accepted a non-public destination", raw)
		}
	}
	parsed, _ := url.Parse("https://93.184.216.34/x")
	if err := policy.ValidateURI(context.Background(), parsed); err != nil {
		t.Fatalf("public literal rejected: %v", err)
	}
}

func TestDestinationResolutionFailureIsRetryable(t *testing.T) {
	policy := DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("temporary resolver failure")
	}}
	parsed, _ := url.Parse("rtmp://unavailable.example/live")
	err := policy.ValidateURI(context.Background(), parsed)
	if !errors.Is(err, ErrDestinationResolution) {
		t.Fatalf("resolution failure must retain retryable classification: %v", err)
	}
}

func TestDestinationPolicyFromValuesParsesAllowAndDenyCIDRs(t *testing.T) {
	policy, err := DestinationPolicyFromValues("true", "10.0.0.0/8,192.0.2.10", "10.20.0.0/16,203.0.113.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if !policy.AllowPrivate || len(policy.AllowedCIDRs) != 2 || len(policy.DeniedCIDRs) != 2 {
		t.Fatalf("unexpected policy: %+v", policy)
	}

	if _, err := DestinationPolicyFromValues("true", "", "not-a-network"); err == nil {
		t.Fatal("invalid deny CIDR must fail closed")
	}
}
