package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_dns/internal/database/navigatordb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

func tenantCustomDomainFromDB(row navigatordb.NavigatorTenantCustomDomain) TenantCustomDomain {
	return TenantCustomDomain{
		TenantID: row.TenantID, Domain: row.Domain, Status: row.Status, AcmeDNSSubdomain: row.AcmeDnsSubdomain,
		IssuerID: row.IssuerID, LastVerifiedAt: row.LastVerifiedAt, CertIssuedAt: row.CertIssuedAt,
		CertExpiresAt: row.CertExpiresAt, LastError: row.LastError, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

// EnsureTenantCustomDomain inserts or updates the custom-domain row. On
// conflict the worker-driven status is preserved unless teardown is being
// reactivated. Reactivation keeps the stable ACME-DNS slug but restarts
// verification and clears stale certificate metadata.
func (s *Store) EnsureTenantCustomDomain(ctx context.Context, tenantID, domain, acmeDNSSubdomain string) (*TenantCustomDomain, error) {
	var row navigatordb.NavigatorTenantCustomDomain
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		row, queryErr = s.q.EnsureTenantCustomDomain(ctx, navigatordb.EnsureTenantCustomDomainParams{
			TenantID: tenantID, Domain: domain, AcmeDnsSubdomain: acmeDNSSubdomain,
		})
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	d := tenantCustomDomainFromDB(row)
	return &d, nil
}

// GetTenantCustomDomain returns the custom-domain row by (tenant_id,
// domain), or ErrNotFound when absent.
func (s *Store) GetTenantCustomDomain(ctx context.Context, tenantID, domain string) (*TenantCustomDomain, error) {
	var row navigatordb.NavigatorTenantCustomDomain
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		row, queryErr = s.q.GetTenantCustomDomain(ctx, navigatordb.GetTenantCustomDomainParams{TenantID: tenantID, Domain: domain})
		return queryErr
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d := tenantCustomDomainFromDB(row)
	return &d, nil
}

// ListTenantCustomDomainsByStatus returns rows in any of the supplied
// statuses, ordered oldest-updated-first.
func (s *Store) ListTenantCustomDomainsByStatus(ctx context.Context, statuses []string) ([]TenantCustomDomain, error) {
	if len(statuses) == 0 {
		return nil, nil
	}
	var rows []navigatordb.NavigatorTenantCustomDomain
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.ListTenantCustomDomainsByStatus(ctx, statuses)
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	var out []TenantCustomDomain
	for _, row := range rows {
		out = append(out, tenantCustomDomainFromDB(row))
	}
	return out, nil
}

// ListTenantCustomDomains returns every row for a tenant.
func (s *Store) ListTenantCustomDomains(ctx context.Context, tenantID string) ([]TenantCustomDomain, error) {
	var rows []navigatordb.NavigatorTenantCustomDomain
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.ListTenantCustomDomains(ctx, tenantID)
		return queryErr
	})
	if err != nil {
		return nil, err
	}
	var out []TenantCustomDomain
	for _, row := range rows {
		out = append(out, tenantCustomDomainFromDB(row))
	}
	return out, nil
}

// SetTenantCustomDomainStatus transitions the lifecycle. cert_issued and
// last_verified_at timestamps are stamped automatically on the matching
// transition; last_error is cleared unless errMsg is non-empty.
func (s *Store) SetTenantCustomDomainStatus(ctx context.Context, tenantID, domain, expectedStatus, status, errMsg string) (bool, error) {
	var n int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		n, queryErr = s.q.SetTenantCustomDomainStatus(ctx, navigatordb.SetTenantCustomDomainStatusParams{
			TenantID: tenantID, Domain: domain, ExpectedStatus: expectedStatus, Status: status, ErrMsg: errMsg,
		})
		return queryErr
	})
	if err != nil {
		return false, err
	}
	return n == 1, err
}

// SetTenantCustomDomainCertMetadata records the issuer and cert expiry
// after a successful ACME issuance. Called from the cert-issuance worker.
func (s *Store) SetTenantCustomDomainCertMetadata(ctx context.Context, tenantID, domain, expectedStatus, issuerID string, expiresAt sql.NullTime) (bool, error) {
	var n int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		n, queryErr = s.q.SetTenantCustomDomainCertMetadata(ctx, navigatordb.SetTenantCustomDomainCertMetadataParams{
			TenantID: tenantID, Domain: domain, ExpectedStatus: expectedStatus, IssuerID: issuerID, CertExpiresAt: expiresAt,
		})
		return queryErr
	})
	if err != nil {
		return false, err
	}
	return n == 1, err
}

// FinalizeTenantCustomDomainRemoval atomically removes a still-tearing-down
// domain, its certificate, and the tenant ACME account when no other custom
// domain needs that account. A concurrent reactivation makes it a no-op.
func (s *Store) FinalizeTenantCustomDomainRemoval(ctx context.Context, tenantID, domain string) (bool, error) {
	var rows int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		rows, queryErr = s.q.FinalizeTenantCustomDomainRemoval(ctx, navigatordb.FinalizeTenantCustomDomainRemovalParams{
			TenantID: tenantID, Domain: domain,
		})
		return queryErr
	})
	return rows == 1, err
}

// DeleteTenantCustomDomain removes only the lifecycle row. Normal teardown uses
// FinalizeTenantCustomDomainRemoval so credential cleanup is atomic.
func (s *Store) DeleteTenantCustomDomain(ctx context.Context, tenantID, domain string) error {
	return database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return s.q.DeleteTenantCustomDomain(ctx, navigatordb.DeleteTenantCustomDomainParams{TenantID: tenantID, Domain: domain})
	})
}
