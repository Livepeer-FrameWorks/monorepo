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

func TestDestinationPolicyFromEnvironmentParsesAllowAndDenyCIDRs(t *testing.T) {
	t.Setenv(allowPrivateEnv, "true")
	t.Setenv(allowedCIDRsEnv, "10.0.0.0/8,192.0.2.10")
	t.Setenv(deniedCIDRsEnv, "10.20.0.0/16,203.0.113.0/24")
	policy, err := DestinationPolicyFromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if !policy.AllowPrivate || len(policy.AllowedCIDRs) != 2 || len(policy.DeniedCIDRs) != 2 {
		t.Fatalf("unexpected environment policy: %+v", policy)
	}

	t.Setenv(deniedCIDRsEnv, "not-a-network")
	if _, err := DestinationPolicyFromEnvironment(); err == nil {
		t.Fatal("invalid deny CIDR must fail closed")
	}
}
