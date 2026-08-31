package logic

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"

	"frameworks/api_dns/internal/store"
)

// AcmeDNSZoneLabel is the Navigator-owned subzone under {root} used for
// ACME-DNS-01 delegation. Customers CNAME
// `_acme-challenge.{their-domain}` at `{slug}.acme-dns.{root}`; Navigator
// writes the TXT to that delegated target via its existing Bunny provider
// during the DNS-01 challenge.
const AcmeDNSZoneLabel = "acme-dns"

// EnsureCustomDomain creates or refreshes a tenant_custom_domains row.
// Generates a stable random `acme_dns_subdomain` slug on first insert; the
// slug is reused on subsequent calls so the customer's CNAME never has to
// change. Status defaults to pending_verification.
func (m *CertManager) EnsureCustomDomain(ctx context.Context, tenantID, domain string) (*store.TenantCustomDomain, error) {
	tenantID = strings.TrimSpace(tenantID)
	domain = strings.TrimSpace(strings.ToLower(domain))
	if tenantID == "" || domain == "" {
		return nil, fmt.Errorf("tenantID and domain are required")
	}
	if existing, err := m.store.GetTenantCustomDomain(ctx, tenantID, domain); err == nil {
		if existing.Status != "tearing_down" {
			return existing, nil
		}
		return m.store.EnsureTenantCustomDomain(ctx, tenantID, domain, existing.AcmeDNSSubdomain)
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	slug, err := generateAcmeDNSSlug()
	if err != nil {
		return nil, fmt.Errorf("generate acme-dns slug: %w", err)
	}
	return m.store.EnsureTenantCustomDomain(ctx, tenantID, domain, slug)
}

// GetTenantCustomDomain returns the row for a single (tenant_id, domain)
// pair, or store.ErrNotFound when absent.
func (m *CertManager) GetTenantCustomDomain(ctx context.Context, tenantID, domain string) (*store.TenantCustomDomain, error) {
	return m.store.GetTenantCustomDomain(ctx, tenantID, domain)
}

// RemoveCustomDomain marks a custom domain for teardown. The worker refreshes
// any ready tenant SAN bundle, then atomically removes the domain certificate,
// unused tenant ACME account, and lifecycle row. Idempotent on absent rows.
func (m *CertManager) RemoveCustomDomain(ctx context.Context, tenantID, domain string) error {
	tenantID = strings.TrimSpace(tenantID)
	domain = strings.TrimSpace(strings.ToLower(domain))
	if tenantID == "" || domain == "" {
		return fmt.Errorf("tenantID and domain are required")
	}
	if _, err := m.store.SetTenantCustomDomainStatus(ctx, tenantID, domain, "", "tearing_down", ""); err != nil {
		return err
	}
	return nil
}

// VerifyCustomDomain resolves the customer's CNAMEs and confirms they
// point at the platform's expected targets:
//
//   - `{domain}` → `{tenant_subdomain}.cdn.{root}` (traffic delegation)
//   - `_acme-challenge.{domain}` → `{acme_dns_subdomain}.acme-dns.{root}`
//     (ACME-DNS-01 delegation)
//
// On success transitions pending_verification → verified. Verification
// errors leave the row in pending_verification with last_error set so the
// next worker tick retries.
func (m *CertManager) VerifyCustomDomain(ctx context.Context, row store.TenantCustomDomain, tenantSubdomain, rootDomain string) error {
	rootDomain = strings.TrimSpace(strings.ToLower(strings.TrimSuffix(rootDomain, ".")))
	tenantSubdomain = strings.TrimSpace(strings.ToLower(tenantSubdomain))
	if rootDomain == "" {
		return fmt.Errorf("rootDomain required")
	}
	if tenantSubdomain == "" {
		return fmt.Errorf("tenant subdomain required (custom domain must follow a tenant alias)")
	}
	expectedTraffic := tenantSubdomain + "." + TenantAliasZoneLabel + "." + rootDomain + "."
	expectedAcme := row.AcmeDNSSubdomain + "." + AcmeDNSZoneLabel + "." + rootDomain + "."

	// Both lookups use the default resolver with the call's context so a
	// stuck DNS server can't pin the verify worker. LookupCNAME returns the
	// final-target FQDN (already lowercased + trailing dot per Go's resolver
	// contract). A non-matching target leaves the row pending and the next
	// tick retries.
	resolver := net.DefaultResolver
	trafficCNAME, err := resolver.LookupCNAME(ctx, row.Domain)
	if err != nil {
		return setVerifyFailure(ctx, m.store, row, fmt.Sprintf("traffic CNAME lookup failed: %v", err))
	}
	if !strings.EqualFold(trafficCNAME, expectedTraffic) {
		return setVerifyFailure(ctx, m.store, row,
			fmt.Sprintf("traffic CNAME mismatch: got %q, expected %q", trafficCNAME, expectedTraffic))
	}
	acmeCNAME, err := resolver.LookupCNAME(ctx, "_acme-challenge."+row.Domain)
	if err != nil {
		return setVerifyFailure(ctx, m.store, row, fmt.Sprintf("acme-challenge CNAME lookup failed: %v", err))
	}
	if !strings.EqualFold(acmeCNAME, expectedAcme) {
		return setVerifyFailure(ctx, m.store, row,
			fmt.Sprintf("acme-challenge CNAME mismatch: got %q, expected %q", acmeCNAME, expectedAcme))
	}
	_, err = m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, row.Status, "verified", "")
	return err
}

func setVerifyFailure(ctx context.Context, st customDomainStore, row store.TenantCustomDomain, msg string) error {
	if _, err := st.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, row.Status, row.Status, msg); err != nil {
		return err
	}
	return fmt.Errorf("%s", msg)
}

func (m *CertManager) IssueCustomDomainCertificate(ctx context.Context, row store.TenantCustomDomain, rootDomain, email string) error {
	if email = strings.TrimSpace(email); email == "" {
		return fmt.Errorf("email required for ACME issuance")
	}
	transitioned, err := m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, row.Status, "cert_issuing", "")
	if err != nil {
		return fmt.Errorf("status cert_issuing: %w", err)
	}
	if !transitioned {
		return nil
	}
	_, _, expiresAt, issuer, err := m.IssueCertificateViaBunnyWithIssuer(ctx, row.TenantID, row.Domain, email)
	if err != nil {
		return m.failCustomDomainIssue(ctx, row, rootDomain, email, err)
	}
	expSQL := sql.NullTime{}
	if !expiresAt.IsZero() {
		expSQL = sql.NullTime{Valid: true, Time: expiresAt}
	}
	metadataWritten, err := m.store.SetTenantCustomDomainCertMetadata(ctx, row.TenantID, row.Domain, "cert_issuing", issuer, expSQL)
	if err != nil {
		return fmt.Errorf("cert metadata: %w", err)
	}
	if !metadataWritten {
		return nil
	}
	alias, aliasErr := m.store.GetTenantAlias(ctx, row.TenantID)
	if aliasErr != nil && !errors.Is(aliasErr, store.ErrNotFound) {
		return m.failCustomDomainIssue(ctx, row, rootDomain, email, fmt.Errorf("tenant alias lookup: %w", aliasErr))
	}
	if alias != nil && alias.Status == "cert_issued" {
		if _, bundleErr := m.EnsureTenantWildcardCertificate(ctx, row.TenantID, alias.Subdomain, TenantAliasZoneLabel, rootDomain, email); bundleErr != nil {
			return m.failCustomDomainIssue(ctx, row, rootDomain, email, fmt.Errorf("refresh tenant tls bundle: %w", bundleErr))
		}
	}
	_, err = m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, "cert_issuing", "cert_issued", "")
	return err
}

func (m *CertManager) failCustomDomainIssue(ctx context.Context, row store.TenantCustomDomain, rootDomain, email string, cause error) error {
	if _, statusErr := m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, "cert_issuing", "cert_failed", cause.Error()); statusErr != nil {
		return fmt.Errorf("cert issue + status update: %w (status: %w)", cause, statusErr)
	}
	// The failed domain no longer participates in the aggregate tenant SAN
	// set. Rebuild immediately so an already-issued alias bundle cannot keep
	// retrying the poisoned SAN until its own expiry window.
	alias, aliasErr := m.store.GetTenantAlias(ctx, row.TenantID)
	if aliasErr != nil && !errors.Is(aliasErr, store.ErrNotFound) {
		return errors.Join(cause, fmt.Errorf("refresh tenant tls bundle authority: %w", aliasErr))
	}
	if alias != nil && alias.Status == "cert_issued" {
		if _, bundleErr := m.EnsureTenantWildcardCertificate(ctx, row.TenantID, alias.Subdomain, TenantAliasZoneLabel, rootDomain, email); bundleErr != nil {
			return errors.Join(cause, fmt.Errorf("refresh tenant tls bundle after domain failure: %w", bundleErr))
		}
	}
	return cause
}

func (m *CertManager) FinalizeCustomDomainRemoval(ctx context.Context, tenantID, domain, rootDomain, email string) error {
	if alias, err := m.store.GetTenantAlias(ctx, tenantID); err == nil && alias != nil && alias.Status == "cert_issued" {
		if _, bundleErr := m.EnsureTenantWildcardCertificate(ctx, tenantID, alias.Subdomain, TenantAliasZoneLabel, rootDomain, email); bundleErr != nil {
			return fmt.Errorf("refresh tenant tls bundle: %w", bundleErr)
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("tenant alias lookup: %w", err)
	}
	_, err := m.store.FinalizeTenantCustomDomainRemoval(ctx, tenantID, domain)
	return err
}

// generateAcmeDNSSlug returns a 64-bit random hex slug for use under
// acme-dns.{root}. Collisions are vanishingly unlikely; the store's UNIQUE
// constraint on (tenant_id, domain) is the actual idempotency boundary —
// the slug is just an opaque per-record path.
func generateAcmeDNSSlug() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// customDomainStore is the slice of *store.Store this file needs. Mirrors
// the existing tenantAliasStore shape.
type customDomainStore interface {
	SetTenantCustomDomainStatus(ctx context.Context, tenantID, domain, expectedStatus, status, errMsg string) (bool, error)
}

// ProcessPendingCustomDomains runs the per-tick custom-domain reconciler.
// Returns the number of rows whose status transitioned.
//
// pendingVerification → verified: when both CNAMEs resolve to platform.
// verified            → cert_issued: after a successful ACME order.
// tearing_down        → (deleted): after worker teardown completes.
//
// tenantSubdomainLookup returns the tenant's alias subdomain (the value
// from navigator.tenant_aliases.subdomain), used to compute the expected
// traffic CNAME target. Returning empty + nil means "tenant has no alias"
// and the custom domain stays in pending_verification until it does.
func (m *CertManager) ProcessPendingCustomDomains(ctx context.Context, rootDomain, email string, tenantSubdomainLookup func(ctx context.Context, tenantID string) (string, error)) (int, error) {
	rootDomain = strings.TrimSpace(rootDomain)
	if rootDomain == "" {
		return 0, fmt.Errorf("rootDomain is required")
	}
	rows, err := m.store.ListTenantCustomDomainsByStatus(ctx, []string{"pending_verification", "verified", "cert_failed", "tearing_down"})
	if err != nil {
		return 0, fmt.Errorf("list custom domains: %w", err)
	}
	processed := 0
	for _, row := range rows {
		switch row.Status {
		case "cert_failed":
			// A previous issuance failure may have been persisted before the
			// aggregate tenant-bundle cleanup succeeded. Retry that cleanup on
			// every pass before re-verifying/re-admitting the failed SAN.
			if err := m.refreshTenantBundleAfterCustomDomainFailure(ctx, row.TenantID, rootDomain, email); err != nil {
				continue
			}
			sub, lookupErr := tenantSubdomainLookup(ctx, row.TenantID)
			if lookupErr != nil || sub == "" {
				continue
			}
			if err := m.VerifyCustomDomain(ctx, row, sub, rootDomain); err != nil {
				continue
			}
			processed++
		case "pending_verification":
			sub, lookupErr := tenantSubdomainLookup(ctx, row.TenantID)
			if lookupErr != nil || sub == "" {
				continue
			}
			if err := m.VerifyCustomDomain(ctx, row, sub, rootDomain); err != nil {
				continue
			}
			processed++
		// Fall through to issuance on the next tick to avoid blocking
		// the worker on a slow ACME order.
		case "verified":
			if err := m.IssueCustomDomainCertificate(ctx, row, rootDomain, email); err != nil {
				continue
			}
			processed++
		case "tearing_down":
			if err := m.FinalizeCustomDomainRemoval(ctx, row.TenantID, row.Domain, rootDomain, email); err != nil {
				continue
			}
			processed++
		}
	}
	return processed, nil
}

func (m *CertManager) refreshTenantBundleAfterCustomDomainFailure(ctx context.Context, tenantID, rootDomain, email string) error {
	alias, err := m.store.GetTenantAlias(ctx, tenantID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if alias.Status != "cert_issued" {
		return nil
	}
	_, err = m.EnsureTenantWildcardCertificate(ctx, tenantID, alias.Subdomain, TenantAliasZoneLabel, rootDomain, email)
	return err
}
