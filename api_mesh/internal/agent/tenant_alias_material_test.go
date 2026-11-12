package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func TestTenantAliasCertificatesRotationPinsImmutablePairs(t *testing.T) {
	first := aliasTestBundle(t, "t-acme", "acme", "v1")
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": first}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	before, err := a.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	old := before["t-acme"]
	second := aliasTestBundle(t, "t-acme", "acme", "v2")
	navigator.tlsBundles["tenant:t-acme"] = second
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	after, err := a.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	if after["t-acme"].Path == old.Path {
		t.Fatal("rotation overwrote an active cert/key generation")
	}
	if !a.aliasCertificateUsable(old, time.Now()) || !a.aliasCertificateUsable(after["t-acme"], time.Now()) {
		t.Fatal("an existing worker lost its pinned certificate pair")
	}
	mapping, err := os.ReadFile(a.aliasMapPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(mapping), old.Path) || !strings.Contains(string(mapping), after["t-acme"].Path) {
		t.Fatalf("SNI map does not select only the new immutable generation: %s", mapping)
	}
	second.KeyPem = first.KeyPem
	if callErr := a.syncTenantAliasCertificates(); callErr == nil {
		t.Fatal("mismatched replacement pair was accepted")
	}
	preserved, err := a.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	if preserved["t-acme"] != after["t-acme"] {
		t.Fatal("invalid replacement displaced last-good serving authority")
	}
}

func TestTenantAliasCertificatesRepairMissingMaterialAtSameVersion(t *testing.T) {
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1")}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	a.certSyncInterval = time.Hour
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	before, err := a.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	if callErr := os.Remove(filepath.Join(a.tenantAliasDir(before["t-acme"].Path), "tls.key")); callErr != nil {
		t.Fatal(callErr)
	}
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	if !a.aliasCertificateUsable(before["t-acme"], time.Now()) {
		t.Fatal("matching version memo hid a missing private key")
	}
}

func TestTenantAliasCertificatesPreserveSANAuthorityDuringRename(t *testing.T) {
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "old", "v1")}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "new"}}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	before, err := a.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	if before["t-acme"].Subdomain != "old" {
		t.Fatal("last-good certificate was advertised for an uncovered replacement hostname")
	}
	navigator.tlsBundles["tenant:t-acme"] = aliasTestBundle(t, "t-acme", "new", "v2")
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	after, err := a.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	if after["t-acme"].Subdomain != "new" {
		t.Fatal("issued replacement SAN authority did not become active")
	}
}

func TestTenantAliasCertificatesKeepInventoryOnIncompleteScopeRead(t *testing.T) {
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1")}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), &fakeAliasedTenantsClient{}, navigator)
	registry := stableAliasRegistry()
	registry.instances[0].ClusterId = "media-us-1"
	registry.assignments = map[string][]string{"fh-1": {"private-a"}}
	a.registryClient = registry
	aliases := &scopedAliasClient{}
	a.aliasClient = aliases
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	before, err := os.ReadFile(a.aliasMapPath())
	if err != nil {
		t.Fatal(err)
	}
	aliases.fail = "private-a"
	if callErr := a.syncTenantAliasCertificates(); callErr == nil {
		t.Fatal("partial media inventory was treated as an authoritative empty view")
	}
	after, err := os.ReadFile(a.aliasMapPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("partial inventory removed previously serving tenants")
	}
}

func TestTenantAliasCertificatesDiscoverNewTenantDuringRefreshInterval(t *testing.T) {
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1"), "tenant:t-globex": aliasTestBundle(t, "t-globex", "globex", "v1")}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	a.certSyncInterval = time.Hour
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	a.lastAliasSync.Store(time.Now().Add(-30 * time.Second).Unix())
	firstRefresh := a.lastAliasSync.Load()
	aliases.resp.Tenants = append(aliases.resp.Tenants, &quartermasterpb.AliasedTenantRef{TenantId: "t-globex", Subdomain: "globex"})
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	inventory, err := a.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	if inventory["t-globex"].Host == "" {
		t.Fatal("new tenant waited behind the existing certificates' refresh interval")
	}
	if a.lastAliasSync.Load() != firstRefresh {
		t.Fatal("discovery extended the full renewal refresh interval")
	}
	aliases.resp.Tenants = aliases.resp.Tenants[:1]
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	inventory, err = a.loadAliasInventory()
	if err != nil {
		t.Fatal(err)
	}
	if _, present := inventory["t-globex"]; present {
		t.Fatal("withdrawn tenant remained in the serving inventory until certificate refresh")
	}
}

func TestTenantAliasCertificatesWaitForExactIngressApplyReceipt(t *testing.T) {
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1")}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, root, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	a.aliasAppliedPath = filepath.Join(root, "applied")
	if callErr := a.syncTenantAliasCertificates(); !errors.Is(callErr, ErrTenantAliasIngressPending) {
		t.Fatalf("written files were reported applied before ingress acknowledgement: %v", callErr)
	}
	if a.IsTenantAliasIngressApplied() {
		t.Fatal("hosted ingress reports applied before its receipt")
	}
	mapping, err := os.ReadFile(a.aliasMapPath())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(mapping)
	if callErr := os.WriteFile(a.aliasAppliedPath, []byte(hex.EncodeToString(digest[:])+"\n"), 0o644); callErr != nil {
		t.Fatal(callErr)
	}
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	if !a.IsTenantAliasIngressApplied() {
		t.Fatal("matching apply receipt was ignored")
	}
	navigator.tlsBundles["tenant:t-acme"] = aliasTestBundle(t, "t-acme", "acme", "v2")
	if callErr := a.syncTenantAliasCertificates(); !errors.Is(callErr, ErrTenantAliasIngressPending) {
		t.Fatalf("old receipt accepted a new certificate map: %v", callErr)
	}
	if a.IsTenantAliasIngressApplied() {
		t.Fatal("rotation reused an earlier ingress apply receipt")
	}
}

func TestTenantAliasCertificatesDoNotRaceAnIngressReload(t *testing.T) {
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": aliasTestBundle(t, "t-acme", "acme", "v1")}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, _, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
	before, err := os.ReadFile(a.aliasMapPath())
	if err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(a.tenantAliasDir(""))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if callErr := syscall.Flock(int(reader.Fd()), syscall.LOCK_SH); callErr != nil {
		t.Fatal(callErr)
	}
	navigator.tlsBundles["tenant:t-acme"] = aliasTestBundle(t, "t-acme", "acme", "v2")
	if callErr := a.syncTenantAliasCertificates(); !errors.Is(callErr, ErrTenantAliasIngressPending) {
		t.Fatalf("publication did not defer behind the reloader's pinned map: %v", callErr)
	}
	after, err := os.ReadFile(a.aliasMapPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("inventory changed while ingress was loading its generation")
	}
	if callErr := syscall.Flock(int(reader.Fd()), syscall.LOCK_UN); callErr != nil {
		t.Fatal(callErr)
	}
	if callErr := a.syncTenantAliasCertificates(); callErr != nil {
		t.Fatal(callErr)
	}
}

func TestTenantAliasCertificatesDoNotHideFailuresBehindPendingApply(t *testing.T) {
	invalid := aliasTestBundle(t, "t-acme", "acme", "v1")
	invalid.KeyPem = "invalid key"
	navigator := &fakeCertificateClient{tlsBundles: map[string]*dnspb.GetTLSBundleResponse{"tenant:t-acme": invalid}}
	aliases := &fakeAliasedTenantsClient{resp: &quartermasterpb.ListAliasedTenantsForClusterResponse{Tenants: []*quartermasterpb.AliasedTenantRef{{TenantId: "t-acme", Subdomain: "acme"}}}}
	a, root, _ := newAliasTestAgent(t, foghornRegistry(), aliases, navigator)
	a.registryClient = stableAliasRegistry()
	a.aliasAppliedPath = filepath.Join(root, "applied")
	err := a.syncTenantAliasCertificates()
	if err == nil || errors.Is(err, ErrTenantAliasIngressPending) {
		t.Fatalf("invalid certificate was classified as a normal ingress wait: %v", err)
	}
}
