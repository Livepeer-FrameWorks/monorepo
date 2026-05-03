package logic

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"frameworks/api_dns/internal/store"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/bunny"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
)

const platformCertTenantID = ""

// CertManager handles certificate issuance logic
type certStore interface {
	GetCertificate(ctx context.Context, tenantID, domain string) (*store.Certificate, error)
	SaveCertificate(ctx context.Context, tenantID string, cert *store.Certificate) error
	GetTLSBundle(ctx context.Context, bundleID string) (*store.TLSBundle, error)
	SaveTLSBundle(ctx context.Context, bundle *store.TLSBundle) error
	GetACMEAccount(ctx context.Context, tenantID, email string) (*store.ACMEAccount, error)
	SaveACMEAccount(ctx context.Context, tenantID string, acc *store.ACMEAccount) error
}

type acmeClient interface {
	SetDNS01Provider(provider challenge.Provider) error
	Register() (*registration.Resource, error)
	Obtain(request certificate.ObtainRequest) (*certificate.Resource, error)
}

type CertManager struct {
	store                        certStore
	acmeClientFactory            func(config *lego.Config) (acmeClient, error)
	dnsProviderFactory           func() (challenge.Provider, error)
	bunnyDNSProviderFactory      func() (challenge.Provider, error)
	dnsProviderForDomainsFactory func(domains []string) (challenge.Provider, error)
}

// NewCertManager creates a new CertManager
func NewCertManager(s certStore) *CertManager {
	return &CertManager{
		store:             s,
		acmeClientFactory: newLegoClient,
		dnsProviderFactory: func() (challenge.Provider, error) {
			return cloudflare.NewDNSProvider()
		},
		bunnyDNSProviderFactory: func() (challenge.Provider, error) {
			return bunny.NewDNSProvider()
		},
	}
}

func (m *CertManager) UseBunnyForClusterZones(rootDomain string) {
	rootDomain = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(rootDomain)), ".")
	if rootDomain == "" {
		return
	}
	m.dnsProviderForDomainsFactory = func(domains []string) (challenge.Provider, error) {
		if certificateNeedsBunnyProvider(domains, rootDomain) {
			return m.bunnyDNSProviderFactory()
		}
		return m.dnsProviderFactory()
	}
}

// ACMEUser implements lego.User
type ACMEUser struct {
	Email        string
	Registration *registration.Resource
	key          crypto.PrivateKey
}

func (u *ACMEUser) GetEmail() string                        { return u.Email }
func (u *ACMEUser) GetRegistration() *registration.Resource { return u.Registration }
func (u *ACMEUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

func normalizeDomains(domains []string) []string {
	if len(domains) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(domains))
	normalized := make([]string, 0, len(domains))
	for _, domain := range domains {
		value := strings.TrimSpace(strings.ToLower(strings.TrimSuffix(domain, ".")))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	slices.Sort(normalized)
	return normalized
}

// IssueCertificate requests a certificate using the DNS-01 provider
// authoritative for the requested domain.
// It implements "Cache-First" logic.
// tenantID is optional - empty string means platform-wide certificate.
func (m *CertManager) IssueCertificate(ctx context.Context, tenantID, domain, email string) (certPEM, keyPEM string, expiresAt time.Time, err error) {
	if domain == "" || email == "" {
		return "", "", time.Time{}, fmt.Errorf("domain and email are required")
	}
	domain = normalizeDomains([]string{domain})[0]
	if !isDomainAllowed(domain) {
		return "", "", time.Time{}, fmt.Errorf("domain is not allowed for certificate issuance")
	}

	// 1. Check Cache (DB) - with tenant context
	cert, err := m.store.GetCertificate(ctx, tenantID, domain)
	if err == nil {
		// Check expiry (renew if < 30 days remaining)
		if time.Until(cert.ExpiresAt) > 30*24*time.Hour {
			return cert.CertPEM, cert.KeyPEM, cert.ExpiresAt, nil
		}
		// If expiring soon, proceed to renewal logic (below)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", "", time.Time{}, fmt.Errorf("failed to check certificate cache: %w", err)
	}

	certificatePEM, privateKeyPEM, expiry, err := m.obtainCertificate(ctx, tenantID, []string{domain}, email)
	if err != nil {
		return "", "", time.Time{}, err
	}

	// 9. Save to DB (with tenant context)
	newCert := &store.Certificate{
		Domain:    domain,
		CertPEM:   certificatePEM,
		KeyPEM:    privateKeyPEM,
		ExpiresAt: expiry,
	}
	if err := m.store.SaveCertificate(ctx, tenantID, newCert); err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to save certificate: %w", err)
	}

	return newCert.CertPEM, newCert.KeyPEM, expiry, nil
}

func (m *CertManager) EnsureTLSBundle(ctx context.Context, bundleID string, domains []string, email string) (*store.TLSBundle, error) {
	bundleID = strings.TrimSpace(bundleID)
	domains = normalizeDomains(domains)
	if bundleID == "" || len(domains) == 0 || strings.TrimSpace(email) == "" {
		return nil, fmt.Errorf("bundle_id, domains, and email are required")
	}

	for _, domain := range domains {
		if !isDomainAllowed(domain) {
			return nil, fmt.Errorf("domain %q is not allowed for certificate issuance", domain)
		}
	}

	existing, err := m.store.GetTLSBundle(ctx, bundleID)
	switch {
	case err == nil:
		if slices.Equal(existing.Domains, domains) && time.Until(existing.ExpiresAt) > 30*24*time.Hour {
			return existing, nil
		}
	case !errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("failed to check tls bundle cache: %w", err)
	}

	certificatePEM, privateKeyPEM, expiry, err := m.obtainCertificate(ctx, platformCertTenantID, domains, email)
	if err != nil {
		return nil, err
	}

	bundle := &store.TLSBundle{
		BundleID:  bundleID,
		Domains:   domains,
		CertPEM:   certificatePEM,
		KeyPEM:    privateKeyPEM,
		ExpiresAt: expiry,
	}
	if err := m.store.SaveTLSBundle(ctx, bundle); err != nil {
		return nil, fmt.Errorf("failed to save tls bundle: %w", err)
	}
	return bundle, nil
}

func (m *CertManager) GetTLSBundle(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
	return m.store.GetTLSBundle(ctx, bundleID)
}

func (m *CertManager) obtainCertificate(ctx context.Context, tenantID string, domains []string, email string) (certPEM, keyPEM string, expiresAt time.Time, err error) {
	user, err := m.getOrCreateUser(ctx, tenantID, email)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to load ACME user: %w", err)
	}

	config := lego.NewConfig(user)
	switch strings.ToLower(os.Getenv("ACME_ENV")) {
	case "staging":
		config.CADirURL = lego.LEDirectoryStaging
	default:
		config.CADirURL = lego.LEDirectoryProduction
	}
	config.Certificate.KeyType = certcrypto.EC256

	client, err := m.acmeClientFactory(config)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to create lego client: %w", err)
	}

	providerFactory := m.dnsProviderFactory
	if m.dnsProviderForDomainsFactory != nil {
		providerFactory = func() (challenge.Provider, error) {
			return m.dnsProviderForDomainsFactory(domains)
		}
	}

	provider, err := providerFactory()
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to create DNS provider: %w", err)
	}
	if challengeErr := client.SetDNS01Provider(&resilientDNSProvider{provider: provider}); challengeErr != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to set DNS provider: %w", challengeErr)
	}

	if user.Registration == nil {
		reg, regErr := client.Register()
		if regErr != nil {
			return "", "", time.Time{}, fmt.Errorf("registration failed: %w", regErr)
		}
		user.Registration = reg
		if saveErr := m.saveUser(ctx, tenantID, user); saveErr != nil {
			return "", "", time.Time{}, fmt.Errorf("failed to save user registration: %w", saveErr)
		}
	}

	certificates, err := client.Obtain(certificate.ObtainRequest{
		Domains: domains,
		Bundle:  true,
	})
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to obtain certificate: %w", err)
	}

	expiry := time.Now().Add(90 * 24 * time.Hour)
	block, _ := pem.Decode(certificates.Certificate)
	if block != nil {
		if parsedCert, parseErr := x509.ParseCertificate(block.Bytes); parseErr == nil {
			expiry = parsedCert.NotAfter
		}
	}

	return string(certificates.Certificate), string(certificates.PrivateKey), expiry, nil
}

type resilientDNSProvider struct {
	provider challenge.Provider
}

func (p *resilientDNSProvider) Present(domain, token, keyAuth string) error {
	err := p.provider.Present(domain, token, keyAuth)
	if isCloudflareDuplicateTXTError(err) {
		return nil
	}
	return err
}

func (p *resilientDNSProvider) CleanUp(domain, token, keyAuth string) error {
	err := p.provider.CleanUp(domain, token, keyAuth)
	if isCloudflareUnknownRecordCleanupError(err) {
		return nil
	}
	return err
}

func isCloudflareDuplicateTXTError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "81058") && strings.Contains(msg, "identical record already exists")
}

func isCloudflareUnknownRecordCleanupError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "cloudflare") && strings.Contains(msg, "unknown record id")
}

func certificateNeedsBunnyProvider(domains []string, rootDomain string) bool {
	rootDomain = strings.TrimSuffix(strings.TrimSpace(strings.ToLower(rootDomain)), ".")
	for _, domain := range domains {
		normalized := strings.TrimSuffix(strings.TrimSpace(strings.ToLower(domain)), ".")
		isWildcard := strings.HasPrefix(normalized, "*.")
		base := strings.TrimPrefix(normalized, "*.")
		if base == rootDomain {
			continue
		}
		if !strings.HasSuffix(base, "."+rootDomain) {
			continue
		}
		prefix := strings.TrimSuffix(base, "."+rootDomain)
		labels := strings.Split(prefix, ".")
		if len(labels) == 1 && labels[0] != "" && isWildcard {
			return true
		}
		if len(labels) >= 2 {
			return true
		}
	}
	return false
}

type legoClient struct {
	client *lego.Client
}

func newLegoClient(config *lego.Config) (acmeClient, error) {
	client, err := lego.NewClient(config)
	if err != nil {
		return nil, err
	}
	return &legoClient{client: client}, nil
}

func (l *legoClient) SetDNS01Provider(provider challenge.Provider) error {
	return l.client.Challenge.SetDNS01Provider(provider)
}

func (l *legoClient) Register() (*registration.Resource, error) {
	return l.client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
}

func (l *legoClient) Obtain(request certificate.ObtainRequest) (*certificate.Resource, error) {
	return l.client.Certificate.Obtain(request)
}

func isDomainAllowed(domain string) bool {
	domain = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(domain, ".")))
	if domain == "" {
		return false
	}
	domain = strings.TrimPrefix(domain, "*.")

	env := strings.TrimSpace(os.Getenv("NAVIGATOR_CERT_ALLOWED_SUFFIXES"))
	var suffixes []string
	if env != "" {
		for _, s := range strings.Split(env, ",") {
			s = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(s, ".")))
			if s != "" {
				suffixes = append(suffixes, s)
			}
		}
	} else if root := strings.TrimSpace(os.Getenv("BRAND_DOMAIN")); root != "" {
		suffixes = []string{strings.ToLower(strings.TrimSuffix(root, "."))}
	}

	// If no allowlist is configured, allow all (dev mode).
	if len(suffixes) == 0 {
		return true
	}

	for _, suffix := range suffixes {
		if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
			return true
		}
	}
	return false
}

// GetCertificate retrieves a certificate from the store.
// tenantID is optional - empty string means platform-wide certificate.
func (m *CertManager) GetCertificate(ctx context.Context, tenantID, domain string) (*store.Certificate, error) {
	return m.store.GetCertificate(ctx, tenantID, domain)
}

// HasClusterWildcardCert returns true if the cluster has a valid (non-expired)
// wildcard TLS certificate. Implements the CertChecker interface used by DNSManager
// to gate granular edge service subdomains.
func (m *CertManager) HasClusterWildcardCert(ctx context.Context, clusterSlug, rootDomain string) bool {
	domain := fmt.Sprintf("*.%s.%s", clusterSlug, rootDomain)
	cert, err := m.GetCertificate(ctx, platformCertTenantID, domain)
	return err == nil && cert != nil && cert.ExpiresAt.After(time.Now())
}

func (m *CertManager) EnsureClusterWildcardCertificate(ctx context.Context, clusterSlug, rootDomain, email string) (*store.Certificate, error) {
	clusterSlug = strings.TrimSpace(clusterSlug)
	rootDomain = strings.TrimSpace(rootDomain)
	if clusterSlug == "" || rootDomain == "" {
		return nil, fmt.Errorf("cluster slug and root domain are required")
	}
	domain := fmt.Sprintf("*.%s.%s", clusterSlug, rootDomain)

	if _, _, _, err := m.IssueCertificate(ctx, platformCertTenantID, domain, email); err != nil {
		return nil, err
	}

	return m.GetCertificate(ctx, platformCertTenantID, domain)
}

func (m *CertManager) getOrCreateUser(ctx context.Context, tenantID, email string) (*ACMEUser, error) {
	// Try DB (with tenant context)
	acc, err := m.store.GetACMEAccount(ctx, tenantID, email)
	if err == nil {
		// Parse Private Key
		block, _ := pem.Decode([]byte(acc.PrivateKeyPEM))
		key, parseKeyErr := x509.ParseECPrivateKey(block.Bytes)
		if parseKeyErr != nil {
			return nil, fmt.Errorf("failed to parse stored private key: %w", parseKeyErr)
		}

		// Parse Registration
		var reg registration.Resource
		if unmarshalErr := json.Unmarshal([]byte(acc.Registration), &reg); unmarshalErr != nil {
			return nil, fmt.Errorf("failed to parse stored registration: %w", unmarshalErr)
		}

		return &ACMEUser{
			Email:        email,
			Registration: &reg,
			key:          key,
		}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, fmt.Errorf("failed to check ACME account: %w", err)
	}

	// Generate new
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &ACMEUser{
		Email: email,
		key:   key,
	}, nil
}

func (m *CertManager) saveUser(ctx context.Context, tenantID string, user *ACMEUser) error {
	// Serialize Key
	ecKey, ok := user.key.(*ecdsa.PrivateKey)
	if !ok {
		return fmt.Errorf("unexpected key type: expected *ecdsa.PrivateKey")
	}
	keyBytes, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		return err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	// Serialize Registration
	regJSON, err := json.Marshal(user.Registration)
	if err != nil {
		return err
	}

	acc := &store.ACMEAccount{
		Email:         user.Email,
		Registration:  string(regJSON),
		PrivateKeyPEM: string(keyPEM),
	}
	return m.store.SaveACMEAccount(ctx, tenantID, acc)
}
