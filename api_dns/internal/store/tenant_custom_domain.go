package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_dns/internal/database/navigatordb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"google.golang.org/protobuf/proto"
)

func tenantCustomDomainFromDB(row navigatordb.NavigatorTenantCustomDomain) TenantCustomDomain {
	return TenantCustomDomain{
		TenantID: row.TenantID, Domain: row.Domain, Status: row.Status, AcmeDNSSubdomain: row.AcmeDnsSubdomain,
		IssuerID: row.IssuerID, LastVerifiedAt: row.LastVerifiedAt, CertIssuedAt: row.CertIssuedAt,
		CertExpiresAt: row.CertExpiresAt, LastError: row.LastError,
		LastRenewalError: row.LastRenewalError, LastRenewalErrorAt: row.LastRenewalErrorAt, NextAttemptAt: row.NextAttemptAt,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		VerificationStartedAt: row.VerificationStartedAt, FailureReportedAt: row.FailureReportedAt,
	}
}

// DomainEventSource and DomainEventSchema identify Navigator's domain events
// and the schema holding its outbox.
const (
	DomainEventSource = "navigator"
	DomainEventSchema = "navigator"
)

// enqueueCustomDomainEvent writes a custom domain event through tx, the
// transaction that commits the status change it reports. The worker makes
// these changes, so the event carries no caller.
func enqueueCustomDomainEvent(ctx context.Context, tx *sql.Tx, tenantID, domain string, msg proto.Message) error {
	ev, err := events.New(DomainEventSource, tenantID, domain, msg)
	if err != nil {
		return fmt.Errorf("build custom domain event: %w", err)
	}
	return outbox.Enqueue(ctx, tx, DomainEventSchema, ev)
}

// MarkTenantCustomDomainVerified moves a domain from expectedStatus to
// verified. The first verification (from pending_verification) commits
// custom_domain.verified with the change; re-verification of a cert_failed
// domain before its retry emits nothing. Returns false when the row left
// expectedStatus.
func (s *Store) MarkTenantCustomDomainVerified(ctx context.Context, tenantID, domain, expectedStatus string) (bool, error) {
	var transitioned bool
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		n, err := s.q.WithTx(tx).SetTenantCustomDomainStatus(ctx, navigatordb.SetTenantCustomDomainStatusParams{
			TenantID: tenantID, Domain: domain, ExpectedStatus: expectedStatus, Status: "verified",
		})
		if err != nil {
			return err
		}
		transitioned = n == 1
		if !transitioned || expectedStatus != "pending_verification" {
			return nil
		}
		return enqueueCustomDomainEvent(ctx, tx, tenantID, domain, &publicv1.CustomDomainVerified{Domain: domain})
	})
	if err != nil {
		return false, err
	}
	return transitioned, nil
}

// ExpireTenantCustomDomainVerifications fails every pending_verification
// domain whose verification started more than period ago and commits a
// custom_domain.failed event for each with the change. Returns how many
// domains it failed.
func (s *Store) ExpireTenantCustomDomainVerifications(ctx context.Context, period time.Duration) (int, error) {
	var expired int
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		rows, err := s.q.WithTx(tx).ExpireTenantCustomDomainVerifications(ctx, int64(period/time.Second))
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := enqueueCustomDomainEvent(ctx, tx, row.TenantID, row.Domain, &publicv1.CustomDomainFailed{
				Domain: row.Domain, Reason: publicv1.CustomDomainFailureReason_CUSTOM_DOMAIN_FAILURE_REASON_VERIFICATION_EXPIRED,
			}); err != nil {
				return err
			}
		}
		expired = len(rows)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return expired, nil
}

// EnsureTenantCustomDomain inserts or updates the custom-domain row. On
// conflict the worker-driven status is preserved unless teardown is being
// reactivated. Reactivation keeps the stable ACME-DNS slug but restarts
// verification and clears stale certificate, renewal, and retry metadata.
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
// transition; last_error is cleared unless errMsg is non-empty, and
// next_attempt_at survives only while the row stays cert_failed.
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

// CompleteTenantCustomDomainIssuance moves a cert_issuing domain to
// cert_issued with the serving tenant bundle's issuer and expiry, clearing
// issuance and renewal errors. Returns false when the row left cert_issuing.
func (s *Store) CompleteTenantCustomDomainIssuance(ctx context.Context, tenantID, domain, issuerID string, expiresAt sql.NullTime) (bool, error) {
	var n int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		n, queryErr = s.q.CompleteTenantCustomDomainIssuance(ctx, navigatordb.CompleteTenantCustomDomainIssuanceParams{
			TenantID: tenantID, Domain: domain, IssuerID: issuerID, CertExpiresAt: expiresAt,
		})
		return queryErr
	})
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// FailTenantCustomDomainIssuance moves a cert_issuing domain to cert_failed
// and schedules its next attempt retryAfter from the database clock. The
// first failure since the domain's last successful issuance or reactivation
// commits custom_domain.failed with the change; a failed retry does not.
func (s *Store) FailTenantCustomDomainIssuance(ctx context.Context, tenantID, domain, errMsg string, retryAfter time.Duration) (bool, error) {
	var transitioned bool
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		firstFailure, err := s.q.WithTx(tx).FailTenantCustomDomainIssuance(ctx, navigatordb.FailTenantCustomDomainIssuanceParams{
			TenantID: tenantID, Domain: domain, ErrMsg: errMsg, RetryAfterSeconds: int64(retryAfter / time.Second),
		})
		if errors.Is(err, sql.ErrNoRows) {
			transitioned = false
			return nil
		}
		if err != nil {
			return err
		}
		transitioned = true
		if !firstFailure {
			return nil
		}
		return enqueueCustomDomainEvent(ctx, tx, tenantID, domain, &publicv1.CustomDomainFailed{
			Domain: domain, Reason: publicv1.CustomDomainFailureReason_CUSTOM_DOMAIN_FAILURE_REASON_CERTIFICATE_FAILED,
		})
	})
	if err != nil {
		return false, err
	}
	return transitioned, nil
}

// RefreshTenantCustomDomainServedCertificate records the tenant bundle that
// now serves each participating custom domain of the tenant listed in
// domains, and clears any recorded renewal failure. Returns the rows updated.
func (s *Store) RefreshTenantCustomDomainServedCertificate(ctx context.Context, tenantID string, domains []string, issuerID string, expiresAt sql.NullTime) (int64, error) {
	var n int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		n, queryErr = s.q.RefreshTenantCustomDomainServedCertificate(ctx, navigatordb.RefreshTenantCustomDomainServedCertificateParams{
			TenantID: tenantID, Domains: domains, IssuerID: issuerID, CertExpiresAt: expiresAt,
		})
		return queryErr
	})
	return n, err
}

// RecordTenantCustomDomainRenewalFailure stamps a failed tenant bundle
// renewal on the tenant's participating custom domains without changing their
// status: the previous bundle is still valid and keeps serving the SANs.
func (s *Store) RecordTenantCustomDomainRenewalFailure(ctx context.Context, tenantID, errMsg string) (int64, error) {
	var n int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		n, queryErr = s.q.RecordTenantCustomDomainRenewalFailure(ctx, navigatordb.RecordTenantCustomDomainRenewalFailureParams{
			TenantID: tenantID, ErrMsg: errMsg,
		})
		return queryErr
	})
	return n, err
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
