package logic

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"frameworks/api_dns/internal/store"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	getCertFunc                         func(ctx context.Context, tenantID, domain string) (*store.Certificate, error)
	saveCertFunc                        func(ctx context.Context, tenantID string, cert *store.Certificate) error
	getTLSBundleFunc                    func(ctx context.Context, bundleID string) (*store.TLSBundle, error)
	saveTLSBundleFunc                   func(ctx context.Context, bundle *store.TLSBundle) error
	getAccountFunc                      func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error)
	saveAccountFunc                     func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error
	listTenantCustomDomainsFunc         func(ctx context.Context, tenantID string) ([]store.TenantCustomDomain, error)
	setTenantCustomDomainCertMetadataFn func(ctx context.Context, tenantID, domain, issuerID string, certExpiresAt sql.NullTime) error
	saveCertCalled                      int
	saveBundleCalled                    int
	saveAccountCalled                   int
	setCustomDomainMetadataCalled       int
}

func (f *fakeStore) GetCertificate(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
	return f.getCertFunc(ctx, tenantID, domain)
}

func (f *fakeStore) SaveCertificate(ctx context.Context, tenantID string, cert *store.Certificate) error {
	f.saveCertCalled++
	return f.saveCertFunc(ctx, tenantID, cert)
}

func (f *fakeStore) DeleteCertificate(_ context.Context, _, _ string) error {
	return nil
}

func (f *fakeStore) GetTLSBundle(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
	return f.getTLSBundleFunc(ctx, bundleID)
}

func (f *fakeStore) SaveTLSBundle(ctx context.Context, bundle *store.TLSBundle) error {
	f.saveBundleCalled++
	return f.saveTLSBundleFunc(ctx, bundle)
}

func (f *fakeStore) GetACMEAccount(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
	return f.getAccountFunc(ctx, tenantID, email, ca)
}

func (f *fakeStore) SaveACMEAccount(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
	f.saveAccountCalled++
	return f.saveAccountFunc(ctx, tenantID, acc)
}

// Tenant alias methods are outside these cert-focused test paths.
func (f *fakeStore) EnsureTenantAlias(_ context.Context, tenantID, subdomain string) (*store.TenantAlias, error) {
	return &store.TenantAlias{TenantID: tenantID, Subdomain: subdomain, Status: "cert_issuing"}, nil
}

func (f *fakeStore) GetTenantAlias(_ context.Context, _ string) (*store.TenantAlias, error) {
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListPendingTenantAliases(_ context.Context) ([]store.TenantAlias, error) {
	return nil, nil
}

func (f *fakeStore) SetTenantAliasStatus(_ context.Context, _, _, _ string) error {
	return nil
}

func (f *fakeStore) DeleteTenantAlias(_ context.Context, _ string) error {
	return nil
}

func (f *fakeStore) UpsertTenantEdgeApplyState(_ context.Context, _ *store.TenantEdgeApplyState) error {
	return nil
}

func (f *fakeStore) TenantAliasHasDNS(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (f *fakeStore) DeleteTenantEdgeApplyState(_ context.Context, _ string) error {
	return nil
}

func (f *fakeStore) DeleteTenantEdgeApplyStateForCluster(_ context.Context, _, _ string) error {
	return nil
}

func (f *fakeStore) InsertTenantAliasRetirement(_ context.Context, _, _ string) error {
	return nil
}

func (f *fakeStore) ListTenantAliasRetirementLabels(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (f *fakeStore) EnsureTenantCustomDomain(_ context.Context, tenantID, domain, acmeDNSSubdomain string) (*store.TenantCustomDomain, error) {
	return &store.TenantCustomDomain{TenantID: tenantID, Domain: domain, AcmeDNSSubdomain: acmeDNSSubdomain, Status: "pending_verification"}, nil
}

func (f *fakeStore) GetTenantCustomDomain(_ context.Context, _, _ string) (*store.TenantCustomDomain, error) {
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListTenantCustomDomainsByStatus(_ context.Context, _ []string) ([]store.TenantCustomDomain, error) {
	return nil, nil
}

func (f *fakeStore) ListTenantCustomDomains(ctx context.Context, tenantID string) ([]store.TenantCustomDomain, error) {
	if f.listTenantCustomDomainsFunc != nil {
		return f.listTenantCustomDomainsFunc(ctx, tenantID)
	}
	return nil, nil
}

func (f *fakeStore) SetTenantCustomDomainStatus(_ context.Context, _, _, _, _ string) error {
	return nil
}

func (f *fakeStore) SetTenantCustomDomainCertMetadata(ctx context.Context, tenantID, domain, issuerID string, certExpiresAt sql.NullTime) error {
	f.setCustomDomainMetadataCalled++
	if f.setTenantCustomDomainCertMetadataFn != nil {
		return f.setTenantCustomDomainCertMetadataFn(ctx, tenantID, domain, issuerID, certExpiresAt)
	}
	return nil
}

func (f *fakeStore) DeleteTenantCustomDomain(_ context.Context, _, _ string) error {
	return nil
}

type fakeDNSProvider struct {
	presentCalls int
	cleanupCalls int
	presentErr   error
	cleanupErr   error
}

func (f *fakeDNSProvider) Present(domain, token, keyAuth string) error {
	f.presentCalls++
	return f.presentErr
}

func (f *fakeDNSProvider) CleanUp(domain, token, keyAuth string) error {
	f.cleanupCalls++
	return f.cleanupErr
}

type fakeACMEClient struct {
	provider        challenge.Provider
	registerCalled  int
	obtainCalled    int
	obtainedDomains []string
	registerErr     error
	obtainErr       error
	resource        *certificate.Resource
}

func (f *fakeACMEClient) SetDNS01Provider(provider challenge.Provider) error {
	f.provider = provider
	return nil
}

func (f *fakeACMEClient) Register() (*registration.Resource, error) {
	f.registerCalled++
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	return &registration.Resource{URI: "acct-1"}, nil
}

func (f *fakeACMEClient) RegisterWithEAB(_ registration.RegisterEABOptions) (*registration.Resource, error) {
	f.registerCalled++
	if f.registerErr != nil {
		return nil, f.registerErr
	}
	return &registration.Resource{URI: "acct-1-eab"}, nil
}

func (f *fakeACMEClient) Obtain(request certificate.ObtainRequest) (*certificate.Resource, error) {
	f.obtainCalled++
	f.obtainedDomains = append([]string(nil), request.Domains...)
	if f.provider != nil {
		if err := f.provider.Present(request.Domains[0], "token", "keyAuth"); err != nil {
			return nil, err
		}
	}

	if f.obtainErr != nil {
		if f.provider != nil {
			_ = f.provider.CleanUp(request.Domains[0], "token", "keyAuth")
		}
		return nil, f.obtainErr
	}

	if f.provider != nil {
		_ = f.provider.CleanUp(request.Domains[0], "token", "keyAuth")
	}

	return f.resource, nil
}

func TestIssueCertificateSetsUpAndCleansUpChallenges(t *testing.T) {
	ctx := context.Background()
	notAfter := time.Now().Add(10 * time.Hour)
	certPEM, keyPEM := buildTestCert(t, notAfter)

	provider := &fakeDNSProvider{}
	acme := &fakeACMEClient{
		resource: &certificate.Resource{
			Certificate: certPEM,
			PrivateKey:  keyPEM,
		},
	}
	fakeStore := &fakeStore{
		getCertFunc: func(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
			return nil, store.ErrNotFound
		},
		saveCertFunc: func(ctx context.Context, tenantID string, cert *store.Certificate) error {
			return nil
		},
		getTLSBundleFunc: func(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
			return nil, store.ErrNotFound
		},
		saveTLSBundleFunc: func(ctx context.Context, bundle *store.TLSBundle) error {
			return nil
		},
		getAccountFunc: func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
			return nil
		},
	}

	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(config *lego.Config) (acmeClient, error) {
		return acme, nil
	}
	manager.dnsProviderFactory = func() (challenge.Provider, error) {
		return provider, nil
	}

	returnedCert, returnedKey, expiresAt, err := manager.IssueCertificate(ctx, "", "example.com", "me@example.com")
	require.NoError(t, err)
	require.Equal(t, string(certPEM), returnedCert)
	require.Equal(t, string(keyPEM), returnedKey)
	require.WithinDuration(t, notAfter, expiresAt, time.Second)
	require.Equal(t, 1, provider.presentCalls)
	require.Equal(t, 1, provider.cleanupCalls)
	require.Equal(t, 1, acme.registerCalled)
	require.Equal(t, 1, acme.obtainCalled)
	require.Equal(t, 1, fakeStore.saveAccountCalled)
	require.Equal(t, 1, fakeStore.saveCertCalled)
}

func TestEnsureClusterWildcardCertificateCreatesApexAndWildcardBundle(t *testing.T) {
	ctx := context.Background()
	notAfter := time.Now().Add(48 * time.Hour)
	certPEM, keyPEM := buildTestCert(t, notAfter)

	provider := &fakeDNSProvider{}
	acme := &fakeACMEClient{
		resource: &certificate.Resource{
			Certificate: certPEM,
			PrivateKey:  keyPEM,
		},
	}

	var saved *store.TLSBundle
	fakeStore := &fakeStore{
		getCertFunc: func(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
			return nil, store.ErrNotFound
		},
		saveCertFunc: func(ctx context.Context, tenantID string, cert *store.Certificate) error {
			return nil
		},
		getTLSBundleFunc: func(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
			require.Equal(t, "cluster:media-eu-1", bundleID)
			return nil, store.ErrNotFound
		},
		saveTLSBundleFunc: func(ctx context.Context, bundle *store.TLSBundle) error {
			saved = bundle
			return nil
		},
		getAccountFunc: func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
			return nil
		},
	}

	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(config *lego.Config) (acmeClient, error) {
		return acme, nil
	}
	manager.dnsProviderFactory = func() (challenge.Provider, error) {
		return provider, nil
	}

	bundle, err := manager.EnsureClusterWildcardCertificate(ctx, "media-eu-1", "frameworks.network", "ops@frameworks.network")
	require.NoError(t, err)
	require.NotNil(t, bundle)
	require.Equal(t, "cluster:media-eu-1", bundle.BundleID)
	require.Equal(t, []string{"*.media-eu-1.frameworks.network", "media-eu-1.frameworks.network"}, bundle.Domains)
	require.Equal(t, bundle, saved)
	require.Equal(t, []string{"*.media-eu-1.frameworks.network", "media-eu-1.frameworks.network"}, acme.obtainedDomains)
}

func TestHasClusterWildcardCertChecksClusterBundle(t *testing.T) {
	ctx := context.Background()
	fakeStore := &fakeStore{
		getCertFunc: func(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
			t.Fatalf("HasClusterWildcardCert should not read legacy single-domain certificates")
			return nil, nil
		},
		saveCertFunc: func(ctx context.Context, tenantID string, cert *store.Certificate) error {
			return nil
		},
		getTLSBundleFunc: func(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
			require.Equal(t, "cluster:media-eu-1", bundleID)
			return &store.TLSBundle{
				BundleID:  bundleID,
				Domains:   []string{"media-eu-1.frameworks.network", "*.media-eu-1.frameworks.network"},
				ExpiresAt: time.Now().Add(time.Hour),
			}, nil
		},
		saveTLSBundleFunc: func(ctx context.Context, bundle *store.TLSBundle) error {
			return nil
		},
		getAccountFunc: func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
			return nil
		},
	}

	manager := NewCertManager(fakeStore)
	require.True(t, manager.HasClusterWildcardCert(ctx, "media-eu-1", "frameworks.network"))
}

func TestIssueCertificateFailureDoesNotPersistCertificate(t *testing.T) {
	ctx := context.Background()
	provider := &fakeDNSProvider{}
	acme := &fakeACMEClient{
		obtainErr: errors.New("acme boom"),
	}
	fakeStore := &fakeStore{
		getCertFunc: func(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
			return nil, store.ErrNotFound
		},
		saveCertFunc: func(ctx context.Context, tenantID string, cert *store.Certificate) error {
			return nil
		},
		getTLSBundleFunc: func(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
			return nil, store.ErrNotFound
		},
		saveTLSBundleFunc: func(ctx context.Context, bundle *store.TLSBundle) error {
			return nil
		},
		getAccountFunc: func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
			return nil
		},
	}

	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(config *lego.Config) (acmeClient, error) {
		return acme, nil
	}
	manager.dnsProviderFactory = func() (challenge.Provider, error) {
		return provider, nil
	}

	_, _, _, err := manager.IssueCertificate(ctx, "", "example.com", "me@example.com")
	require.Error(t, err)
	require.Equal(t, 1, provider.presentCalls)
	require.Equal(t, 1, provider.cleanupCalls)
	require.Equal(t, 0, fakeStore.saveCertCalled)
}

func TestIssueCertificateToleratesExistingCloudflareChallengeRecord(t *testing.T) {
	ctx := context.Background()
	notAfter := time.Now().Add(10 * time.Hour)
	certPEM, keyPEM := buildTestCert(t, notAfter)

	provider := &fakeDNSProvider{
		presentErr: errors.New("cloudflare: failed to create TXT record: [status code 400] 81058: An identical record already exists"),
	}
	acme := &fakeACMEClient{
		resource: &certificate.Resource{
			Certificate: certPEM,
			PrivateKey:  keyPEM,
		},
	}
	fakeStore := &fakeStore{
		getCertFunc: func(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
			return nil, store.ErrNotFound
		},
		saveCertFunc: func(ctx context.Context, tenantID string, cert *store.Certificate) error {
			return nil
		},
		getTLSBundleFunc: func(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
			return nil, store.ErrNotFound
		},
		saveTLSBundleFunc: func(ctx context.Context, bundle *store.TLSBundle) error {
			return nil
		},
		getAccountFunc: func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
			return nil
		},
	}

	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(config *lego.Config) (acmeClient, error) {
		return acme, nil
	}
	manager.dnsProviderFactory = func() (challenge.Provider, error) {
		return provider, nil
	}

	_, _, _, err := manager.IssueCertificate(ctx, "", "example.com", "me@example.com")
	require.NoError(t, err)
	require.Equal(t, 1, provider.presentCalls)
	require.Equal(t, 1, fakeStore.saveCertCalled)
}

func TestResilientDNSProviderToleratesUnknownCloudflareCleanupRecord(t *testing.T) {
	provider := &fakeDNSProvider{
		cleanupErr: errors.New("cloudflare: unknown record ID for '_acme-challenge.example.com.'"),
	}
	wrapped := &resilientDNSProvider{provider: provider}

	require.NoError(t, wrapped.CleanUp("example.com", "token", "keyAuth"))
	require.Equal(t, 1, provider.cleanupCalls)
}

func TestEnsureTLSBundleObtainsAndPersistsBundle(t *testing.T) {
	ctx := context.Background()
	notAfter := time.Now().Add(24 * time.Hour)
	certPEM, keyPEM := buildTestCert(t, notAfter)

	provider := &fakeDNSProvider{}
	acme := &fakeACMEClient{
		resource: &certificate.Resource{
			Certificate: certPEM,
			PrivateKey:  keyPEM,
		},
	}
	fakeStore := &fakeStore{
		getCertFunc: func(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
			return nil, store.ErrNotFound
		},
		saveCertFunc: func(ctx context.Context, tenantID string, cert *store.Certificate) error {
			return nil
		},
		getTLSBundleFunc: func(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
			return nil, store.ErrNotFound
		},
		saveTLSBundleFunc: func(ctx context.Context, bundle *store.TLSBundle) error {
			return nil
		},
		getAccountFunc: func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
			return nil
		},
	}

	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(config *lego.Config) (acmeClient, error) {
		return acme, nil
	}
	manager.dnsProviderFactory = func() (challenge.Provider, error) {
		return provider, nil
	}

	bundle, err := manager.EnsureTLSBundle(ctx, "wildcard-frameworks-network", []string{"*.frameworks.network", "*.frameworks.network"}, "ops@frameworks.network")
	require.NoError(t, err)
	require.Equal(t, "wildcard-frameworks-network", bundle.BundleID)
	require.Equal(t, []string{"*.frameworks.network"}, bundle.Domains)
	require.WithinDuration(t, notAfter, bundle.ExpiresAt, time.Second)
	require.Equal(t, 1, fakeStore.saveBundleCalled)
	require.Equal(t, 1, provider.presentCalls)
	require.Equal(t, 1, provider.cleanupCalls)
}

func TestEnsureTLSBundleRenewsTenantCustomDomainBundleWithBunnyProvider(t *testing.T) {
	ctx := context.Background()
	t.Setenv("NAVIGATOR_CERT_ALLOWED_SUFFIXES", "frameworks.network")
	notAfter := time.Now().Add(48 * time.Hour)
	certPEM, keyPEM := buildTestCert(t, notAfter)

	standardProvider := &fakeDNSProvider{}
	bunnyProvider := &fakeDNSProvider{}
	acme := &fakeACMEClient{
		resource: &certificate.Resource{
			Certificate: certPEM,
			PrivateKey:  keyPEM,
		},
	}
	fakeStore := &fakeStore{
		getCertFunc: func(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
			return nil, store.ErrNotFound
		},
		saveCertFunc: func(ctx context.Context, tenantID string, cert *store.Certificate) error {
			return nil
		},
		getTLSBundleFunc: func(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
			return nil, store.ErrNotFound
		},
		saveTLSBundleFunc: func(ctx context.Context, bundle *store.TLSBundle) error {
			return nil
		},
		getAccountFunc: func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
			return nil
		},
		listTenantCustomDomainsFunc: func(ctx context.Context, tenantID string) ([]store.TenantCustomDomain, error) {
			require.Equal(t, "tenant-123", tenantID)
			return []store.TenantCustomDomain{{TenantID: tenantID, Domain: "media.example.com", Status: "cert_issued"}}, nil
		},
		setTenantCustomDomainCertMetadataFn: func(ctx context.Context, tenantID, domain, issuerID string, certExpiresAt sql.NullTime) error {
			require.Equal(t, "tenant-123", tenantID)
			require.Equal(t, "media.example.com", domain)
			require.NotEmpty(t, issuerID)
			require.True(t, certExpiresAt.Valid)
			return nil
		},
	}

	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(config *lego.Config) (acmeClient, error) {
		return acme, nil
	}
	manager.dnsProviderFactory = func() (challenge.Provider, error) {
		return standardProvider, nil
	}
	manager.bunnyDNSProviderFactory = func() (challenge.Provider, error) {
		return bunnyProvider, nil
	}

	bundle, err := manager.EnsureTLSBundle(ctx, "tenant:tenant-123", []string{
		"acme.cdn.frameworks.network",
		"*.acme.cdn.frameworks.network",
		"media.example.com",
	}, "ops@frameworks.network")
	require.NoError(t, err)
	require.Equal(t, "tenant:tenant-123", bundle.BundleID)
	require.Equal(t, 0, standardProvider.presentCalls)
	require.Equal(t, 1, bunnyProvider.presentCalls)
	require.Equal(t, 1, fakeStore.setCustomDomainMetadataCalled)
}

func TestIssueCustomDomainCertificateIssuesRequestedDomain(t *testing.T) {
	ctx := context.Background()
	notAfter := time.Now().Add(48 * time.Hour)
	certPEM, keyPEM := buildTestCert(t, notAfter)

	bunnyProvider := &fakeDNSProvider{}
	acme := &fakeACMEClient{
		resource: &certificate.Resource{
			Certificate: certPEM,
			PrivateKey:  keyPEM,
		},
	}
	fakeStore := &fakeStore{
		getCertFunc: func(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
			require.Equal(t, "tenant-123", tenantID)
			require.Equal(t, "media.example.com", domain)
			return nil, store.ErrNotFound
		},
		saveCertFunc: func(ctx context.Context, tenantID string, cert *store.Certificate) error {
			require.Equal(t, "tenant-123", tenantID)
			require.Equal(t, "media.example.com", cert.Domain)
			require.NotEmpty(t, cert.IssuerCA)
			return nil
		},
		getAccountFunc: func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
			return nil
		},
		setTenantCustomDomainCertMetadataFn: func(ctx context.Context, tenantID, domain, issuerID string, certExpiresAt sql.NullTime) error {
			require.Equal(t, "tenant-123", tenantID)
			require.Equal(t, "media.example.com", domain)
			require.NotEmpty(t, issuerID)
			require.True(t, certExpiresAt.Valid)
			return nil
		},
	}

	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(config *lego.Config) (acmeClient, error) {
		return acme, nil
	}
	manager.bunnyDNSProviderFactory = func() (challenge.Provider, error) {
		return bunnyProvider, nil
	}

	err := manager.IssueCustomDomainCertificate(ctx, store.TenantCustomDomain{
		TenantID: "tenant-123",
		Domain:   "media.example.com",
		Status:   "verified",
	}, "frameworks.network", "ops@frameworks.network")
	require.NoError(t, err)
	require.Equal(t, []string{"media.example.com"}, acme.obtainedDomains)
	require.Equal(t, 1, bunnyProvider.presentCalls)
	require.Equal(t, 1, fakeStore.saveCertCalled)
	require.Equal(t, 1, fakeStore.setCustomDomainMetadataCalled)
}

func TestCertificateNeedsBunnyProvider(t *testing.T) {
	tests := []struct {
		name    string
		domains []string
		want    bool
	}{
		{name: "cluster wildcard", domains: []string{"*.media-eu.frameworks.network"}, want: true},
		{name: "cluster bundle", domains: []string{"media-eu.frameworks.network", "*.media-eu.frameworks.network"}, want: true},
		{name: "media service name under cluster zone", domains: []string{"livepeer.media-eu.frameworks.network"}, want: true},
		{name: "root wildcard stays cloudflare", domains: []string{"*.frameworks.network"}, want: false},
		{name: "root apex stays cloudflare", domains: []string{"frameworks.network"}, want: false},
		{name: "operator service stays cloudflare", domains: []string{"bridge.frameworks.network"}, want: false},
		{name: "operator grafana stays cloudflare", domains: []string{"grafana.frameworks.network"}, want: false},
		{name: "nested wildcard under media cluster zone", domains: []string{"*.edge.media-eu.frameworks.network"}, want: true},
		{name: "pool-assigned global foghorn", domains: []string{"foghorn.frameworks.network"}, want: true},
		{name: "pool-assigned global chandler", domains: []string{"chandler.frameworks.network"}, want: true},
		{name: "pool-assigned global livepeer", domains: []string{"livepeer.frameworks.network"}, want: true},
		{name: "platform-edge global edge", domains: []string{"edge.frameworks.network"}, want: true},
		{name: "platform-edge global edge-ingest", domains: []string{"edge-ingest.frameworks.network"}, want: true},
		{name: "platform-edge multi-SAN", domains: []string{"edge.frameworks.network", "edge-ingest.frameworks.network", "edge-egress.frameworks.network"}, want: true},
		{name: "tenant cdn wildcard", domains: []string{"*.cdn.frameworks.network"}, want: true},
		{name: "tenant cdn apex", domains: []string{"acme.cdn.frameworks.network", "*.acme.cdn.frameworks.network"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, certificateNeedsBunnyProvider(tt.domains, "frameworks.network"))
		})
	}
}

func TestUseBunnyForClusterZonesSelectsProviderByDelegatedZone(t *testing.T) {
	ctx := context.Background()
	t.Setenv("BRAND_DOMAIN", "frameworks.network")
	notAfter := time.Now().Add(10 * time.Hour)
	certPEM, keyPEM := buildTestCert(t, notAfter)

	cloudflareProvider := &fakeDNSProvider{}
	bunnyProvider := &fakeDNSProvider{}
	acme := &fakeACMEClient{
		resource: &certificate.Resource{
			Certificate: certPEM,
			PrivateKey:  keyPEM,
		},
	}
	fakeStore := &fakeStore{
		getCertFunc: func(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
			return nil, store.ErrNotFound
		},
		saveCertFunc: func(ctx context.Context, tenantID string, cert *store.Certificate) error {
			return nil
		},
		getTLSBundleFunc: func(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
			return nil, store.ErrNotFound
		},
		saveTLSBundleFunc: func(ctx context.Context, bundle *store.TLSBundle) error {
			return nil
		},
		getAccountFunc: func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
			return nil
		},
	}

	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(config *lego.Config) (acmeClient, error) {
		return acme, nil
	}
	manager.dnsProviderFactory = func() (challenge.Provider, error) {
		return cloudflareProvider, nil
	}
	manager.bunnyDNSProviderFactory = func() (challenge.Provider, error) {
		return bunnyProvider, nil
	}
	manager.UseBunnyForClusterZones("frameworks.network")

	_, _, _, err := manager.IssueCertificate(ctx, "", "livepeer.media-eu.frameworks.network", "ops@frameworks.network")
	require.NoError(t, err)
	require.Equal(t, 0, cloudflareProvider.presentCalls)
	require.Equal(t, 1, bunnyProvider.presentCalls)
}

func buildTestCert(t *testing.T, notAfter time.Time) ([]byte, []byte) {
	t.Helper()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: serial,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		DNSNames:     []string{"example.com"},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyBytes, err := x509.MarshalECPrivateKey(privateKey)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return certPEM, keyPEM
}
