package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

type fakeAliasedTenantsClient struct {
	resp  *quartermasterpb.ListAliasedTenantsForClusterResponse
	err   error
	calls int
}

func (f *fakeAliasedTenantsClient) ListAliasedTenantsForCluster(_ context.Context, _ string) (*quartermasterpb.ListAliasedTenantsForClusterResponse, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.resp == nil {
		return &quartermasterpb.ListAliasedTenantsForClusterResponse{}, nil
	}
	return f.resp, nil
}

func foghornRegistry() *fakeServiceRegistryClient {
	return &fakeServiceRegistryClient{
		responses: []*quartermasterpb.ListServiceInstancesResponse{{
			Instances: []*quartermasterpb.ServiceInstance{{ServiceId: "foghorn", Status: "running"}},
		}},
	}
}

func newAliasTestAgent(t *testing.T, registry *fakeServiceRegistryClient, alias *fakeAliasedTenantsClient, navigator *fakeCertificateClient) (*Agent, string, string) {
	t.Helper()
	dir := t.TempDir()
	tlsRoot := filepath.Join(dir, "tls")
	trigger := filepath.Join(dir, "reload.trigger")
	return &Agent{
		logger:          logging.NewLogger(),
		nodeID:          "core-eu-1",
		clusterID:       "core-central-primary",
		registryClient:  registry,
		aliasClient:     alias,
		navigatorClient: navigator,
		ingressTLSRoot:  tlsRoot,
		ingressTrigger:  trigger,
		ingressVersions: make(map[string]string),
		aliasVersions:   make(map[string]string),
		syncTimeout:     time.Second,
	}, tlsRoot, trigger
}

// Foghorn host: issued bundles are written keyed by subdomain, pending
// (not-found) bundles are skipped without error, stale subdirectories are
// pruned, and the reload trigger is touched once material changed.
func TestSyncTenantAliasCertificatesWritesPrunesAndTouchesTrigger(t *testing.T) {
	navigator := &fakeCertificateClient{
		tlsBundles: map[string]*dnspb.GetTLSBundleResponse{
			"tenant:t-acme": {Found: true, BundleId: "tenant:t-acme", CertPem: "acme-cert", KeyPem: "acme-key", Version: "v1"},
			// tenant:t-globex intentionally absent → issuance pending.
		},
	}
	alias := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{
		Tenants: []*quartermasterpb.AliasedTenantRef{
			{TenantId: "t-acme", Subdomain: "acme"},
			{TenantId: "t-globex", Subdomain: "globex"},
		},
	}}
	agent, tlsRoot, trigger := newAliasTestAgent(t, foghornRegistry(), alias, navigator)

	// Pre-plant a leftover from a downgraded tenant; the sync must prune it.
	staleDir := filepath.Join(tlsRoot, "tenant-alias", "oldco")
	if err := os.MkdirAll(staleDir, 0o750); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staleDir, "tls.crt"), []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	if err := agent.syncTenantAliasCertificates(); err != nil {
		t.Fatalf("syncTenantAliasCertificates: %v", err)
	}

	acmeDir := filepath.Join(tlsRoot, "tenant-alias", "acme")
	if got, err := os.ReadFile(filepath.Join(acmeDir, "tls.crt")); err != nil || string(got) != "acme-cert" {
		t.Fatalf("acme tls.crt = %q err=%v, want acme-cert", string(got), err)
	}
	if got, err := os.ReadFile(filepath.Join(acmeDir, "tls.key")); err != nil || string(got) != "acme-key" {
		t.Fatalf("acme tls.key = %q err=%v, want acme-key", string(got), err)
	}
	if _, err := os.Stat(filepath.Join(tlsRoot, "tenant-alias", "globex")); !os.IsNotExist(err) {
		t.Fatalf("globex (pending issuance) should have no dir; stat err=%v", err)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("stale oldco dir should have been pruned; stat err=%v", err)
	}
	if _, err := os.Stat(trigger); err != nil {
		t.Fatalf("expected reload trigger to be touched: %v", err)
	}
}

// A node without foghorn converges to an empty set: the alias list RPC is
// never made and leftovers are pruned (e.g. foghorn moved off this host).
func TestSyncTenantAliasCertificatesNonFoghornNodePrunes(t *testing.T) {
	registry := &fakeServiceRegistryClient{
		responses: []*quartermasterpb.ListServiceInstancesResponse{{
			Instances: []*quartermasterpb.ServiceInstance{{ServiceId: "helmsman", Status: "running"}},
		}},
	}
	alias := &fakeAliasedTenantsClient{}
	agent, tlsRoot, _ := newAliasTestAgent(t, registry, alias, &fakeCertificateClient{})

	staleDir := filepath.Join(tlsRoot, "tenant-alias", "acme")
	if err := os.MkdirAll(staleDir, 0o750); err != nil {
		t.Fatalf("mkdir stale: %v", err)
	}

	if err := agent.syncTenantAliasCertificates(); err != nil {
		t.Fatalf("syncTenantAliasCertificates: %v", err)
	}
	if alias.calls != 0 {
		t.Fatalf("ListAliasedTenantsForCluster calls = %d, want 0 on non-foghorn node", alias.calls)
	}
	if _, err := os.Stat(staleDir); !os.IsNotExist(err) {
		t.Fatalf("non-foghorn node should prune alias certs; stat err=%v", err)
	}
}

// Subdomains are path components; anything that fails the bundle-ID charset
// must be skipped before touching disk.
func TestSyncTenantAliasCertificatesRejectsUnsafeSubdomain(t *testing.T) {
	navigator := &fakeCertificateClient{
		tlsBundles: map[string]*dnspb.GetTLSBundleResponse{
			"tenant:t-evil": {Found: true, BundleId: "tenant:t-evil", CertPem: "c", KeyPem: "k", Version: "v1"},
		},
	}
	alias := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{
		Tenants: []*quartermasterpb.AliasedTenantRef{
			{TenantId: "t-evil", Subdomain: "../escape"},
			{TenantId: "t-evil", Subdomain: "UPPER"},
		},
	}}
	agent, tlsRoot, _ := newAliasTestAgent(t, foghornRegistry(), alias, navigator)

	if err := agent.syncTenantAliasCertificates(); err != nil {
		t.Fatalf("syncTenantAliasCertificates: %v", err)
	}
	if len(navigator.tlsBundleRequests) != 0 {
		t.Fatalf("GetTLSBundle calls = %d, want 0 (unsafe subdomains skipped)", len(navigator.tlsBundleRequests))
	}
	if entries, err := os.ReadDir(filepath.Join(tlsRoot, "tenant-alias")); err == nil && len(entries) != 0 {
		t.Fatalf("expected no alias dirs for unsafe subdomains, got %v", entries)
	}
}
