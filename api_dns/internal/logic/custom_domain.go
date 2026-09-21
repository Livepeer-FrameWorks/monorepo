package logic

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"frameworks/api_dns/internal/store"
)

// AcmeDNSZoneLabel is the Navigator-owned subzone under {root} used for
// ACME-DNS-01 delegation. Customers CNAME
// `_acme-challenge.{their-domain}` at `{slug}.acme-dns.{root}`; Navigator
// writes the TXT to that delegated target via its existing Bunny provider
// during the DNS-01 challenge.
const AcmeDNSZoneLabel = "acme-dns"

// CustomDomainVerificationPeriod is how long a custom domain may stay in
// pending_verification. A domain whose CNAMEs have not verified by then is
// marked verification_failed and custom_domain.failed is emitted; requesting
// the domain again restarts verification.
const CustomDomainVerificationPeriod = 7 * 24 * time.Hour

// EnsureCustomDomain creates or refreshes a tenant_custom_domains row.
// Generates a stable random `acme_dns_subdomain` slug on first insert; the
// slug is reused on subsequent calls so the customer's CNAME never has to
// change. Status defaults to pending_verification, and a tearing_down or
// verification_failed row restarts verification.
func (m *CertManager) EnsureCustomDomain(ctx context.Context, tenantID, domain string) (*store.TenantCustomDomain, error) {
	tenantID = strings.TrimSpace(tenantID)
	domain = strings.TrimSpace(strings.ToLower(domain))
	if tenantID == "" || domain == "" {
		return nil, fmt.Errorf("tenantID and domain are required")
	}
	if existing, err := m.store.GetTenantCustomDomain(ctx, tenantID, domain); err == nil {
		if existing.Status != "tearing_down" && existing.Status != "verification_failed" {
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

// ListTenantCustomDomains exposes tenant-scoped lifecycle rows to Navigator's
// status RPC. It is used when the desired domain was cleared and the repair
// caller no longer knows which stale applied domain needs teardown.
func (m *CertManager) ListTenantCustomDomains(ctx context.Context, tenantID string) ([]store.TenantCustomDomain, error) {
	return m.store.ListTenantCustomDomains(ctx, tenantID)
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
// On success transitions pending_verification → verified, committing
// custom_domain.verified with it (re-verifying a cert_failed domain emits
// nothing). Verification errors leave the row in its status with last_error
// set so the next worker tick retries.
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
	_, err = m.store.MarkTenantCustomDomainVerified(ctx, row.TenantID, row.Domain, row.Status)
	return err
}

func setVerifyFailure(ctx context.Context, st customDomainStore, row store.TenantCustomDomain, msg string) error {
	if _, err := st.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, row.Status, row.Status, msg); err != nil {
		return err
	}
	return fmt.Errorf("%s", msg)
}

// customDomainIssueRetryDelay spaces tenant bundle orders after a custom
// domain's SAN failed, so a persistently broken domain does not place an ACME
// order on every reconcile tick.
const customDomainIssueRetryDelay = 15 * time.Minute

// IssueCustomDomainCertificate admits a verified domain into the tenant bundle,
// the only certificate that serves it. The first bundle order belongs to the
// tenant alias, so a domain waits in pending_alias until that bundle is issued
// rather than joining (and possibly poisoning) the alias's own issuance.
func (m *CertManager) IssueCustomDomainCertificate(ctx context.Context, row store.TenantCustomDomain, rootDomain, email string) error {
	if email = strings.TrimSpace(email); email == "" {
		return fmt.Errorf("email required for ACME issuance")
	}
	alias, err := m.store.GetTenantAlias(ctx, row.TenantID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("tenant alias lookup: %w", err)
	}
	if alias == nil || alias.Status != "cert_issued" {
		if _, statusErr := m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, "verified", "pending_alias", ""); statusErr != nil {
			return fmt.Errorf("status pending_alias: %w", statusErr)
		}
		return nil
	}
	transitioned, err := m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, "verified", "cert_issuing", "")
	if err != nil {
		return fmt.Errorf("status cert_issuing: %w", err)
	}
	if !transitioned {
		return nil
	}
	bundle, err := m.EnsureTenantWildcardCertificate(ctx, row.TenantID, alias.Subdomain, TenantAliasZoneLabel, rootDomain, email)
	if err == nil && !slices.Contains(bundle.Domains, row.Domain) {
		err = fmt.Errorf("tenant tls bundle does not cover %s", row.Domain)
	}
	if err != nil {
		return m.failCustomDomainIssue(ctx, row, fmt.Errorf("tenant tls bundle: %w", err))
	}
	expiresAt := sql.NullTime{}
	if !bundle.ExpiresAt.IsZero() {
		expiresAt = sql.NullTime{Valid: true, Time: bundle.ExpiresAt}
	}
	if _, err := m.store.CompleteTenantCustomDomainIssuance(ctx, row.TenantID, row.Domain, bundle.IssuerCA, expiresAt); err != nil {
		return fmt.Errorf("status cert_issued: %w", err)
	}
	return nil
}

// failCustomDomainIssue records the failure only. The failed domain leaves the
// tenant SAN set, and the last-good bundle never contained it, so there is
// nothing to rebuild; the next scheduled bundle order simply omits it.
func (m *CertManager) failCustomDomainIssue(ctx context.Context, row store.TenantCustomDomain, cause error) error {
	if _, statusErr := m.store.FailTenantCustomDomainIssuance(ctx, row.TenantID, row.Domain, cause.Error(), customDomainIssueRetryDelay); statusErr != nil {
		return fmt.Errorf("cert issue + status update: %w (status: %w)", cause, statusErr)
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
// pending_verification → verified:      both CNAMEs resolve to the platform.
// pending_verification → verification_failed: CustomDomainVerificationPeriod passed.
// verified             → pending_alias: the tenant alias bundle is not issued yet.
// pending_alias        → verified:      the tenant alias bundle is issued.
// verified             → cert_issued:   the tenant bundle now includes the SAN.
// verified             → cert_failed:   the tenant bundle order failed.
// cert_failed          → verified:      next_attempt_at passed and CNAMEs still resolve.
// tearing_down         → (deleted):     the tenant bundle no longer includes the SAN.
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
	expired, err := m.store.ExpireTenantCustomDomainVerifications(ctx, CustomDomainVerificationPeriod)
	if err != nil {
		return 0, fmt.Errorf("expire custom domain verifications: %w", err)
	}
	rows, err := m.store.ListTenantCustomDomainsByStatus(ctx, []string{"pending_verification", "verified", "pending_alias", "cert_failed", "tearing_down"})
	if err != nil {
		return 0, fmt.Errorf("list custom domains: %w", err)
	}
	now := time.Now()
	processed := expired
	for _, row := range rows {
		switch row.Status {
		case "pending_alias":
			alias, aliasErr := m.store.GetTenantAlias(ctx, row.TenantID)
			if aliasErr != nil || alias.Status != "cert_issued" {
				continue
			}
			promoted, statusErr := m.store.SetTenantCustomDomainStatus(ctx, row.TenantID, row.Domain, "pending_alias", "verified", "")
			if statusErr != nil || !promoted {
				continue
			}
			processed++
			row.Status = "verified"
			if issueErr := m.IssueCustomDomainCertificate(ctx, row, rootDomain, email); issueErr != nil {
				// The issuance path persists the failure on the row.
				continue
			}
		case "cert_failed":
			if row.NextAttemptAt.Valid && row.NextAttemptAt.Time.After(now) {
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
