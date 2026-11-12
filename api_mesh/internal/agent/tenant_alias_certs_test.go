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
			Instances: []*quartermasterpb.ServiceInstance{{ServiceId: "foghorn", InstanceId: "fh-1", ClusterId: "media-test", Status: "running"}},
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
			"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1"),
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
	if callErr := os.MkdirAll(staleDir, 0o750); callErr != nil {
		t.Fatalf("mkdir stale: %v", callErr)
	}
	if callErr := os.WriteFile(filepath.Join(staleDir, "tls.crt"), []byte("stale"), 0o644); callErr != nil {
		t.Fatalf("write stale: %v", callErr)
	}

	if callErr := agent.syncTenantAliasCertificates(); callErr != nil {
		t.Fatalf("syncTenantAliasCertificates: %v", callErr)
	}

	inventory, err := agent.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	acmeDir := agent.tenantAliasDir(inventory["t-acme"].Path)
	if got, err := os.ReadFile(filepath.Join(acmeDir, "tls.crt")); err != nil || string(got) != navigator.tlsBundles["tenant:t-acme"].CertPem {
		t.Fatalf("acme tls.crt = %q err=%v, want acme-cert", string(got), err)
	}
	if got, err := os.ReadFile(filepath.Join(acmeDir, "tls.key")); err != nil || string(got) != navigator.tlsBundles["tenant:t-acme"].KeyPem {
		t.Fatalf("acme tls.key = %q err=%v, want acme-key", string(got), err)
	}
	if _, callErr := os.Stat(filepath.Join(tlsRoot, "tenant-alias", "globex")); !os.IsNotExist(callErr) {
		t.Fatalf("globex (pending issuance) should have no dir; stat callErr=%v", callErr)
	}
	if _, callErr := os.Stat(staleDir); !os.IsNotExist(callErr) {
		t.Fatalf("stale oldco dir should have been pruned; stat callErr=%v", callErr)
	}
	if _, callErr := os.Stat(trigger); callErr != nil {
		t.Fatalf("expected reload trigger to be touched: %v", callErr)
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
	if callErr := os.MkdirAll(staleDir, 0o750); callErr != nil {
		t.Fatalf("mkdir stale: %v", callErr)
	}

	if callErr := agent.syncTenantAliasCertificates(); callErr != nil {
		t.Fatalf("syncTenantAliasCertificates: %v", callErr)
	}
	if alias.calls != 0 {
		t.Fatalf("ListAliasedTenantsForCluster calls = %d, want 0 on non-foghorn node", alias.calls)
	}
	if _, callErr := os.Stat(staleDir); !os.IsNotExist(callErr) {
		t.Fatalf("non-foghorn node should prune alias certs; stat callErr=%v", callErr)
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

	if callErr := agent.syncTenantAliasCertificates(); callErr != nil {
		t.Fatalf("syncTenantAliasCertificates: %v", callErr)
	}
	if len(navigator.tlsBundleRequests) != 0 {
		t.Fatalf("GetTLSBundle calls = %d, want 0 (unsafe subdomains skipped)", len(navigator.tlsBundleRequests))
	}
	if entries, err := os.ReadDir(filepath.Join(tlsRoot, "tenant-alias", "revisions")); err == nil && len(entries) != 0 {
		t.Fatalf("expected no certificate generations for unsafe subdomains, got %v", entries)
	}
}
