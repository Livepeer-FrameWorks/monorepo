package worker

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_dns/internal/store"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/stretchr/testify/require"
)

type fakeRenewStore struct {
	certs         []store.Certificate
	bundles       []store.TLSBundle
	aliases       map[string]*store.TenantAlias
	customDomains map[string]*store.TenantCustomDomain
	err           error
}

func (f *fakeRenewStore) ListExpiringCertificates(ctx context.Context, threshold time.Duration) ([]store.Certificate, error) {
	return f.certs, f.err
}

func (f *fakeRenewStore) ListExpiringTLSBundles(ctx context.Context, threshold time.Duration) ([]store.TLSBundle, error) {
	return f.bundles, f.err
}

func (f *fakeRenewStore) GetTenantAlias(_ context.Context, tenantID string) (*store.TenantAlias, error) {
	alias := f.aliases[tenantID]
	if alias == nil {
		return nil, store.ErrNotFound
	}
	return alias, nil
}

func (f *fakeRenewStore) GetTenantCustomDomain(_ context.Context, tenantID, domain string) (*store.TenantCustomDomain, error) {
	customDomain := f.customDomains[tenantID+"\x00"+domain]
	if customDomain == nil {
		return nil, store.ErrNotFound
	}
	return customDomain, nil
}

func (f *fakeRenewStore) DeleteExpiredCertificateIssuanceLeases(context.Context) (int64, error) {
	return 0, nil
}

type fakeIssuer struct {
	results       []error
	calls         []string
	bundles       []string
	tenantBundles []tenantBundleCall
}

type tenantBundleCall struct {
	tenantID  string
	subdomain string
}

func (f *fakeIssuer) RenewCertificate(ctx context.Context, tenantID, domain, email string) (string, string, time.Time, error) {
	f.calls = append(f.calls, domain)
	if len(f.results) == 0 {
		return "", "", time.Time{}, nil
	}
	err := f.results[0]
	f.results = f.results[1:]
	return "", "", time.Time{}, err
}

func (f *fakeIssuer) EnsureTLSBundle(ctx context.Context, bundleID string, domains []string, email string) (*store.TLSBundle, error) {
	f.bundles = append(f.bundles, bundleID)
	if len(f.results) == 0 {
		return &store.TLSBundle{BundleID: bundleID, Domains: domains}, nil
	}
	err := f.results[0]
	f.results = f.results[1:]
	if err != nil {
		return nil, err
	}
	return &store.TLSBundle{BundleID: bundleID, Domains: domains}, nil
}

func (f *fakeIssuer) EnsureTenantWildcardCertificate(_ context.Context, tenantID, subdomain, _, _, _ string) (*store.TLSBundle, error) {
	f.tenantBundles = append(f.tenantBundles, tenantBundleCall{tenantID: tenantID, subdomain: subdomain})
	if len(f.results) == 0 {
		return &store.TLSBundle{BundleID: "tenant:" + tenantID}, nil
	}
	err := f.results[0]
	f.results = f.results[1:]
	if err != nil {
		return nil, err
	}
	return &store.TLSBundle{BundleID: "tenant:" + tenantID}, nil
}

func TestRenewalWorkerRetriesWithBackoff(t *testing.T) {
	retryErr := errors.New("rate limit: 429")
	store := &fakeRenewStore{
		certs: []store.Certificate{
			{Domain: "example.com", TenantID: sql.NullString{Valid: false}},
		},
	}
	issuer := &fakeIssuer{
		results: []error{retryErr, retryErr, nil},
	}
	var sleeps []time.Duration

	worker := &RenewalWorker{
		store:       store,
		certManager: issuer,
		logger:      logging.NewLogger(),
		acmeEmail:   "ops@example.com",
		sleep: func(ctx context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		},
	}

	worker.renewCertificates(context.Background())

	require.Equal(t, []string{"example.com", "example.com", "example.com"}, issuer.calls)
	require.Equal(t, []time.Duration{30 * time.Second, 60 * time.Second}, sleeps)
}

func TestRenewalWorkerSkipsRetriesOnNonRetryableErrorAndContinues(t *testing.T) {
	nonRetryErr := errors.New("invalid response")
	store := &fakeRenewStore{
		certs: []store.Certificate{
			{Domain: "fail.example.com", TenantID: sql.NullString{Valid: false}},
			{Domain: "next.example.com", TenantID: sql.NullString{Valid: false}},
		},
	}
	issuer := &fakeIssuer{
		results: []error{nonRetryErr, nil},
	}

	worker := &RenewalWorker{
		store:       store,
		certManager: issuer,
		logger:      logging.NewLogger(),
		acmeEmail:   "ops@example.com",
		sleep: func(ctx context.Context, duration time.Duration) error {
			return nil
		},
	}

	worker.renewCertificates(context.Background())

	require.Equal(t, []string{"fail.example.com", "next.example.com"}, issuer.calls)
}

func TestRenewalWorkerRequiresLiveTenantAuthority(t *testing.T) {
	tenantID := "10000000-0000-0000-0000-000000000001"
	pendingTenantID := "10000000-0000-0000-0000-000000000002"
	renewStore := &fakeRenewStore{
		certs: []store.Certificate{
			{Domain: "alias.example.test", TenantID: sql.NullString{String: tenantID, Valid: true}},
			{Domain: "custom.example.test", TenantID: sql.NullString{String: tenantID, Valid: true}},
			{Domain: "platform.example.test"},
		},
		bundles: []store.TLSBundle{
			{BundleID: "tenant:" + tenantID, Domains: []string{"alias.example.test"}},
			{BundleID: "tenant:" + pendingTenantID, Domains: []string{"pending.example.test"}},
			{BundleID: "platform", Domains: []string{"platform.example.test"}},
		},
		aliases: map[string]*store.TenantAlias{
			tenantID:        {TenantID: tenantID, Subdomain: "alias", Status: "cert_issued"},
			pendingTenantID: {TenantID: pendingTenantID, Status: "cert_issuing"},
		},
		customDomains: map[string]*store.TenantCustomDomain{
			tenantID + "\x00custom.example.test": {TenantID: tenantID, Domain: "custom.example.test", Status: "cert_failed"},
		},
	}
	issuer := &fakeIssuer{}
	worker := NewRenewalWorker(renewStore, issuer, logging.NewLogger(), "frameworks.network", "ops@example.com")
	worker.renewCertificates(context.Background())

	require.Equal(t, []string{"custom.example.test", "platform.example.test"}, issuer.calls)
	require.Equal(t, []tenantBundleCall{{tenantID: tenantID, subdomain: "alias"}}, issuer.tenantBundles)
	require.Equal(t, []string{"platform"}, issuer.bundles)
}

func TestRenewalWorkerChecksBundlesWhenNoCertificatesExpire(t *testing.T) {
	renewStore := &fakeRenewStore{bundles: []store.TLSBundle{{BundleID: "platform", Domains: []string{"platform.example.test"}}}}
	issuer := &fakeIssuer{}
	worker := NewRenewalWorker(renewStore, issuer, logging.NewLogger(), "frameworks.network", "ops@example.com")
	worker.renewCertificates(context.Background())
	require.Equal(t, []string{"platform"}, issuer.bundles)
}
