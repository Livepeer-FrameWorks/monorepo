package validate

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
)

// resolverPolicy answers every host with addr, standing in for DNS.
func resolverPolicy(addr string) restream.DestinationPolicy {
	return restream.DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP(addr)}, nil
	}}
}

func TestURLRejectsAtCreate(t *testing.T) {
	public := resolverPolicy("93.184.216.34")
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
		if _, err := URL(context.Background(), public, raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("URL(%q) = %v, want ErrInvalid", raw, err)
		}
	}
	for _, private := range []string{"10.0.0.8", "127.0.0.1", "169.254.169.254", "100.100.100.200"} {
		if _, err := URL(context.Background(), resolverPolicy(private), "https://rebinder.example.com/x"); !errors.Is(err, ErrInvalid) {
			t.Errorf("a host resolving to %s = %v, want ErrInvalid", private, err)
		}
	}
	got, err := URL(context.Background(), public, "  https://hooks.example.com:8443/in?x=1 ")
	if err != nil || got != "https://hooks.example.com:8443/in?x=1" {
		t.Fatalf("public URL = %q, %v", got, err)
	}
	failing := restream.DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("SERVFAIL")
	}}
	if _, err := URL(context.Background(), failing, "https://hooks.example.com/x"); !errors.Is(err, ErrResolution) {
		t.Fatalf("resolver failure = %v, want ErrResolution", err)
	}
}

func TestURLPrivatePolicyAcceptsIsolatedReceivers(t *testing.T) {
	private := resolverPolicy("192.168.10.40")
	private.AllowPrivate = true
	for _, raw := range []string{
		"http://192.168.10.40:8080/hooks",
		"https://10.1.2.3/x",
		"http://receiver.local/x",
		"http://webhook-receiver.internal:9000/x",
		"http://webhook-receiver:9000/x",
	} {
		if _, err := URL(context.Background(), private, raw); err != nil {
			t.Errorf("URL(%q) with private destinations allowed = %v, want accepted", raw, err)
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
		if _, err := URL(context.Background(), private, raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("URL(%q) with private destinations allowed = %v, want ErrInvalid", raw, err)
		}
	}
	loopback := resolverPolicy("127.0.0.1")
	loopback.AllowPrivate = true
	if _, err := URL(context.Background(), loopback, "http://localhost:9000/x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("localhost resolving to loopback = %v, want ErrInvalid", err)
	}
}

func TestEventTypes(t *testing.T) {
	got, err := EventTypes([]string{" stream.live", "clip.ready", "stream.live", "*"})
	if err != nil || len(got) != 3 || got[0] != "stream.live" || got[1] != "clip.ready" || got[2] != "*" {
		t.Fatalf("EventTypes = %v, %v", got, err)
	}
	for _, bad := range [][]string{nil, {""}, {"stream.*"}, {"recording.chapter_ready"}, {"tenant.created"}, {"nope"}} {
		if _, err := EventTypes(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("EventTypes(%v) = %v, want ErrInvalid", bad, err)
		}
	}
}

func TestAPIVersionAndDescription(t *testing.T) {
	if v, err := APIVersion(""); err != nil || v != "v1" {
		t.Fatalf("APIVersion(\"\") = %q, %v", v, err)
	}
	if _, err := APIVersion("v2"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("APIVersion(v2) = %v", err)
	}
	long := make([]rune, 501)
	for i := range long {
		long[i] = 'é'
	}
	if _, err := Description(string(long)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("501-character description = %v", err)
	}
	if d, err := Description(" orders "); err != nil || d != "orders" {
		t.Fatalf("Description = %q, %v", d, err)
	}
}
