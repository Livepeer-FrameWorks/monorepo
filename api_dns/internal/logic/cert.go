package logic

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"frameworks/api_dns/internal/store"

	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
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
	DeleteCertificate(ctx context.Context, tenantID, domain string) error
	GetTLSBundle(ctx context.Context, bundleID string) (*store.TLSBundle, error)
	SaveTLSBundle(ctx context.Context, bundle *store.TLSBundle) error
	GetACMEAccount(ctx context.Context, tenantID, email, ca string) (*store.ACMEAccount, error)
	SaveACMEAccount(ctx context.Context, tenantID string, acc *store.ACMEAccount) error
	// Tenant alias intent and DNS readiness state.
	EnsureTenantAlias(ctx context.Context, tenantID, subdomain string) (*store.TenantAlias, error)
	GetTenantAlias(ctx context.Context, tenantID string) (*store.TenantAlias, error)
	ListPendingTenantAliases(ctx context.Context) ([]store.TenantAlias, error)
	SetTenantAliasStatus(ctx context.Context, tenantID, status, errMsg string) error
	DeleteTenantAlias(ctx context.Context, tenantID string) error
	UpsertTenantEdgeApplyState(ctx context.Context, st *store.TenantEdgeApplyState) error
	TenantAliasHasDNS(ctx context.Context, tenantID string) (bool, error)
	DeleteTenantEdgeApplyState(ctx context.Context, tenantID string) error
	DeleteTenantEdgeApplyStateForCluster(ctx context.Context, tenantID, clusterID string) error
	// Tenant custom domain (BYO domain) lifecycle.
	EnsureTenantCustomDomain(ctx context.Context, tenantID, domain, acmeDNSSubdomain string) (*store.TenantCustomDomain, error)
	GetTenantCustomDomain(ctx context.Context, tenantID, domain string) (*store.TenantCustomDomain, error)
	ListTenantCustomDomainsByStatus(ctx context.Context, statuses []string) ([]store.TenantCustomDomain, error)
	ListTenantCustomDomains(ctx context.Context, tenantID string) ([]store.TenantCustomDomain, error)
	SetTenantCustomDomainStatus(ctx context.Context, tenantID, domain, status, errMsg string) error
	SetTenantCustomDomainCertMetadata(ctx context.Context, tenantID, domain, issuerID string, expiresAt sql.NullTime) error
	DeleteTenantCustomDomain(ctx context.Context, tenantID, domain string) error
}

type acmeClient interface {
	SetDNS01Provider(provider challenge.Provider) error
	Register() (*registration.Resource, error)
	// RegisterWithEAB is used by CAs that require External Account
	// Binding (Google Trust Services). Let's Encrypt clients can leave
	// this returning an error; it will not be called.
	RegisterWithEAB(opts registration.RegisterEABOptions) (*registration.Resource, error)
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

// UseBunnyForMediaZones wires the DNS-01 provider selector: any domain
// whose authoritative zone is Bunny-delegated (cluster zones, per-service
// global zones like foghorn.{root}, and the tenant cdn.{root} zone) gets
// the Bunny provider; everything else (root operator services in
// Cloudflare) uses Cloudflare.
//
// UseBunnyForClusterZones preserves the older method name used by
// existing Navigator initialization code.
func (m *CertManager) UseBunnyForMediaZones(rootDomain string) {
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

// UseBunnyForClusterZones delegates to UseBunnyForMediaZones.
func (m *CertManager) UseBunnyForClusterZones(rootDomain string) {
	m.UseBunnyForMediaZones(rootDomain)
}

// bunnyDelegatedLabels returns the root child zones Navigator owns in Bunny:
// the shared tenant alias zone and the global media entrypoint zones.
func bunnyDelegatedLabels() map[string]struct{} {
	out := map[string]struct{}{
		TenantAliasZoneLabel: {},
	}
	for _, label := range GlobalServiceZoneLabels() {
		out[label] = struct{}{}
	}
	return out
}

// GlobalServiceZoneLabels returns the per-service Bunny zone labels that
// receive global smart records, e.g. foghorn.frameworks.network. The list is
// code-owned because these are first-class product URLs, not deploy-time knobs.
func GlobalServiceZoneLabels() []string {
	return pkgdns.GlobalRootServiceZoneLabels()
}

// TenantAliasZoneLabel is the single shared zone label under the root
// that holds all per-paying-tenant DNS records
// (acme.cdn.frameworks.network, foghorn.acme.cdn.frameworks.network, ...).
const TenantAliasZoneLabel = pkgdns.TenantAliasZoneLabel

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
// authoritative for the requested domain. Cache-first: returns the
// existing cert if not expiring within 30 days. On miss/expiry, issues
// via the configured CA order with rate-limit fallback.
//
// Renewal correctness: when renewing an existing cert, IssueCertificate
// pins to the same CA that originally issued (store.Certificate.IssuerCA)
// so ARI works. Brand-new certs use the resolved CA order.
//
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
	var existingIssuer CAProvider
	cert, err := m.store.GetCertificate(ctx, tenantID, domain)
	if err == nil {
		// Check expiry (renew if < 30 days remaining)
		if time.Until(cert.ExpiresAt) > 30*24*time.Hour {
			return cert.CertPEM, cert.KeyPEM, cert.ExpiresAt, nil
		}
		// If expiring soon, proceed to renewal logic below, pinned to
		// the same CA so ARI continuity holds.
		if cert.IssuerCA != "" {
			existingIssuer = CAProvider(cert.IssuerCA)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", "", time.Time{}, fmt.Errorf("failed to check certificate cache: %w", err)
	}

	cas := caOrder()
	if existingIssuer != "" {
		// Renewal: pin to the original CA. No fallback: if the CA is
		// down, we want to fail visibly rather than silently switching
		// keys mid-renewal.
		cas = []CAProvider{existingIssuer}
	}

	var certificatePEM, privateKeyPEM string
	var expiry time.Time
	var issuedBy CAProvider
	var lastErr error
	for _, ca := range cas {
		certificatePEM, privateKeyPEM, expiry, lastErr = m.obtainCertificate(ctx, tenantID, []string{domain}, email, ca)
		if lastErr == nil {
			issuedBy = ca
			break
		}
		if !isRateLimitError(lastErr) {
			break
		}
		// Rate-limited: try next CA in the order.
	}
	if lastErr != nil {
		return "", "", time.Time{}, lastErr
	}

	// 9. Save to DB with tenant context. Record which CA signed it
	// so renewals route correctly.
	newCert := &store.Certificate{
		Domain:    domain,
		CertPEM:   certificatePEM,
		KeyPEM:    privateKeyPEM,
		ExpiresAt: expiry,
		IssuerCA:  string(issuedBy),
	}
	if err := m.store.SaveCertificate(ctx, tenantID, newCert); err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to save certificate: %w", err)
	}

	return newCert.CertPEM, newCert.KeyPEM, expiry, nil
}

// IssueCertificateViaBunny issues a certificate for an arbitrary domain
// using Bunny as the DNS-01 provider. The challenge TXT lands in the
// Navigator-owned acme-dns.{root} subzone; the customer points
// `_acme-challenge.<domain>` at their assigned acme-dns record via CNAME.
// Used for tenant custom domains where the cert domain is not under our
// platform root. Wraps issueCertificateViaBunny so existing callers that
// don't care about the issuer can ignore the extra return.
func (m *CertManager) IssueCertificateViaBunny(ctx context.Context, tenantID, domain, email string) (certPEM, keyPEM string, expiresAt time.Time, err error) {
	certPEM, keyPEM, expiresAt, _, err = m.issueCertificateViaBunny(ctx, tenantID, domain, email)
	return
}

// IssueCertificateViaBunnyWithIssuer is the same as IssueCertificateViaBunny
// but also returns the issuing CA so the caller (custom-domain lifecycle)
// can persist it on the tenant_custom_domains row.
func (m *CertManager) IssueCertificateViaBunnyWithIssuer(ctx context.Context, tenantID, domain, email string) (certPEM, keyPEM string, expiresAt time.Time, issuer string, err error) {
	return m.issueCertificateViaBunny(ctx, tenantID, domain, email)
}

func (m *CertManager) issueCertificateViaBunny(ctx context.Context, tenantID, domain, email string) (certPEM, keyPEM string, expiresAt time.Time, issuer string, err error) {
	if domain == "" || email == "" {
		return "", "", time.Time{}, "", fmt.Errorf("domain and email are required")
	}
	domain = normalizeDomains([]string{domain})[0]

	var existingIssuer CAProvider
	cert, err := m.store.GetCertificate(ctx, tenantID, domain)
	if err == nil {
		if time.Until(cert.ExpiresAt) > 30*24*time.Hour {
			return cert.CertPEM, cert.KeyPEM, cert.ExpiresAt, cert.IssuerCA, nil
		}
		if cert.IssuerCA != "" {
			existingIssuer = CAProvider(cert.IssuerCA)
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", "", time.Time{}, "", fmt.Errorf("failed to check certificate cache: %w", err)
	}

	cas := caOrder()
	if existingIssuer != "" {
		cas = []CAProvider{existingIssuer}
	}

	providerFactory := m.bunnyDNSProviderFactory

	var (
		certificatePEM, privateKeyPEM string
		expiry                        time.Time
		issuedBy                      CAProvider
		lastErr                       error
	)
	for _, ca := range cas {
		certificatePEM, privateKeyPEM, expiry, lastErr = m.obtainCertificateWith(ctx, tenantID, []string{domain}, email, ca, providerFactory)
		if lastErr == nil {
			issuedBy = ca
			break
		}
		if !isRateLimitError(lastErr) {
			break
		}
	}
	if lastErr != nil {
		return "", "", time.Time{}, "", lastErr
	}

	newCert := &store.Certificate{
		Domain:    domain,
		CertPEM:   certificatePEM,
		KeyPEM:    privateKeyPEM,
		ExpiresAt: expiry,
		IssuerCA:  string(issuedBy),
	}
	if err := m.store.SaveCertificate(ctx, tenantID, newCert); err != nil {
		return "", "", time.Time{}, "", fmt.Errorf("failed to save certificate: %w", err)
	}
	return newCert.CertPEM, newCert.KeyPEM, expiry, string(issuedBy), nil
}

// RenewCertificate is the cert-renewal entry point used by the background
// renewal worker. It dispatches custom-domain renewals to the Bunny ACME-DNS
// path (which is the only provider configured for the acme-dns.{root}
// delegated subzone) and platform domains through the standard
// allowlisted-provider path.
func (m *CertManager) RenewCertificate(ctx context.Context, tenantID, domain, email string) (certPEM, keyPEM string, expiresAt time.Time, err error) {
	if tenantID != "" && domain != "" {
		if row, lookupErr := m.store.GetTenantCustomDomain(ctx, tenantID, domain); lookupErr == nil && row != nil {
			c, k, exp, issuer, issueErr := m.issueCertificateViaBunny(ctx, tenantID, domain, email)
			if issueErr != nil {
				return "", "", time.Time{}, issueErr
			}
			expSQL := sql.NullTime{}
			if !exp.IsZero() {
				expSQL = sql.NullTime{Valid: true, Time: exp}
			}
			if metaErr := m.store.SetTenantCustomDomainCertMetadata(ctx, tenantID, domain, issuer, expSQL); metaErr != nil {
				return "", "", time.Time{}, fmt.Errorf("custom-domain cert metadata: %w", metaErr)
			}
			return c, k, exp, nil
		} else if lookupErr != nil && !errors.Is(lookupErr, store.ErrNotFound) {
			return "", "", time.Time{}, fmt.Errorf("custom-domain lookup: %w", lookupErr)
		}
	}
	return m.IssueCertificate(ctx, tenantID, domain, email)
}

func (m *CertManager) EnsureTLSBundle(ctx context.Context, bundleID string, domains []string, email string) (*store.TLSBundle, error) {
	if strings.HasPrefix(strings.TrimSpace(bundleID), "tenant:") && hasDomainOutsidePlatformAllowlist(domains) {
		return m.ensureTLSBundle(ctx, bundleID, domains, email, m.bunnyDNSProviderFactory, false)
	}
	return m.ensureTLSBundle(ctx, bundleID, domains, email, nil, true)
}

func (m *CertManager) ensureTLSBundle(ctx context.Context, bundleID string, domains []string, email string, providerFactory func() (challenge.Provider, error), enforceAllowlist bool) (*store.TLSBundle, error) {
	bundleID = strings.TrimSpace(bundleID)
	domains = normalizeDomains(domains)
	if bundleID == "" || len(domains) == 0 || strings.TrimSpace(email) == "" {
		return nil, fmt.Errorf("bundle_id, domains, and email are required")
	}

	if enforceAllowlist {
		for _, domain := range domains {
			if !isDomainAllowed(domain) {
				return nil, fmt.Errorf("domain %q is not allowed for certificate issuance", domain)
			}
		}
	}
	if providerFactory == nil {
		providerFactory = m.dnsProviderFactory
		if m.dnsProviderForDomainsFactory != nil {
			providerFactory = func() (challenge.Provider, error) {
				return m.dnsProviderForDomainsFactory(domains)
			}
		}
	}

	var pinnedCA CAProvider
	existing, err := m.store.GetTLSBundle(ctx, bundleID)
	switch {
	case err == nil:
		if slices.Equal(existing.Domains, domains) && time.Until(existing.ExpiresAt) > 30*24*time.Hour {
			return existing, nil
		}
		// Pin renewals to the original issuing CA so the ACME account,
		// ARI hints, and rate-limit pool stay consistent across rotations.
		// Without this, a renewal would re-resolve via the current CA
		// order and could silently migrate a bundle off the CA that
		// originally issued it.
		if existing.IssuerCA != "" {
			pinnedCA = CAProvider(existing.IssuerCA)
		}
	case !errors.Is(err, store.ErrNotFound):
		return nil, fmt.Errorf("failed to check tls bundle cache: %w", err)
	}

	var cas []CAProvider
	if pinnedCA != "" {
		cas = []CAProvider{pinnedCA}
		for _, other := range caOrder() {
			if other != pinnedCA {
				cas = append(cas, other)
				break
			}
		}
	} else {
		cas = caOrder()
	}
	var certificatePEM, privateKeyPEM string
	var expiry time.Time
	var issuingCA CAProvider
	var lastErr error
	for _, ca := range cas {
		certificatePEM, privateKeyPEM, expiry, lastErr = m.obtainCertificateWith(ctx, platformCertTenantID, domains, email, ca, providerFactory)
		if lastErr == nil {
			issuingCA = ca
			break
		}
		if !isRateLimitError(lastErr) {
			break
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}

	bundle := &store.TLSBundle{
		BundleID:  bundleID,
		Domains:   domains,
		CertPEM:   certificatePEM,
		KeyPEM:    privateKeyPEM,
		ExpiresAt: expiry,
		IssuerCA:  string(issuingCA),
	}
	if err := m.store.SaveTLSBundle(ctx, bundle); err != nil {
		return nil, fmt.Errorf("failed to save tls bundle: %w", err)
	}
	if strings.HasPrefix(bundleID, "tenant:") {
		if err := m.updateCustomDomainBundleMetadata(ctx, strings.TrimPrefix(bundleID, "tenant:"), bundle); err != nil {
			return nil, err
		}
	}
	return bundle, nil
}

func hasDomainOutsidePlatformAllowlist(domains []string) bool {
	for _, domain := range normalizeDomains(domains) {
		if !isDomainAllowed(domain) {
			return true
		}
	}
	return false
}

func (m *CertManager) updateCustomDomainBundleMetadata(ctx context.Context, tenantID string, bundle *store.TLSBundle) error {
	rows, err := m.store.ListTenantCustomDomains(ctx, tenantID)
	if err != nil {
		return fmt.Errorf("list tenant custom domains: %w", err)
	}
	domainSet := make(map[string]struct{}, len(bundle.Domains))
	for _, domain := range bundle.Domains {
		domainSet[domain] = struct{}{}
	}
	expSQL := sql.NullTime{}
	if !bundle.ExpiresAt.IsZero() {
		expSQL = sql.NullTime{Valid: true, Time: bundle.ExpiresAt}
	}
	for _, row := range rows {
		if _, ok := domainSet[row.Domain]; !ok {
			continue
		}
		if err := m.store.SetTenantCustomDomainCertMetadata(ctx, tenantID, row.Domain, bundle.IssuerCA, expSQL); err != nil {
			return fmt.Errorf("custom-domain cert metadata: %w", err)
		}
	}
	return nil
}

func (m *CertManager) GetTLSBundle(ctx context.Context, bundleID string) (*store.TLSBundle, error) {
	return m.store.GetTLSBundle(ctx, bundleID)
}

func (m *CertManager) obtainCertificate(ctx context.Context, tenantID string, domains []string, email string, ca CAProvider) (certPEM, keyPEM string, expiresAt time.Time, err error) {
	providerFactory := m.dnsProviderFactory
	if m.dnsProviderForDomainsFactory != nil {
		providerFactory = func() (challenge.Provider, error) {
			return m.dnsProviderForDomainsFactory(domains)
		}
	}
	return m.obtainCertificateWith(ctx, tenantID, domains, email, ca, providerFactory)
}

// obtainCertificateWith runs ACME issuance with an explicit DNS-01 provider
// factory. Custom-domain issuance uses this so the lego client always
// writes the TXT challenge through Bunny (Navigator owns the acme-dns
// subzone the customer CNAMEs into), regardless of the cert domain.
func (m *CertManager) obtainCertificateWith(ctx context.Context, tenantID string, domains []string, email string, ca CAProvider, providerFactory func() (challenge.Provider, error)) (certPEM, keyPEM string, expiresAt time.Time, err error) {
	if ca == "" {
		ca = CADefaultIssuer
	}
	caCfg, caErr := resolveCAConfig(ca)
	if caErr != nil {
		return "", "", time.Time{}, fmt.Errorf("resolve CA %s: %w", ca, caErr)
	}

	user, err := m.getOrCreateUser(ctx, tenantID, email, ca)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to load ACME user: %w", err)
	}

	config := lego.NewConfig(user)
	config.CADirURL = caCfg.DirectoryURL
	config.Certificate.KeyType = certcrypto.EC256

	client, err := m.acmeClientFactory(config)
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to create lego client: %w", err)
	}

	if providerFactory == nil {
		providerFactory = m.dnsProviderFactory
	}

	provider, err := providerFactory()
	if err != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to create DNS provider: %w", err)
	}
	if challengeErr := client.SetDNS01Provider(&resilientDNSProvider{provider: provider}); challengeErr != nil {
		return "", "", time.Time{}, fmt.Errorf("failed to set DNS provider: %w", challengeErr)
	}

	if user.Registration == nil {
		reg, regErr := registerACMEUser(client, caCfg)
		if regErr != nil {
			return "", "", time.Time{}, fmt.Errorf("registration failed (%s): %w", ca, regErr)
		}
		user.Registration = reg
		if saveErr := m.saveUser(ctx, tenantID, user, ca); saveErr != nil {
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
	delegated := bunnyDelegatedLabels()
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
		// Wildcard one-label under root: cluster wildcard or future
		// per-tenant zone wildcard. Always Bunny.
		if len(labels) == 1 && labels[0] != "" && isWildcard {
			return true
		}
		// Exact (non-wildcard) one-label under root: Bunny only when
		// that label is explicitly NS-delegated (the 8 per-service
		// global zones and the cdn tenant zone). Cloudflare keeps the
		// operator services (bridge, grafana, etc.).
		if len(labels) == 1 && labels[0] != "" && !isWildcard {
			if _, ok := delegated[labels[0]]; ok {
				return true
			}
			continue
		}
		// Anything two-or-more labels deep is definitely a Bunny zone.
		// (cluster-scoped or tenant-scoped sub-records).
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

func (l *legoClient) RegisterWithEAB(opts registration.RegisterEABOptions) (*registration.Resource, error) {
	return l.client.Registration.RegisterWithExternalAccountBinding(opts)
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

// Bundle IDs for the global platform multi-SAN certs. These match the
// bundle_id values Foghorn tags when distributing the certs to nodes via
// ConfigSeed.
const (
	BundleIDPoolAssignedGlobal = "platform:pool-multi"
	BundleIDPlatformEdgeGlobal = "platform:edge-multi"
)

// EnsureTenantWildcardCertificate issues the per-tenant wildcard cert
// covering *.{subdomain}.{tenantZone}.{root} plus the apex
// {subdomain}.{tenantZone}.{root}. Bundle ID matches what Foghorn
// distributes ("tenant:{tenantID}").
//
// tenantZone is the shared tenant alias label ("cdn"). rootDomain is e.g.
// "frameworks.network". DNS-01 runs against the shared cdn.{root} Bunny zone
// via the existing media-zone predicate.
func (m *CertManager) EnsureTenantWildcardCertificate(ctx context.Context, tenantID, subdomain, tenantZone, rootDomain, email string) (*store.TLSBundle, error) {
	tenantID = strings.TrimSpace(tenantID)
	subdomain = strings.TrimSpace(strings.ToLower(subdomain))
	tenantZone = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(tenantZone, ".")))
	rootDomain = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(rootDomain, ".")))
	if tenantID == "" || subdomain == "" || tenantZone == "" || rootDomain == "" {
		return nil, fmt.Errorf("tenantID, subdomain, tenantZone, and rootDomain are required")
	}
	apex := subdomain + "." + tenantZone + "." + rootDomain
	wildcard := "*." + apex
	bundleID := "tenant:" + tenantID
	domains := []string{apex, wildcard}
	customDomains, err := m.verifiedCustomDomainsForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	domains = append(domains, customDomains...)
	if len(customDomains) == 0 {
		return m.EnsureTLSBundle(ctx, bundleID, domains, email)
	}
	return m.ensureTLSBundle(ctx, bundleID, domains, email, m.bunnyDNSProviderFactory, false)
}

func (m *CertManager) verifiedCustomDomainsForTenant(ctx context.Context, tenantID string) ([]string, error) {
	rows, err := m.store.ListTenantCustomDomains(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list tenant custom domains: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		switch row.Status {
		case "verified", "cert_issuing", "cert_issued":
			out = append(out, row.Domain)
		}
	}
	return out, nil
}

// EnsurePoolAssignedGlobalCertificate issues a single multi-SAN cert
// covering foghorn.{root}, chandler.{root}, livepeer.{root}. Distributed
// only to platform-operated foghorn/chandler/livepeer pool nodes.
func (m *CertManager) EnsurePoolAssignedGlobalCertificate(ctx context.Context, rootDomain, email string) (*store.TLSBundle, error) {
	rootDomain = strings.TrimSpace(rootDomain)
	if rootDomain == "" {
		return nil, fmt.Errorf("root domain is required")
	}
	domains := []string{
		"foghorn." + rootDomain,
		"chandler." + rootDomain,
		"livepeer." + rootDomain,
	}
	return m.EnsureTLSBundle(ctx, BundleIDPoolAssignedGlobal, domains, email)
}

// EnsurePlatformEdgeGlobalCertificate issues a single multi-SAN cert
// covering edge.{root}, edge-ingest.{root}, edge-egress.{root},
// edge-storage.{root}, edge-processing.{root}. Distributed only to
// `platform_official` cluster edges. Never to third-party operators.
func (m *CertManager) EnsurePlatformEdgeGlobalCertificate(ctx context.Context, rootDomain, email string) (*store.TLSBundle, error) {
	rootDomain = strings.TrimSpace(rootDomain)
	if rootDomain == "" {
		return nil, fmt.Errorf("root domain is required")
	}
	domains := []string{
		"edge." + rootDomain,
		"edge-ingest." + rootDomain,
		"edge-egress." + rootDomain,
		"edge-storage." + rootDomain,
		"edge-processing." + rootDomain,
	}
	return m.EnsureTLSBundle(ctx, BundleIDPlatformEdgeGlobal, domains, email)
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

func (m *CertManager) getOrCreateUser(ctx context.Context, tenantID, email string, ca CAProvider) (*ACMEUser, error) {
	// Try DB with tenant + CA context; registrations are CA-specific.
	acc, err := m.store.GetACMEAccount(ctx, tenantID, email, string(ca))
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

func (m *CertManager) saveUser(ctx context.Context, tenantID string, user *ACMEUser, ca CAProvider) error {
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
		CA:            string(ca),
	}
	return m.store.SaveACMEAccount(ctx, tenantID, acc)
}
