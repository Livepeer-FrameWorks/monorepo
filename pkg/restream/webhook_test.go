package restream

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// webhookResolverPolicy answers every host with addr, standing in for DNS.
func webhookResolverPolicy(addr string) DestinationPolicy {
	return DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP(addr)}, nil
	}}
}

func TestValidateWebhookURLRejectsAtCreate(t *testing.T) {
	public := webhookResolverPolicy("93.184.216.34")
	for _, raw := range []string{
		"",
		"http://hooks.example.com/x",
		"ftp://hooks.example.com/x",
		"https://user:pass@hooks.example.com/x",
		"https://hooks.example.com/x#frag",
		"https:///nohost",
		"https://localhost/x",
		"https://api.localhost/x",
		"https://printer.local/x",
		"https://metadata.google.internal/x",
		"https://frameworks.network/hooks",
		"https://bridge.frameworks.network/hooks",
		"https://BRIDGE.FRAMEWORKS.NETWORK./hooks",
		"https://127.0.0.1/x",
		"https://10.1.2.3/x",
		"https://192.168.0.10/x",
		"https://169.254.169.254/latest/meta-data",
		"https://100.64.0.7/x",
		"https://[::1]/x",
		"https://[fd00:ec2::254]/x",
		"https://0.0.0.0/x",
		"https://hooks.example.com:99999/x",
	} {
		if _, err := ValidateWebhookURL(context.Background(), public, raw); !errors.Is(err, ErrInvalidWebhookURL) {
			t.Errorf("ValidateWebhookURL(%q) = %v, want ErrInvalidWebhookURL", raw, err)
		}
	}
	for _, private := range []string{"10.0.0.8", "127.0.0.1", "169.254.169.254", "100.100.100.200"} {
		if _, err := ValidateWebhookURL(context.Background(), webhookResolverPolicy(private), "https://rebinder.example.com/x"); !errors.Is(err, ErrInvalidWebhookURL) {
			t.Errorf("a host resolving to %s = %v, want ErrInvalidWebhookURL", private, err)
		}
	}
	got, err := ValidateWebhookURL(context.Background(), public, "  https://hooks.example.com:8443/in?x=1 ")
	if err != nil || got != "https://hooks.example.com:8443/in?x=1" {
		t.Fatalf("public URL = %q, %v", got, err)
	}
	failing := DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("SERVFAIL")
	}}
	if _, err := ValidateWebhookURL(context.Background(), failing, "https://hooks.example.com/x"); !errors.Is(err, ErrDestinationResolution) {
		t.Fatalf("resolver failure = %v, want ErrDestinationResolution", err)
	}
}

func TestValidateWebhookURLPrivatePolicyAcceptsIsolatedReceivers(t *testing.T) {
	private := webhookResolverPolicy("192.168.10.40")
	private.AllowPrivate = true
	for _, raw := range []string{
		"http://192.168.10.40:8080/hooks",
		"https://10.1.2.3/x",
		"http://receiver.local/x",
		"http://webhook-receiver:9000/x",
	} {
		if _, err := ValidateWebhookURL(context.Background(), private, raw); err != nil {
			t.Errorf("ValidateWebhookURL(%q) with private destinations allowed = %v, want accepted", raw, err)
		}
	}
	for _, raw := range []string{
		"ftp://192.168.10.40/x",
		"http://user:pass@192.168.10.40/x",
		"http://127.0.0.1/x",
		"http://169.254.169.254/latest/meta-data",
		"http://metadata.google.internal/x",
		"http://bridge.staging.frameworks.network/hooks",
	} {
		if _, err := ValidateWebhookURL(context.Background(), private, raw); !errors.Is(err, ErrInvalidWebhookURL) {
			t.Errorf("ValidateWebhookURL(%q) with private destinations allowed = %v, want ErrInvalidWebhookURL", raw, err)
		}
	}
	loopback := webhookResolverPolicy("127.0.0.1")
	loopback.AllowPrivate = true
	if _, err := ValidateWebhookURL(context.Background(), loopback, "http://localhost:9000/x"); !errors.Is(err, ErrInvalidWebhookURL) {
		t.Fatalf("localhost resolving to loopback = %v, want ErrInvalidWebhookURL", err)
	}
}

// WebhookDestinationPolicy carries only the private-destination switch; no
// operator restream exception reaches webhook endpoints.
func TestWebhookDestinationPolicy(t *testing.T) {
	for _, allow := range []bool{false, true} {
		policy := WebhookDestinationPolicy(allow)
		if policy.AllowPrivate != allow || len(policy.AllowedCIDRs) != 0 || len(policy.DeniedCIDRs) != 0 || policy.LookupIP == nil || policy.PlatformAddress == nil {
			t.Fatalf("WebhookDestinationPolicy(%v) = %+v", allow, policy)
		}
		// The test host's own routes (a developer VPN) must not decide.
		policy.PlatformAddress = func(net.IP) bool { return false }
		err := policy.ValidateIP(net.ParseIP("10.0.0.8"))
		if allow != (err == nil) {
			t.Fatalf("WebhookDestinationPolicy(%v) private address err = %v", allow, err)
		}
		if err := policy.ValidateIP(net.ParseIP("127.0.0.1")); err == nil {
			t.Fatalf("WebhookDestinationPolicy(%v) admitted loopback", allow)
		}
	}
}

// localUnicastAddress returns a non-loopback address assigned to this host,
// the kind of address a platform service bound to every interface answers on.
func localUnicastAddress(t *testing.T) net.IP {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatalf("list interface addresses: %v", err)
	}
	for _, addr := range addrs {
		prefix, ok := addr.(*net.IPNet)
		if !ok || prefix.IP.IsLoopback() || prefix.IP.IsLinkLocalUnicast() || prefix.IP.To4() == nil {
			continue
		}
		return prefix.IP.To4()
	}
	t.Skip("host has no non-loopback IPv4 address")
	return nil
}

// The private-destination switch opens customer networks only. Platform
// names (the Privateer mesh namespace) and the platform host's own addresses
// stay refused at registration and at every dial.
func TestWebhookPolicyRefusesPlatformDestinationsWithPrivateAllowed(t *testing.T) {
	private := webhookResolverPolicy("192.168.10.40")
	private.AllowPrivate = true
	for _, raw := range []string{
		"http://quartermaster.internal:18002/rc18-ssrf-probe",
		"https://quartermaster.internal/x",
		"http://webhook-receiver.internal:9000/x",
		"http://localhost:9000/x",
		"http://api.localhost/x",
	} {
		if _, err := ValidateWebhookURL(context.Background(), private, raw); !errors.Is(err, ErrInvalidWebhookURL) {
			t.Errorf("ValidateWebhookURL(%q) with private destinations allowed = %v, want ErrInvalidWebhookURL", raw, err)
		}
	}

	local := localUnicastAddress(t)
	policy := WebhookDestinationPolicy(true)
	if err := policy.ValidateIP(local); err == nil {
		t.Errorf("WebhookDestinationPolicy(true).ValidateIP(%s) admitted this host's own address", local)
	}
	if err := policy.DialControl()("tcp", net.JoinHostPort(local.String(), "18002"), nil); !errors.Is(err, ErrDialBlocked) {
		t.Errorf("dial to this host's own address %s = %v, want ErrDialBlocked", local, err)
	}
	rebinding := policy
	rebinding.LookupIP = func(context.Context, string) ([]net.IP, error) { return []net.IP{local}, nil }
	if _, err := ValidateWebhookURL(context.Background(), rebinding, "http://receiver.example.com/x"); !errors.Is(err, ErrInvalidWebhookURL) {
		t.Errorf("a name resolving to this host's own address %s = %v, want ErrInvalidWebhookURL", local, err)
	}
}

// A host the resolver authoritatively reports as nonexistent, or that has no
// addresses, is an invalid URL the tenant must fix; only a resolver failure
// that may clear on retry is reported as ErrDestinationResolution.
func TestValidateWebhookURLSeparatesNonexistentHostFromResolverFailure(t *testing.T) {
	private := DestinationPolicy{AllowPrivate: true}
	for name, lookup := range map[string]func(context.Context, string) ([]net.IP, error){
		"nxdomain": func(_ context.Context, host string) ([]net.IP, error) {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		},
		"no addresses": func(context.Context, string) ([]net.IP, error) { return nil, nil },
	} {
		private.LookupIP = lookup
		_, err := ValidateWebhookURL(context.Background(), private, "http://foo.local/hook")
		if !errors.Is(err, ErrInvalidWebhookURL) || errors.Is(err, ErrDestinationResolution) {
			t.Fatalf("%s: err = %v, want ErrInvalidWebhookURL only", name, err)
		}
		if !strings.Contains(err.Error(), "foo.local does not resolve") {
			t.Fatalf("%s: err = %q, want the host named as not resolving", name, err)
		}
	}
	for name, lookup := range map[string]func(context.Context, string) ([]net.IP, error){
		"servfail": func(_ context.Context, host string) ([]net.IP, error) {
			return nil, &net.DNSError{Err: "server misbehaving", Name: host, IsTemporary: true}
		},
		"timeout": func(_ context.Context, host string) ([]net.IP, error) {
			return nil, &net.DNSError{Err: "i/o timeout", Name: host, IsTimeout: true, IsTemporary: true}
		},
	} {
		private.LookupIP = lookup
		_, err := ValidateWebhookURL(context.Background(), private, "https://hooks.example.com/x")
		if !errors.Is(err, ErrDestinationResolution) || errors.Is(err, ErrInvalidWebhookURL) {
			t.Fatalf("%s: err = %v, want ErrDestinationResolution only", name, err)
		}
	}
}
