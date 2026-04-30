package logic

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
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
	getCertFunc       func(ctx context.Context, tenantID, domain string) (*store.Certificate, error)
	saveCertFunc      func(ctx context.Context, tenantID string, cert *store.Certificate) error
	getTLSBundleFunc  func(ctx context.Context, bundleID string) (*store.TLSBundle, error)
	saveTLSBundleFunc func(ctx context.Context, bundle *store.TLSBundle) error
	getAccountFunc    func(ctx context.Context, tenantID, email string) (*store.ACMEAccount, error)
	saveAccountFunc   func(ctx context.Context, tenantID string, acc *store.ACMEAccount) error
	saveCertCalled    int
	saveBundleCalled  int
	saveAccountCalled int
}

func (f *fakeStore) GetCertificate(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
	return f.getCertFunc(ctx, tenantID, domain)
}

func (f *fakeStore) SaveCertificate(ctx context.Context, tenantID string, cert *store.Certificate) error {
	f.saveCertCalled++
	return f.saveCertFunc(ctx, tenantID, cert)
}

func (f *fakeStore) GetTLSBundle(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
	return f.getTLSBundleFunc(ctx, bundleID)
}

func (f *fakeStore) SaveTLSBundle(ctx context.Context, bundle *store.TLSBundle) error {
	f.saveBundleCalled++
	return f.saveTLSBundleFunc(ctx, bundle)
}

func (f *fakeStore) GetACMEAccount(ctx context.Context, tenantID, email string) (*store.ACMEAccount, error) {
	return f.getAccountFunc(ctx, tenantID, email)
}

func (f *fakeStore) SaveACMEAccount(ctx context.Context, tenantID string, acc *store.ACMEAccount) error {
	f.saveAccountCalled++
	return f.saveAccountFunc(ctx, tenantID, acc)
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
	provider       challenge.Provider
	registerCalled int
	obtainCalled   int
	registerErr    error
	obtainErr      error
	resource       *certificate.Resource
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

func (f *fakeACMEClient) Obtain(request certificate.ObtainRequest) (*certificate.Resource, error) {
	f.obtainCalled++
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
		getAccountFunc: func(ctx context.Context, tenantID, email string) (*store.ACMEAccount, error) {
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
		getAccountFunc: func(ctx context.Context, tenantID, email string) (*store.ACMEAccount, error) {
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
		getAccountFunc: func(ctx context.Context, tenantID, email string) (*store.ACMEAccount, error) {
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
		getAccountFunc: func(ctx context.Context, tenantID, email string) (*store.ACMEAccount, error) {
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
