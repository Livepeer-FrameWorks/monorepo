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

// URL applies the shared webhook URL rules (tested in pkg/restream) and maps
// their outcome onto this package's sentinels, which the gRPC layer turns into
// InvalidArgument and Unavailable.
func TestURLMapsSharedRules(t *testing.T) {
	public := resolverPolicy("93.184.216.34")
	got, err := URL(context.Background(), public, "  https://hooks.example.com:8443/in?x=1 ")
	if err != nil || got != "https://hooks.example.com:8443/in?x=1" {
		t.Fatalf("public URL = %q, %v", got, err)
	}
	for _, raw := range []string{"http://hooks.example.com/x", "https://10.1.2.3/x", "https://bridge.frameworks.network/hooks"} {
		if _, err := URL(context.Background(), public, raw); !errors.Is(err, ErrInvalid) {
			t.Errorf("URL(%q) = %v, want ErrInvalid", raw, err)
		}
	}
	failing := restream.DestinationPolicy{LookupIP: func(context.Context, string) ([]net.IP, error) {
		return nil, errors.New("SERVFAIL")
	}}
	if _, err := URL(context.Background(), failing, "https://hooks.example.com/x"); !errors.Is(err, ErrResolution) {
		t.Fatalf("resolver failure = %v, want ErrResolution", err)
	}
	private := resolverPolicy("192.168.10.40")
	private.AllowPrivate = true
	if _, err := URL(context.Background(), private, "http://receiver.internal:9000/x"); err != nil {
		t.Fatalf("private receiver with private destinations allowed = %v", err)
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
