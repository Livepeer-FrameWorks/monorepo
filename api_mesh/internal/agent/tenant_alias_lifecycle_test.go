package agent

import (
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// Locate the published pair through the serving inventory, with the legacy
// layout as a comparison boundary. These assertions run on both implementations.
func publishedAliasPair(a *Agent, sub string) (string, string, error) {
	root := filepath.Join(a.ingressTLSRoot, "tenant-alias")
	mapping, err := os.ReadFile(filepath.Join(root, "nginx.map"))
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Join(root, sub, "tls.crt"), filepath.Join(root, sub, "tls.key"), nil
	}
	if err != nil {
		return "", "", err
	}
	host := "foghorn." + sub + ".cdn.example.test"
	for _, line := range strings.Split(string(mapping), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == host {
			dir := filepath.Join(root, strings.TrimSuffix(fields[1], ";"))
			return filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key"), nil
		}
	}
	return "", "", os.ErrNotExist
}

func TestTenantAliasLifecycleDiscoversNewTenantBeforeRenewal(t *testing.T) {
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1"), "tenant:t-globex": aliasTestBundle(t, "t-globex", "globex", "v1")}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	a.certSyncInterval = time.Hour
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	aliases.resp.Tenants = append(aliases.resp.Tenants, &quartermasterpb.AliasedTenantRef{TenantId: "t-globex", Subdomain: "globex"})
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	cert, _, err := publishedAliasPair(a, "globex")
	if err != nil {
		t.Fatalf("new tenant blocked by existing certificate refresh interval: %v", err)
	}
	if _, callErr := os.Stat(cert); callErr != nil {
		t.Fatalf("new tenant blocked by existing certificate refresh interval: %v", callErr)
	}
}

func TestTenantAliasLifecycleRepairsMissingKeyAtSameVersion(t *testing.T) {
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1")}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	a.certSyncInterval = time.Hour
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	_, key, err := publishedAliasPair(a, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if callErr := os.Remove(key); callErr != nil {
		t.Fatal(callErr)
	}
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	if _, callErr := os.Stat(key); callErr != nil {
		t.Fatalf("matching bundle version hid a missing serving key: %v", callErr)
	}
}

func TestTenantAliasLifecycleRejectsMismatchedRotation(t *testing.T) {
	first := aliasTestBundle(t, "t-acme", "acme", "v1")
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": first}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	second := aliasTestBundle(t, "t-acme", "acme", "v2")
	second.KeyPem = first.KeyPem
	navigator.tlsBundles["tenant:t-acme"] = second
	if callErr := a.syncTenantAliasCertificates(); callErr == nil {
		t.Fatal("invalid rotated pair displaced valid serving material without an error")
	}
	cert, key, err := publishedAliasPair(a, "acme")
	if err != nil {
		t.Fatal(err)
	}
	if _, callErr := tls.LoadX509KeyPair(cert, key); callErr != nil {
		t.Fatalf("invalid rotation destroyed the last good pair: %v", callErr)
	}
}

func TestTenantAliasLifecyclePreservesServingStateOnIncompleteRegistryPage(t *testing.T) {
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1")}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	cert, key, err := publishedAliasPair(a, "acme")
	if err != nil {
		t.Fatal(err)
	}
	a.registryClient = &fakeServiceRegistryClient{responses: []*quartermasterpb.ListServiceInstancesResponse{{Pagination: &commonpb.CursorPaginationResponse{HasNextPage: true}}}}
	if callErr := a.syncTenantAliasCertificates(); callErr == nil {
		t.Fatal("incomplete registry page was accepted as authoritative absence")
	}
	if _, callErr := tls.LoadX509KeyPair(cert, key); callErr != nil {
		t.Fatalf("incomplete inventory removed a serving certificate: %v", callErr)
	}
}
