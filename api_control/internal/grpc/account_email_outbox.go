package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

const (
	accountEmailPurposeVerification = "verification"

	accountEmailOutboxBaseBackoff = 30 * time.Second
	accountEmailOutboxMaxBackoff  = 30 * time.Minute
	accountEmailOutboxPollPeriod  = 15 * time.Second
	// accountEmailOutboxLease hides a claimed row from other replicas. One row per batch: an SMTP send is bounded
	// by accountEmailOutboxItemTimeout and settlement by SettleTimeout, together well inside the lease.
	accountEmailOutboxLease         = 60 * time.Second
	accountEmailOutboxItemTimeout   = 30 * time.Second
	accountEmailOutboxSettleTimeout = 5 * time.Second
	accountEmailOutboxBatchSize     = 1
	// accountEmailOutboxWindow matches the verification link lifetime: past it the request is abandoned and the
	// user asks for a new link.
	accountEmailOutboxWindow             = 24 * time.Hour
	accountEmailOutboxAlertAfterAttempts = 6
)

func accountEmailOutboxConfig() outbox.Config {
	return outbox.Config{
		BaseBackoff:        accountEmailOutboxBaseBackoff,
		MaxBackoff:         accountEmailOutboxMaxBackoff,
		BatchSize:          accountEmailOutboxBatchSize,
		PollPeriod:         accountEmailOutboxPollPeriod,
		Lease:              accountEmailOutboxLease,
		SettleTimeout:      accountEmailOutboxSettleTimeout,
		AlertAfterAttempts: accountEmailOutboxAlertAfterAttempts,
	}
}

type accountEmailOutboxRow struct {
	userID     string
	tenantID   string
	purpose    string
	attempts   int
	leaseToken string
}

// enqueueAccountEmailTx records a pending account email in the caller's transaction, so the email obligation
// commits or rolls back with the user or token change that requires it.
func enqueueAccountEmailTx(ctx context.Context, exec outboxExecutor, userID, tenantID, purpose string) error {
	if err := commodoredb.New(exec).EnqueueAccountEmail(ctx, commodoredb.EnqueueAccountEmailParams{
		UserID: userID, TenantID: tenantID, Purpose: purpose,
	}); err != nil {
		return fmt.Errorf("enqueue %s email: %w", purpose, err)
	}
	return nil
}

// kickAccountEmailOutbox delivers just-enqueued emails now instead of at the next poll; the claim lease keeps it
// safe alongside the polling worker.
func (s *CommodoreServer) kickAccountEmailOutbox() {
	if s.accountEmailKickFn != nil {
		s.accountEmailKickFn()
		return
	}
	if s.db == nil {
		return
	}
	go s.accountEmailOutboxWorker().ProcessBatch(context.Background())
}

func (s *CommodoreServer) accountEmailOutboxWorker() *outbox.Worker[accountEmailOutboxRow] {
	cfg := accountEmailOutboxConfig()
	// RecordFailure logs the alert itself with the row identity; the worker would log it a second time.
	cfg.AlertAfterAttempts = 0
	return &outbox.Worker[accountEmailOutboxRow]{
		Config:     cfg,
		Store:      &accountEmailOutboxStore{server: s},
		Dispatcher: &accountEmailOutboxDispatcher{server: s},
		Logger:     s.logger,
		AlertLabel: "account email",
	}
}

func (s *CommodoreServer) runAccountEmailOutboxWorker(ctx context.Context) {
	if s.db == nil {
		return
	}
	s.accountEmailOutboxWorker().Run(ctx)
}

// accountEmailClaimID carries the full row identity through the generic worker so settlement stays tenant-fenced.
func accountEmailClaimID(row accountEmailOutboxRow) string {
	return row.tenantID + "|" + row.userID + "|" + row.purpose
}

func parseAccountEmailClaimID(id string) (tenantID, userID, purpose string) {
	parts := strings.SplitN(id, "|", 3)
	if len(parts) != 3 {
		return "", "", ""
	}
	return parts[0], parts[1], parts[2]
}

type accountEmailOutboxStore struct{ server *CommodoreServer }

func (st *accountEmailOutboxStore) ClaimBatch(ctx context.Context, _ int, _ time.Duration) ([]outbox.Claim[accountEmailOutboxRow], error) {
	rows, err := st.server.claimAccountEmailOutboxBatch(ctx)
	if err != nil {
		return nil, err
	}
	claims := make([]outbox.Claim[accountEmailOutboxRow], 0, len(rows))
	for _, row := range rows {
		claims = append(claims, outbox.Claim[accountEmailOutboxRow]{ID: accountEmailClaimID(row), Attempts: row.attempts, Payload: row, LeaseToken: row.leaseToken})
	}
	return claims, nil
}

func (st *accountEmailOutboxStore) MarkCompleted(ctx context.Context, id string) error {
	return st.MarkCompletedToken(ctx, id, "")
}

func (st *accountEmailOutboxStore) RecordFailure(ctx context.Context, id string, currentAttempts int, _ []string, cause error, _ time.Duration) error {
	return st.RecordFailureToken(ctx, id, currentAttempts, nil, cause, 0, "")
}

func (st *accountEmailOutboxStore) MarkCompletedToken(ctx context.Context, id, leaseToken string) error {
	tenantID, userID, purpose := parseAccountEmailClaimID(id)
	if tenantID == "" {
		return fmt.Errorf("complete account email %q: malformed claim identity", id)
	}
	return commodoredb.New(st.server.db).CompleteAccountEmail(ctx, commodoredb.CompleteAccountEmailParams{
		UserID: userID, Purpose: purpose, TenantID: tenantID, LeaseToken: leaseToken,
	})
}

func (st *accountEmailOutboxStore) RecordFailureToken(ctx context.Context, id string, currentAttempts int, _ []string, cause error, _ time.Duration, leaseToken string) error {
	tenantID, userID, purpose := parseAccountEmailClaimID(id)
	if tenantID == "" {
		return fmt.Errorf("record account email failure %q: malformed claim identity", id)
	}
	nextAttempts := currentAttempts + 1
	last := ""
	if cause != nil {
		last = cause.Error()
	}
	backoff := outbox.ComputeBackoff(accountEmailOutboxConfig(), currentAttempts)
	if err := commodoredb.New(st.server.db).FailAccountEmail(ctx, commodoredb.FailAccountEmailParams{
		Attempts: int32(nextAttempts), BackoffMs: backoff.Milliseconds(), LastError: last,
		UserID: userID, Purpose: purpose, TenantID: tenantID, LeaseToken: leaseToken,
	}); err != nil {
		return fmt.Errorf("record account email failure: %w", err)
	}
	fields := logging.Fields{
		"user_id": userID, "tenant_id": tenantID, "purpose": purpose,
		"attempts": nextAttempts, "retry_in_ms": backoff.Milliseconds(), "cause": last,
	}
	if nextAttempts >= accountEmailOutboxAlertAfterAttempts {
		st.server.logger.WithFields(fields).Error("Account email delivery keeps failing; SMTP relay or sender identity likely broken")
	} else {
		st.server.logger.WithFields(fields).Warn("Account email delivery failed; will retry")
	}
	return nil
}

type accountEmailOutboxDispatcher struct{ server *CommodoreServer }

func (d *accountEmailOutboxDispatcher) Dispatch(ctx context.Context, row accountEmailOutboxRow) ([]string, error) {
	sendCtx, cancel := context.WithTimeout(ctx, accountEmailOutboxItemTimeout)
	defer cancel()
	if err := d.server.deliverAccountEmail(sendCtx, row); err != nil {
		return []string{row.userID}, err
	}
	return nil, nil
}

// deliverAccountEmail sends one account email. The raw verification token exists only for the duration of this
// send: its hash replaces the stored one first, so the emailed link is always the valid one. A user verified or
// deleted in the meantime has nothing left to deliver.
func (s *CommodoreServer) deliverAccountEmail(ctx context.Context, row accountEmailOutboxRow) error {
	if row.purpose != accountEmailPurposeVerification {
		return fmt.Errorf("unsupported account email purpose %q", row.purpose)
	}
	queries := commodoredb.New(s.db)
	recipient, err := queries.GetAccountEmailRecipient(ctx, commodoredb.GetAccountEmailRecipientParams{UserID: row.userID, TenantID: row.tenantID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load recipient: %w", err)
	}
	if recipient.Verified || strings.TrimSpace(recipient.Email) == "" {
		return nil
	}

	token, err := generateSecureToken(32)
	if err != nil {
		return fmt.Errorf("generate verification token: %w", err)
	}
	updated, err := queries.SetUnverifiedUserVerificationToken(ctx, commodoredb.SetUnverifiedUserVerificationTokenParams{
		VerificationToken: hashToken(token),
		TokenExpiresAt:    time.Now().Add(24 * time.Hour),
		UserID:            row.userID,
		TenantID:          row.tenantID,
	})
	if err != nil {
		return fmt.Errorf("store verification token: %w", err)
	}
	if updated == 0 {
		return nil
	}

	send := s.sendVerificationEmail
	if s.verificationEmailSendFn != nil {
		send = s.verificationEmailSendFn
	}
	if err := send(recipient.Email, token); err != nil {
		return fmt.Errorf("send verification email: %w", err)
	}
	s.logger.WithFields(logging.Fields{
		"user_id": row.userID, "tenant_id": row.tenantID, "attempt": row.attempts + 1,
	}).Info("Verification email sent")
	return nil
}

// claimAccountEmailOutboxBatch abandons requests past the verification window, then claims and leases due rows in
// one transaction.
func (s *CommodoreServer) claimAccountEmailOutboxBatch(ctx context.Context) ([]accountEmailOutboxRow, error) {
	var out []accountEmailOutboxRow
	err := database.WithRetryablePostgresTxWithHook(ctx, s.db, nil, func(error, int) {
		s.recycleIdlePostgresConns()
	}, func(tx *sql.Tx) error {
		queries := commodoredb.New(tx)
		abandoned, err := queries.AbandonExpiredAccountEmails(ctx, accountEmailOutboxWindow.Milliseconds())
		if err != nil {
			return fmt.Errorf("abandon expired account emails: %w", err)
		}
		if abandoned > 0 {
			s.logger.WithField("count", abandoned).Warn("Abandoned undelivered verification emails past the link window")
		}
		claimed, err := queries.ClaimAccountEmailBatch(ctx, int32(accountEmailOutboxBatchSize))
		if err != nil {
			return err
		}
		batch := make([]accountEmailOutboxRow, 0, len(claimed))
		for _, row := range claimed {
			leaseToken, lErr := queries.LeaseAccountEmail(ctx, commodoredb.LeaseAccountEmailParams{
				LeaseMs: accountEmailOutboxLease.Milliseconds(), UserID: row.UserID, Purpose: row.Purpose, TenantID: row.TenantID,
			})
			if lErr != nil {
				return fmt.Errorf("lease account email %s: %w", row.UserID, lErr)
			}
			if !leaseToken.Valid {
				return fmt.Errorf("lease account email %s: database returned NULL lease token", row.UserID)
			}
			batch = append(batch, accountEmailOutboxRow{
				userID: row.UserID, tenantID: row.TenantID, purpose: row.Purpose, attempts: int(row.Attempts), leaseToken: leaseToken.String,
			})
		}
		out = batch
		return nil
	})
	return out, err
}
