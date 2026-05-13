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
		return existing, nil
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

// RemoveCustomDomain marks a custom domain for teardown. The worker
// clears Bunny challenge records + cert material before deleting the row.
// Idempotent on absent rows.
func (m *CertManager) RemoveCustomDomain(ctx context.Context, tenantID, domain string) error {
	tenantID = strings.TrimSpace(tenantID)
	domain = strings.TrimSpace(strings.ToLower(domain))
	if tenantID == "" || domain == "" {
		return fmt.Errorf("tenantID and domain are required")
	}
	if err := m.store.SetTenantCustomDomainStatus(ctx, tenantID, domain, "tearing_down", ""); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
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
	return m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, "verified", "")
}

func setVerifyFailure(ctx context.Context, st customDomainStore, row store.TenantCustomDomain, msg string) error {
	if err := st.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, row.Status, msg); err != nil {
		return err
	}
	return fmt.Errorf("%s", msg)
}

// IssueCustomDomainCertificate runs ACME DNS-01 for a verified customer
// domain. The customer CNAMEs `_acme-challenge.<domain>` into the
// Navigator-owned acme-dns.{root} subzone; lego writes the TXT there
// through Bunny (the provider that owns the subzone), regardless of who
// hosts DNS for the customer's apex. Issuer + cert_expires_at land on
// the lifecycle row; the cert + key live in navigator.certificates via
// SaveCertificate.
func (m *CertManager) IssueCustomDomainCertificate(ctx context.Context, row store.TenantCustomDomain, email string) error {
	if email = strings.TrimSpace(email); email == "" {
		return fmt.Errorf("email required for ACME issuance")
	}
	if err := m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, "cert_issuing", ""); err != nil {
		return fmt.Errorf("status cert_issuing: %w", err)
	}
	_, _, expiresAt, issuer, err := m.IssueCertificateViaBunnyWithIssuer(ctx, row.TenantID, row.Domain, email)
	if err != nil {
		if statusErr := m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, "cert_failed", err.Error()); statusErr != nil {
			return fmt.Errorf("acme + status update: %w (status: %w)", err, statusErr)
		}
		return err
	}
	expSQL := sql.NullTime{}
	if !expiresAt.IsZero() {
		expSQL = sql.NullTime{Valid: true, Time: expiresAt}
	}
	if err := m.store.SetTenantCustomDomainCertMetadata(ctx, row.TenantID, row.Domain, issuer, expSQL); err != nil {
		return fmt.Errorf("cert metadata: %w", err)
	}
	return m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, "cert_issued", "")
}

// FinalizeCustomDomainRemoval drops the cert material from
// navigator.certificates and then deletes the tenant_custom_domains row.
// Cert deletion runs first; the lifecycle row is the operator-facing
// breadcrumb and stays until cert cleanup succeeds.
func (m *CertManager) FinalizeCustomDomainRemoval(ctx context.Context, tenantID, domain string) error {
	if err := m.store.DeleteCertificate(ctx, tenantID, domain); err != nil {
		return fmt.Errorf("delete cert material: %w", err)
	}
	return m.store.DeleteTenantCustomDomain(ctx, tenantID, domain)
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
	SetTenantCustomDomainStatus(ctx context.Context, tenantID, domain, status, errMsg string) error
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
		case "pending_verification", "cert_failed":
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
			if err := m.IssueCustomDomainCertificate(ctx, row, email); err != nil {
				continue
			}
			processed++
		case "tearing_down":
			if err := m.FinalizeCustomDomainRemoval(ctx, row.TenantID, row.Domain); err != nil {
				continue
			}
			processed++
		}
	}
	return processed, nil
}
