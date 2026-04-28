package worker

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_dns/internal/store"
	"frameworks/pkg/logging"

	"github.com/stretchr/testify/require"
)

type fakeRenewStore struct {
	certs   []store.Certificate
	bundles []store.TLSBundle
	err     error
}

func (f *fakeRenewStore) ListExpiringCertificates(ctx context.Context, threshold time.Duration) ([]store.Certificate, error) {
	return f.certs, f.err
}

func (f *fakeRenewStore) ListExpiringTLSBundles(ctx context.Context, threshold time.Duration) ([]store.TLSBundle, error) {
	return f.bundles, f.err
}

type fakeIssuer struct {
	results []error
	calls   []string
	bundles []string
}

func (f *fakeIssuer) IssueCertificate(ctx context.Context, tenantID, domain, email string) (string, string, time.Time, error) {
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
