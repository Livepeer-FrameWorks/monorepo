package ssh

import (
	"context"
	"errors"
	"testing"
)

// stubResolver injects fixed responses for ssh -G and DNS lookups.
func stubResolver(sshG map[string]string, dns map[string][]string, dnsErr error) *DefaultResolver {
	return &DefaultResolver{
		SSHGHostname: func(_ context.Context, alias string) (string, error) {
			if h, ok := sshG[alias]; ok {
				return h, nil
			}
			// Unmatched alias: ssh -G echoes the alias itself (no Host block).
			return alias, nil
		},
		LookupHost: func(_ context.Context, name string) ([]string, error) {
			if dnsErr != nil {
				return nil, dnsErr
			}
			if addrs, ok := dns[name]; ok {
				return addrs, nil
			}
			return nil, errors.New("no such host")
		},
	}
}

func TestResolveTarget_NoHostName_UsesUserAtAddress(t *testing.T) {
	t.Parallel()
	r := stubResolver(nil, nil, nil)
	got, err := r.Resolve(context.Background(), &ConnectionConfig{
		Address: "1.2.3.4",
		User:    "root",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.AliasVerified {
		t.Error("expected AliasVerified=false")
	}
	if got.Target != "root@1.2.3.4" {
		t.Errorf("Target=%q, want root@1.2.3.4", got.Target)
	}
}

func TestResolveTarget_AliasVerifiedIPMatch(t *testing.T) {
	t.Parallel()
	r := stubResolver(map[string]string{"central-eu-1": "1.2.3.4"}, nil, nil)
	got, err := r.Resolve(context.Background(), &ConnectionConfig{
		Address:  "1.2.3.4",
		User:     "root",
		HostName: "central-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.AliasVerified || got.Target != "central-eu-1" {
		t.Errorf("got %+v, want verified central-eu-1", got)
	}
}

func TestResolveTarget_AliasVerifiedDNSMatch(t *testing.T) {
	t.Parallel()
	r := stubResolver(
		map[string]string{"central-eu-1": "prod-bastion.example.com"},
		map[string][]string{"prod-bastion.example.com": {"1.2.3.4", "5.6.7.8"}},
		nil,
	)
	got, err := r.Resolve(context.Background(), &ConnectionConfig{
		Address:  "1.2.3.4",
		User:     "root",
		HostName: "central-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.AliasVerified || got.Target != "central-eu-1" {
		t.Errorf("got %+v, want verified central-eu-1", got)
	}
}

func TestResolveTarget_AliasDNSMismatch_FallsThrough(t *testing.T) {
	t.Parallel()
	r := stubResolver(
		map[string]string{
			"central-eu-1":            "stale.example.com",
			"frameworks-central-eu-1": "also-stale.example.com",
		},
		map[string][]string{
			"stale.example.com":      {"9.9.9.9"},
			"also-stale.example.com": {"8.8.8.8"},
		},
		nil,
	)
	got, err := r.Resolve(context.Background(), &ConnectionConfig{
		Address:  "1.2.3.4",
		User:     "root",
		HostName: "central-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.AliasVerified {
		t.Errorf("stale aliases must not verify; got %+v", got)
	}
	if got.Target != "root@1.2.3.4" {
		t.Errorf("Target=%q, want root@1.2.3.4 fallback", got.Target)
	}
}

func TestResolveTarget_StaleAliasNeverConnectsToWrongHost(t *testing.T) {
	t.Parallel()
	// Operator re-purposed `central-eu-1` alias to point at 127.0.0.1.
	// Manifest still says the real host is 1.2.3.4.
	r := stubResolver(map[string]string{"central-eu-1": "127.0.0.1"}, nil, nil)
	got, err := r.Resolve(context.Background(), &ConnectionConfig{
		Address:  "1.2.3.4",
		User:     "root",
		HostName: "central-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.AliasVerified {
		t.Fatal("SAFETY REGRESSION: stale alias pointing at 127.0.0.1 was accepted for a host at 1.2.3.4")
	}
	if got.Target != "root@1.2.3.4" {
		t.Errorf("Target=%q, want root@1.2.3.4", got.Target)
	}
}

func TestResolveTarget_FrameworksPrefixFallback(t *testing.T) {
	t.Parallel()
	// First candidate (bare name) doesn't resolve, second (frameworks-prefix) does.
	r := stubResolver(map[string]string{
		"frameworks-central-eu-1": "1.2.3.4",
	}, nil, nil)
	got, err := r.Resolve(context.Background(), &ConnectionConfig{
		Address:  "1.2.3.4",
		User:     "root",
		HostName: "central-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !got.AliasVerified || got.Target != "frameworks-central-eu-1" {
		t.Errorf("got %+v, want verified frameworks-central-eu-1", got)
	}
}

func TestResolveTarget_AliasDNSResolutionFails_FallsThrough(t *testing.T) {
	t.Parallel()
	// DNS is broken (offline, resolver failure). Safety: treat as unverified.
	r := stubResolver(
		map[string]string{"central-eu-1": "unreachable.example.com"},
		nil,
		errors.New("no network"),
	)
	got, err := r.Resolve(context.Background(), &ConnectionConfig{
		Address:  "1.2.3.4",
		User:     "root",
		HostName: "central-eu-1",
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.AliasVerified {
		t.Errorf("DNS failure must not produce a verified alias; got %+v", got)
	}
}
