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
	"slices"
	"sort"
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
	getCertFunc                  func(ctx context.Context, tenantID, domain string) (*store.Certificate, error)
	saveCertFunc                 func(ctx context.Context, tenantID string, cert *store.Certificate) error
	getTLSBundleFunc             func(ctx context.Context, bundleID string) (*store.TLSBundle, error)
	getTLSBundleForIssuanceFunc  func(ctx context.Context, bundleID string) (*store.TLSBundle, error)
	saveTLSBundleFunc            func(ctx context.Context, bundle *store.TLSBundle) error
	getAccountFunc               func(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error)
	saveAccountFunc              func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error
	listTenantCustomDomainsFunc  func(ctx context.Context, tenantID string) ([]store.TenantCustomDomain, error)
	upsertTenantEdgeApplyAckFunc func(ctx context.Context, state *store.TenantEdgeApplyState) (store.TenantEdgeApplyAckOutcome, error)
	getTenantAliasFunc           func(ctx context.Context, tenantID string) (*store.TenantAlias, error)
	saveCertCalled               int
	saveBundleCalled             int
	saveAccountCalled            int
	issuanceLeaseHeld            bool
	issuanceLeaseOwners          []string
	issuanceLeaseRenewed         bool
	renewIssuanceLeaseFunc       func() (bool, error)
	// customDomains, keyed by domain, backs the custom-domain lifecycle with
	// the same transition fences as the SQL queries. Nil leaves those methods
	// as permissive no-ops for tests outside the lifecycle.
	customDomains              map[string]*store.TenantCustomDomain
	servedCertificateRefreshes int
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

func (f *fakeStore) GetTLSBundleForIssuance(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
	if f.getTLSBundleForIssuanceFunc != nil {
		return f.getTLSBundleForIssuanceFunc(ctx, bundleID)
	}
	return f.getTLSBundleFunc(ctx, bundleID)
}

func (f *fakeStore) SaveTLSBundle(ctx context.Context, bundle *store.TLSBundle) error {
	f.saveBundleCalled++
	return f.saveTLSBundleFunc(ctx, bundle)
}

func (f *fakeStore) TryAcquireCertificateIssuanceLease(_ context.Context, _, owner string, _ time.Duration) (bool, error) {
	f.issuanceLeaseOwners = append(f.issuanceLeaseOwners, owner)
	if f.issuanceLeaseHeld {
		return false, nil
	}
	f.issuanceLeaseHeld = true
	return true, nil
}

func (f *fakeStore) RenewCertificateIssuanceLease(_ context.Context, _, _ string, _ time.Duration) (bool, error) {
	if f.renewIssuanceLeaseFunc != nil {
		return f.renewIssuanceLeaseFunc()
	}
	if !f.issuanceLeaseHeld {
		return false, nil
	}
	f.issuanceLeaseRenewed = true
	return true, nil
}

func TestIssuanceLeaseOwnerIsUniquePerAttempt(t *testing.T) {
	fakeStore := &fakeStore{}
	manager := NewCertManager(fakeStore)

	_, releaseFirst, err := manager.acquireIssuanceLease(context.Background(), "tls-bundle:tenant:tenant-123")
	require.NoError(t, err)
	releaseFirst()
	_, releaseSecond, err := manager.acquireIssuanceLease(context.Background(), "tls-bundle:tenant:tenant-123")
	require.NoError(t, err)
	releaseSecond()

	require.Len(t, fakeStore.issuanceLeaseOwners, 2)
	require.NotEqual(t, fakeStore.issuanceLeaseOwners[0], fakeStore.issuanceLeaseOwners[1])
}

func TestIssuanceLeaseMustStillBeOwnedBeforePublish(t *testing.T) {
	fakeStore := &fakeStore{}
	manager := NewCertManager(fakeStore)
	owner, release, err := manager.acquireIssuanceLease(context.Background(), "tls-bundle:tenant:tenant-123")
	require.NoError(t, err)
	defer release()

	fakeStore.issuanceLeaseHeld = false
	err = manager.renewIssuanceLeaseForPublish(context.Background(), "tls-bundle:tenant:tenant-123", owner)
	require.ErrorIs(t, err, store.ErrIssuanceInProgress)
}

func TestSuccessfulACMEOrderCannotPublishAfterIssuanceLeaseExpires(t *testing.T) {
	certPEM, keyPEM := buildTestCert(t, time.Now().Add(48*time.Hour))
	fakeStore := &fakeStore{
		getTLSBundleFunc: func(context.Context, string) (*store.TLSBundle, error) { return nil, store.ErrNotFound },
		getAccountFunc: func(context.Context, string, string, string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(context.Context, string, *store.ACMEAccount) error { return nil },
		saveTLSBundleFunc: func(context.Context, *store.TLSBundle) error {
			t.Fatal("bundle published after issuance lease was lost")
			return nil
		},
		renewIssuanceLeaseFunc: func() (bool, error) { return false, nil },
	}
	manager := NewCertManager(fakeStore)
	manager.SetIssuanceSettings(func() IssuanceSettings { return IssuanceSettings{AllowedSuffixes: "example.com"} })
	manager.acmeClientFactory = func(*lego.Config) (acmeClient, error) {
		return &fakeACMEClient{resource: &certificate.Resource{Certificate: certPEM, PrivateKey: keyPEM}}, nil
	}
	manager.dnsProviderFactory = func() (challenge.Provider, error) { return &fakeDNSProvider{}, nil }

	_, err := manager.EnsureTLSBundle(context.Background(), "platform:test", []string{"media.example.com"}, "ops@example.com")
	require.ErrorIs(t, err, store.ErrIssuanceInProgress)
	require.Equal(t, 0, fakeStore.saveBundleCalled)
}

func TestTenantBundleSANsIncludeOnlyIssuingAndIssuedCustomDomains(t *testing.T) {
	fakeStore := &fakeStore{
		listTenantCustomDomainsFunc: func(context.Context, string) ([]store.TenantCustomDomain, error) {
			return []store.TenantCustomDomain{
				{Domain: "pending.example.test", Status: "pending_verification"},
				{Domain: "verified.example.test", Status: "verified"},
				{Domain: "waiting.example.test", Status: "pending_alias"},
				{Domain: "issuing.example.test", Status: "cert_issuing"},
				{Domain: "issued.example.test", Status: "cert_issued"},
				{Domain: "failed.example.test", Status: "cert_failed"},
				{Domain: "removed.example.test", Status: "tearing_down"},
			}, nil
		},
	}
	domains, err := NewCertManager(fakeStore).verifiedCustomDomainsForTenant(context.Background(), "tenant-1")
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"issuing.example.test", "issued.example.test"}, domains)
}

func (f *fakeStore) ReleaseCertificateIssuanceLease(context.Context, string, string) error {
	f.issuanceLeaseHeld = false
	return nil
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
	return &store.TenantAlias{TenantID: tenantID, Subdomain: subdomain, Status: "cert_issuing", AuthorityVersion: 1}, nil
}

func (f *fakeStore) GetTenantAlias(ctx context.Context, tenantID string) (*store.TenantAlias, error) {
	if f.getTenantAliasFunc != nil {
		return f.getTenantAliasFunc(ctx, tenantID)
	}
	return &store.TenantAlias{TenantID: tenantID, Subdomain: "acme", Status: "cert_issuing", AuthorityVersion: 1}, nil
}

func (f *fakeStore) ListPendingTenantAliases(_ context.Context) ([]store.TenantAlias, error) {
	return nil, nil
}

func (f *fakeStore) SetTenantAliasStatus(_ context.Context, _, _ string, _ int64, _, _ string) (bool, error) {
	return true, nil
}

func (f *fakeStore) DeleteTenantAlias(_ context.Context, _ string) (bool, error) {
	return true, nil
}

func (f *fakeStore) UpsertTenantEdgeApplyAck(ctx context.Context, state *store.TenantEdgeApplyState) (store.TenantEdgeApplyAckOutcome, error) {
	if f.upsertTenantEdgeApplyAckFunc != nil {
		return f.upsertTenantEdgeApplyAckFunc(ctx, state)
	}
	return store.TenantEdgeApplyAckAccepted, nil
}

func (f *fakeStore) TenantAliasHasDNS(_ context.Context, _ string) (bool, error) {
	return false, nil
}

func (f *fakeStore) TenantAliasClusterAuthorityState(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func (f *fakeStore) GrantTenantAliasClusterAuthority(_ context.Context, _, _ string, _ int64) (bool, error) {
	return true, nil
}

func (f *fakeStore) RevokeTenantAliasClusterAuthority(_ context.Context, _, _ string, _ int64) (bool, error) {
	return true, nil
}

func (f *fakeStore) ListTenantAliasAuthorizedClusters(_ context.Context, _ string) ([]string, error) {
	return nil, nil
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

func (f *fakeStore) customDomain(tenantID, domain string) *store.TenantCustomDomain {
	row := f.customDomains[domain]
	if row == nil || row.TenantID != tenantID {
		return nil
	}
	return row
}

func (f *fakeStore) sortedCustomDomains() []*store.TenantCustomDomain {
	names := make([]string, 0, len(f.customDomains))
	for name := range f.customDomains {
		names = append(names, name)
	}
	sort.Strings(names)
	rows := make([]*store.TenantCustomDomain, 0, len(names))
	for _, name := range names {
		rows = append(rows, f.customDomains[name])
	}
	return rows
}

func (f *fakeStore) GetTenantCustomDomain(_ context.Context, tenantID, domain string) (*store.TenantCustomDomain, error) {
	row := f.customDomain(tenantID, domain)
	if row == nil {
		return nil, store.ErrNotFound
	}
	copied := *row
	return &copied, nil
}

func (f *fakeStore) ListTenantCustomDomainsByStatus(_ context.Context, statuses []string) ([]store.TenantCustomDomain, error) {
	var out []store.TenantCustomDomain
	for _, row := range f.sortedCustomDomains() {
		if slices.Contains(statuses, row.Status) {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (f *fakeStore) ListTenantCustomDomains(ctx context.Context, tenantID string) ([]store.TenantCustomDomain, error) {
	if f.listTenantCustomDomainsFunc != nil {
		return f.listTenantCustomDomainsFunc(ctx, tenantID)
	}
	var out []store.TenantCustomDomain
	for _, row := range f.sortedCustomDomains() {
		if row.TenantID == tenantID {
			out = append(out, *row)
		}
	}
	return out, nil
}

func (f *fakeStore) SetTenantCustomDomainStatus(_ context.Context, tenantID, domain, expectedStatus, status, errMsg string) (bool, error) {
	if f.customDomains == nil {
		return true, nil
	}
	row := f.customDomain(tenantID, domain)
	if row == nil || (status != "tearing_down" && row.Status != expectedStatus) {
		return false, nil
	}
	row.Status = status
	row.LastError = sql.NullString{String: errMsg, Valid: errMsg != ""}
	if status != "cert_failed" {
		row.NextAttemptAt = sql.NullTime{}
	}
	return true, nil
}

func (f *fakeStore) MarkTenantCustomDomainVerified(ctx context.Context, tenantID, domain, expectedStatus string) (bool, error) {
	return f.SetTenantCustomDomainStatus(ctx, tenantID, domain, expectedStatus, "verified", "")
}

func (f *fakeStore) ExpireTenantCustomDomainVerifications(_ context.Context, period time.Duration) (int, error) {
	expired := 0
	for _, row := range f.customDomains {
		if row.Status == "pending_verification" && !row.VerificationStartedAt.IsZero() && time.Since(row.VerificationStartedAt) > period {
			row.Status = "verification_failed"
			expired++
		}
	}
	return expired, nil
}

func (f *fakeStore) CompleteTenantCustomDomainIssuance(_ context.Context, tenantID, domain, issuerID string, expiresAt sql.NullTime) (bool, error) {
	row := f.customDomain(tenantID, domain)
	if row == nil || row.Status != "cert_issuing" {
		return false, nil
	}
	row.Status = "cert_issued"
	row.IssuerID = sql.NullString{String: issuerID, Valid: issuerID != ""}
	row.CertExpiresAt = expiresAt
	row.LastError, row.LastRenewalError = sql.NullString{}, sql.NullString{}
	row.NextAttemptAt, row.LastRenewalErrorAt = sql.NullTime{}, sql.NullTime{}
	return true, nil
}

func (f *fakeStore) FailTenantCustomDomainIssuance(_ context.Context, tenantID, domain, errMsg string, retryAfter time.Duration) (bool, error) {
	row := f.customDomain(tenantID, domain)
	if row == nil || row.Status != "cert_issuing" {
		return false, nil
	}
	row.Status = "cert_failed"
	row.LastError = sql.NullString{String: errMsg, Valid: errMsg != ""}
	row.NextAttemptAt = sql.NullTime{Valid: true, Time: time.Now().Add(retryAfter)}
	return true, nil
}

func (f *fakeStore) RefreshTenantCustomDomainServedCertificate(_ context.Context, tenantID string, domains []string, issuerID string, expiresAt sql.NullTime) (int64, error) {
	f.servedCertificateRefreshes++
	var updated int64
	for _, row := range f.customDomains {
		if row.TenantID != tenantID || !store.CustomDomainParticipatesInTenantBundle(row.Status) || !slices.Contains(domains, row.Domain) {
			continue
		}
		row.IssuerID = sql.NullString{String: issuerID, Valid: issuerID != ""}
		row.CertExpiresAt = expiresAt
		row.LastRenewalError, row.LastRenewalErrorAt = sql.NullString{}, sql.NullTime{}
		updated++
	}
	return updated, nil
}

func (f *fakeStore) FinalizeTenantCustomDomainRemoval(_ context.Context, tenantID, domain string) (bool, error) {
	if f.customDomains == nil {
		return true, nil
	}
	row := f.customDomain(tenantID, domain)
	if row == nil || row.Status != "tearing_down" {
		return false, nil
	}
	delete(f.customDomains, domain)
	return true, nil
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

func TestEnsureTenantTLSBundleReusesIssuanceCacheWhileAliasIsPending(t *testing.T) {
	ctx := context.Background()
	domains := []string{"*.acme.cdn.frameworks.network", "acme.cdn.frameworks.network"}
	existing := &store.TLSBundle{
		BundleID: "tenant:tenant-123", Domains: domains, CertPEM: "last-good-cert",
		KeyPEM: "last-good-key", ExpiresAt: time.Now().Add(60 * 24 * time.Hour), IssuerCA: "google-trust",
	}
	fakeStore := &fakeStore{
		getTLSBundleFunc: func(context.Context, string) (*store.TLSBundle, error) {
			t.Fatal("issuance path used the serving TLS-bundle read")
			return nil, nil
		},
		getTLSBundleForIssuanceFunc: func(_ context.Context, bundleID string) (*store.TLSBundle, error) {
			require.Equal(t, existing.BundleID, bundleID)
			return existing, nil
		},
		getTenantAliasFunc: func(_ context.Context, tenantID string) (*store.TenantAlias, error) {
			return &store.TenantAlias{TenantID: tenantID, Subdomain: "acme", Status: "cert_issuing", AuthorityVersion: 1}, nil
		},
		listTenantCustomDomainsFunc: func(context.Context, string) ([]store.TenantCustomDomain, error) { return nil, nil },
	}
	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(*lego.Config) (acmeClient, error) {
		t.Fatal("fresh pending bundle placed another ACME order")
		return nil, nil
	}

	got, err := manager.EnsureTLSBundle(ctx, existing.BundleID, domains, "ops@frameworks.network")
	require.NoError(t, err)
	require.Same(t, existing, got)
	require.Equal(t, 0, fakeStore.saveBundleCalled)
}

func TestEnsureTenantTLSBundleDoesNotDuplicateCrossReplicaIssuance(t *testing.T) {
	fakeStore := &fakeStore{
		issuanceLeaseHeld: true,
		getTLSBundleFunc: func(context.Context, string) (*store.TLSBundle, error) {
			return nil, store.ErrNotFound
		},
		getTenantAliasFunc: func(_ context.Context, tenantID string) (*store.TenantAlias, error) {
			return &store.TenantAlias{TenantID: tenantID, Subdomain: "acme", Status: "cert_issuing", AuthorityVersion: 1}, nil
		},
	}
	manager := NewCertManager(fakeStore)
	manager.acmeClientFactory = func(*lego.Config) (acmeClient, error) {
		t.Fatal("lease loser placed a duplicate ACME order")
		return nil, nil
	}

	_, err := manager.EnsureTLSBundle(context.Background(), "tenant:tenant-123", []string{
		"acme.cdn.frameworks.network", "*.acme.cdn.frameworks.network",
	}, "ops@frameworks.network")
	require.ErrorIs(t, err, store.ErrIssuanceInProgress)
	require.Equal(t, 0, fakeStore.saveBundleCalled)
}

func TestEnsureTLSBundleRenewsTenantCustomDomainBundleWithBunnyProvider(t *testing.T) {
	ctx := context.Background()
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
			require.Equal(t, "acme", bundle.AuthoritySubdomain)
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
	}

	manager := NewCertManager(fakeStore)
	manager.SetIssuanceSettings(func() IssuanceSettings { return IssuanceSettings{AllowedSuffixes: "frameworks.network"} })
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
}

const (
	customDomainTestRoot  = "frameworks.network"
	customDomainTestEmail = "ops@frameworks.network"
)

var tenantAliasTestSANs = []string{"*.acme.cdn.frameworks.network", "acme.cdn.frameworks.network"}

// customDomainHarness drives the real CertManager lifecycle over a stateful
// fake store. Any exact per-domain certificate read or write fails the test:
// the tenant bundle is the only certificate that may serve a custom domain.
type customDomainHarness struct {
	store    *fakeStore
	manager  *CertManager
	acme     *fakeACMEClient
	alias    *store.TenantAlias
	saved    []*store.TLSBundle
	notAfter time.Time
}

func newCustomDomainHarness(t *testing.T, aliasStatus string, rows ...store.TenantCustomDomain) *customDomainHarness {
	t.Helper()
	notAfter := time.Now().Add(80 * 24 * time.Hour).Truncate(time.Second)
	certPEM, keyPEM := buildTestCert(t, notAfter)
	h := &customDomainHarness{
		acme:     &fakeACMEClient{resource: &certificate.Resource{Certificate: certPEM, PrivateKey: keyPEM}},
		alias:    &store.TenantAlias{TenantID: "tenant-1", Subdomain: "acme", Status: aliasStatus, AuthorityVersion: 3},
		notAfter: notAfter,
	}
	h.store = &fakeStore{
		customDomains: map[string]*store.TenantCustomDomain{},
		getTenantAliasFunc: func(context.Context, string) (*store.TenantAlias, error) {
			copied := *h.alias
			return &copied, nil
		},
		getCertFunc: func(context.Context, string, string) (*store.Certificate, error) {
			t.Fatal("custom-domain lifecycle read an exact per-domain certificate")
			return nil, nil
		},
		saveCertFunc: func(context.Context, string, *store.Certificate) error {
			t.Fatal("custom-domain lifecycle saved an exact per-domain certificate")
			return nil
		},
		getTLSBundleFunc: func(context.Context, string) (*store.TLSBundle, error) { return nil, store.ErrNotFound },
		getAccountFunc: func(context.Context, string, string, string) (*store.ACMEAccount, error) {
			return nil, store.ErrNotFound
		},
		saveAccountFunc: func(context.Context, string, *store.ACMEAccount) error { return nil },
		saveTLSBundleFunc: func(_ context.Context, bundle *store.TLSBundle) error {
			h.saved = append(h.saved, bundle)
			return nil
		},
	}
	for _, row := range rows {
		if row.TenantID == "" {
			row.TenantID = h.alias.TenantID
		}
		h.store.customDomains[row.Domain] = &row
	}
	h.manager = NewCertManager(h.store)
	h.manager.acmeClientFactory = func(*lego.Config) (acmeClient, error) { return h.acme, nil }
	provider := func() (challenge.Provider, error) { return &fakeDNSProvider{}, nil }
	h.manager.dnsProviderFactory = provider
	h.manager.bunnyDNSProviderFactory = provider
	return h
}

func (h *customDomainHarness) row(domain string) *store.TenantCustomDomain {
	return h.store.customDomains[domain]
}

func (h *customDomainHarness) reconcile(t *testing.T, lookup func(context.Context, string) (string, error)) int {
	t.Helper()
	processed, err := h.manager.ProcessPendingCustomDomains(context.Background(), customDomainTestRoot, customDomainTestEmail, lookup)
	require.NoError(t, err)
	return processed
}

// A reconcile pass fails a domain still pending after the verification period
// before it tries to verify anything, so the expired domain is not verified.
func TestProcessPendingCustomDomainsExpiresStaleVerification(t *testing.T) {
	h := newCustomDomainHarness(t, "cert_issued",
		store.TenantCustomDomain{Domain: "stale.example.com", Status: "pending_verification", VerificationStartedAt: time.Now().Add(-CustomDomainVerificationPeriod - time.Hour)},
		store.TenantCustomDomain{Domain: "fresh.example.com", Status: "pending_verification", VerificationStartedAt: time.Now()},
	)
	lookups := 0
	processed := h.reconcile(t, func(context.Context, string) (string, error) {
		lookups++
		return "", nil
	})

	require.Equal(t, 1, processed)
	require.Equal(t, "verification_failed", h.row("stale.example.com").Status)
	require.Equal(t, "pending_verification", h.row("fresh.example.com").Status)
	require.Equal(t, 1, lookups, "only the domain still inside its period is verified")
}

func TestIssueCustomDomainCertificateServesThroughTenantBundleOnly(t *testing.T) {
	h := newCustomDomainHarness(t, "cert_issued", store.TenantCustomDomain{Domain: "media.example.com", Status: "verified"})

	err := h.manager.IssueCustomDomainCertificate(context.Background(), *h.row("media.example.com"), customDomainTestRoot, customDomainTestEmail)
	require.NoError(t, err)

	require.Equal(t, 1, h.acme.obtainCalled, "the tenant bundle is the only ACME order")
	require.Equal(t, append(slices.Clone(tenantAliasTestSANs), "media.example.com"), h.acme.obtainedDomains)
	require.Equal(t, 0, h.store.saveCertCalled)
	require.Len(t, h.saved, 1)
	require.Equal(t, int64(3), h.saved[0].AuthorityVersion)
	require.NotEmpty(t, h.saved[0].IssuerCA)

	row := h.row("media.example.com")
	require.Equal(t, "cert_issued", row.Status)
	require.Equal(t, h.saved[0].IssuerCA, row.IssuerID.String)
	require.True(t, row.CertExpiresAt.Valid)
	require.WithinDuration(t, h.notAfter, row.CertExpiresAt.Time, time.Second)
	require.False(t, row.LastError.Valid)
	require.False(t, row.NextAttemptAt.Valid)
}

func TestCustomDomainWaitsForIssuedAliasBeforeJoiningTenantBundle(t *testing.T) {
	ctx := context.Background()
	h := newCustomDomainHarness(t, "cert_issuing", store.TenantCustomDomain{Domain: "media.example.com", Status: "verified"})
	lookup := func(context.Context, string) (string, error) { return "acme", nil }

	require.NoError(t, h.manager.IssueCustomDomainCertificate(ctx, *h.row("media.example.com"), customDomainTestRoot, customDomainTestEmail))
	require.Equal(t, "pending_alias", h.row("media.example.com").Status)
	require.Equal(t, 0, h.acme.obtainCalled, "no ACME order before the alias bundle is issued")

	// The alias's own first issuance runs without the waiting domain's SAN.
	_, err := h.manager.EnsureTenantWildcardCertificate(ctx, "tenant-1", "acme", TenantAliasZoneLabel, customDomainTestRoot, customDomainTestEmail)
	require.NoError(t, err)
	require.Equal(t, tenantAliasTestSANs, h.acme.obtainedDomains)

	require.Zero(t, h.reconcile(t, lookup))
	require.Equal(t, "pending_alias", h.row("media.example.com").Status)
	require.Equal(t, 1, h.acme.obtainCalled)

	h.alias.Status = "cert_issued"
	require.Equal(t, 1, h.reconcile(t, lookup))
	require.Equal(t, "cert_issued", h.row("media.example.com").Status)
	require.Equal(t, 2, h.acme.obtainCalled)
	require.Equal(t, append(slices.Clone(tenantAliasTestSANs), "media.example.com"), h.acme.obtainedDomains)
}

func TestCustomDomainIssuanceFailureDefersRetryWithoutBundleRebuild(t *testing.T) {
	h := newCustomDomainHarness(t, "cert_issued",
		store.TenantCustomDomain{Domain: "broken.example.com", Status: "verified"},
		store.TenantCustomDomain{Domain: "healthy.example.com", Status: "cert_issued"},
	)
	h.acme.obtainErr = errors.New("acme: authorization failed for broken.example.com")
	lookups := 0
	lookup := func(context.Context, string) (string, error) {
		lookups++
		return "", nil
	}

	before := time.Now()
	err := h.manager.IssueCustomDomainCertificate(context.Background(), *h.row("broken.example.com"), customDomainTestRoot, customDomainTestEmail)
	require.ErrorContains(t, err, "authorization failed")

	broken := h.row("broken.example.com")
	require.Equal(t, "cert_failed", broken.Status)
	require.Contains(t, broken.LastError.String, "authorization failed")
	require.True(t, broken.NextAttemptAt.Valid)
	require.WithinDuration(t, before.Add(customDomainIssueRetryDelay), broken.NextAttemptAt.Time, 5*time.Second)
	require.Equal(t, 1, h.acme.obtainCalled, "a failed SAN must not trigger a bundle rebuild")
	require.Empty(t, h.saved)
	require.Equal(t, "cert_issued", h.row("healthy.example.com").Status)

	require.Zero(t, h.reconcile(t, lookup))
	require.Zero(t, lookups, "cert_failed domain was re-verified before next_attempt_at")
	require.Equal(t, "cert_failed", broken.Status)
	require.Equal(t, 1, h.acme.obtainCalled)

	broken.NextAttemptAt = sql.NullTime{Valid: true, Time: time.Now().Add(-time.Second)}
	h.reconcile(t, lookup)
	require.Equal(t, 1, lookups, "cert_failed domain was not re-verified after next_attempt_at")
	require.Equal(t, 1, h.acme.obtainCalled)
}

func TestTenantBundleKeepsRenewalFailedDomainAndClearsErrorOnSuccess(t *testing.T) {
	failedAt := time.Now().Add(-time.Hour)
	h := newCustomDomainHarness(t, "cert_issued",
		store.TenantCustomDomain{
			Domain: "media.example.com", Status: "cert_issued",
			IssuerID:         sql.NullString{String: "old-ca", Valid: true},
			CertExpiresAt:    sql.NullTime{Time: time.Now().Add(5 * 24 * time.Hour), Valid: true},
			LastRenewalError: sql.NullString{String: "acme: order rejected", Valid: true}, LastRenewalErrorAt: sql.NullTime{Time: failedAt, Valid: true},
		},
		store.TenantCustomDomain{Domain: "waiting.example.com", Status: "pending_alias"},
		store.TenantCustomDomain{Domain: "failed.example.com", Status: "cert_failed"},
	)

	bundle, err := h.manager.EnsureTenantWildcardCertificate(context.Background(), "tenant-1", "acme", TenantAliasZoneLabel, customDomainTestRoot, customDomainTestEmail)
	require.NoError(t, err)
	require.Equal(t, append(slices.Clone(tenantAliasTestSANs), "media.example.com"), h.acme.obtainedDomains,
		"a renewal failure must not drop the still-served SAN from the next bundle order")

	row := h.row("media.example.com")
	require.Equal(t, "cert_issued", row.Status)
	require.False(t, row.LastRenewalError.Valid)
	require.False(t, row.LastRenewalErrorAt.Valid)
	require.Equal(t, bundle.IssuerCA, row.IssuerID.String)
	require.WithinDuration(t, h.notAfter, row.CertExpiresAt.Time, time.Second)
	require.False(t, h.row("waiting.example.com").IssuerID.Valid)
	require.False(t, h.row("failed.example.com").IssuerID.Valid)
}

func TestConfirmedTenantBundleRefreshesServedCustomDomainMetadata(t *testing.T) {
	existing := &store.TLSBundle{
		BundleID: "tenant:tenant-1", Domains: append(slices.Clone(tenantAliasTestSANs), "media.example.com"),
		ExpiresAt: time.Now().Add(60 * 24 * time.Hour), IssuerCA: "google-trust",
		AuthoritySubdomain: "acme", AuthorityVersion: 3,
	}
	h := newCustomDomainHarness(t, "cert_issued", store.TenantCustomDomain{
		Domain: "media.example.com", Status: "cert_issued",
		IssuerID:         sql.NullString{String: "old-ca", Valid: true},
		LastRenewalError: sql.NullString{String: "acme: order rejected", Valid: true}, LastRenewalErrorAt: sql.NullTime{Time: time.Now(), Valid: true},
	})
	h.store.getTLSBundleForIssuanceFunc = func(context.Context, string) (*store.TLSBundle, error) { return existing, nil }
	h.manager.acmeClientFactory = func(*lego.Config) (acmeClient, error) {
		t.Fatal("fresh tenant bundle placed an ACME order")
		return nil, nil
	}

	got, err := h.manager.EnsureTenantWildcardCertificate(context.Background(), "tenant-1", "acme", TenantAliasZoneLabel, customDomainTestRoot, customDomainTestEmail)
	require.NoError(t, err)
	require.Same(t, existing, got)
	require.Equal(t, 1, h.store.servedCertificateRefreshes)
	row := h.row("media.example.com")
	require.Equal(t, "google-trust", row.IssuerID.String)
	require.WithinDuration(t, existing.ExpiresAt, row.CertExpiresAt.Time, time.Second)
	require.False(t, row.LastRenewalError.Valid)
}

func TestRemovedCustomDomainLeavesTenantBundleSANs(t *testing.T) {
	h := newCustomDomainHarness(t, "cert_issued",
		store.TenantCustomDomain{Domain: "gone.example.com", Status: "cert_issued"},
		store.TenantCustomDomain{Domain: "kept.example.com", Status: "cert_issued"},
	)
	require.NoError(t, h.manager.RemoveCustomDomain(context.Background(), "tenant-1", "gone.example.com"))

	require.Equal(t, 1, h.reconcile(t, func(context.Context, string) (string, error) { return "acme", nil }))
	require.Equal(t, append(slices.Clone(tenantAliasTestSANs), "kept.example.com"), h.acme.obtainedDomains)
	require.Nil(t, h.row("gone.example.com"))
	require.Equal(t, "cert_issued", h.row("kept.example.com").Status)
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
	manager.SetIssuanceSettings(func() IssuanceSettings { return IssuanceSettings{RootDomain: "frameworks.network"} })
	manager.UseBunnyForClusterZones("frameworks.network")

	_, _, _, err := manager.IssueCertificate(ctx, "", "livepeer.media-eu.frameworks.network", "ops@frameworks.network")
	require.NoError(t, err)
	require.Equal(t, 0, cloudflareProvider.presentCalls)
	require.Equal(t, 1, bunnyProvider.presentCalls)
}

func TestRecordConfigSeedApplyResultReturnsOnlyAdvancedTenants(t *testing.T) {
	const tenantID = "10000000-0000-0000-0000-000000000001"
	accepted := false
	fakeStore := &fakeStore{
		upsertTenantEdgeApplyAckFunc: func(_ context.Context, _ *store.TenantEdgeApplyState) (store.TenantEdgeApplyAckOutcome, error) {
			if accepted {
				return store.TenantEdgeApplyAckAccepted, nil
			}
			return store.TenantEdgeApplyAckStale, nil
		},
	}
	manager := NewCertManager(fakeStore)
	affected, discarded, err := manager.RecordConfigSeedApplyResult(
		context.Background(), "node-1", "cluster-1", 7, 1,
		[]string{"tenant:" + tenantID}, nil, map[string]string{"tenant:" + tenantID: "version-1"}, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 0 {
		t.Fatalf("discarded delivery affected tenants=%v, want none", affected)
	}
	if discarded.Stale != 1 || discarded.MissingParent != 0 {
		t.Fatalf("discarded deliveries=%+v, want one stale", discarded)
	}
	accepted = true
	affected, discarded, err = manager.RecordConfigSeedApplyResult(
		context.Background(), "node-1", "cluster-1", 7, 2,
		[]string{"tenant:" + tenantID}, nil, map[string]string{"tenant:" + tenantID: "version-1"}, time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(affected) != 1 || affected[0] != tenantID {
		t.Fatalf("advanced delivery affected tenants=%v, want [%s]", affected, tenantID)
	}
	if discarded.Total() != 0 {
		t.Fatalf("accepted delivery discarded=%+v, want 0", discarded)
	}
}

func TestFinalizeCustomDomainRemovalDoesNotIssueForUnreadyAlias(t *testing.T) {
	for _, status := range []string{"cert_issuing", "cert_failed", "tearing_down"} {
		t.Run(status, func(t *testing.T) {
			fakeStore := &fakeStore{
				getTenantAliasFunc: func(_ context.Context, tenantID string) (*store.TenantAlias, error) {
					return &store.TenantAlias{TenantID: tenantID, Subdomain: "unready", Status: status, AuthorityVersion: 1}, nil
				},
				getTLSBundleFunc: func(context.Context, string) (*store.TLSBundle, error) {
					t.Fatalf("%s alias reached tenant bundle issuance", status)
					return nil, nil
				},
			}
			manager := NewCertManager(fakeStore)
			if err := manager.FinalizeCustomDomainRemoval(context.Background(), "tenant-1", "media.example.test", "example.test", "ops@example.test"); err != nil {
				t.Fatal(err)
			}
			if fakeStore.saveBundleCalled != 0 {
				t.Fatalf("saved %d tenant bundles for %s alias", fakeStore.saveBundleCalled, status)
			}
		})
	}
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
