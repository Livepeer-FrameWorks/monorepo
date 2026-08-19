package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

type invoiceEmailPayload struct {
	InvoiceID string
	TenantID  string
	Recipient string
}

type invoiceEmailOutboxStore struct {
	db *sql.DB
}

type invoiceEmailDispatcher struct {
	jobs *JobManager
	send func(recipient, invoiceID string, amount, meteredAmount, grossMeteredAmount float64, currency string, dueDate time.Time, lineItems []EmailInvoiceLineItem) error
}

func enqueueInvoiceEmailTx(ctx context.Context, tx *sql.Tx, invoiceID, tenantID, recipient, status string) error {
	if tx == nil {
		return errors.New("enqueue invoice email: nil transaction")
	}
	if status != "pending" && status != "paid" {
		return nil
	}
	if invoiceID == "" || tenantID == "" {
		return errors.New("enqueue invoice email: invoice_id and tenant_id are required")
	}
	if strings.TrimSpace(recipient) == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO purser.invoice_email_outbox (invoice_id, tenant_id, recipient)
		VALUES ($1, $2, $3)
		ON CONFLICT (invoice_id) DO NOTHING
	`, invoiceID, tenantID, strings.TrimSpace(recipient))
	if err != nil {
		return fmt.Errorf("insert invoice email outbox: %w", err)
	}
	return nil
}

func invoiceEmailClaimID(tenantID, id string) string {
	return tenantID + "/" + id
}

func parseInvoiceEmailClaimID(claimID string) (tenantID, id string, err error) {
	parts := strings.SplitN(claimID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid invoice email claim id %q", claimID)
	}
	return parts[0], parts[1], nil
}

func (s *invoiceEmailOutboxStore) ClaimBatch(ctx context.Context, batchSize int, lease time.Duration) (claims []outbox.Claim[invoiceEmailPayload], err error) {
	if s.db == nil {
		return nil, errors.New("claim invoice emails: nil database")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, invoice_id, tenant_id, recipient, attempts
		FROM purser.invoice_email_outbox
		WHERE sent_at IS NULL
		  AND next_attempt_at <= NOW()
		  AND (claimed_at IS NULL OR claimed_at < NOW() - ($1 * interval '1 millisecond'))
		ORDER BY next_attempt_at, created_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, lease.Milliseconds(), batchSize)
	if err != nil {
		return nil, fmt.Errorf("select invoice email outbox: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	type emailCandidate struct {
		id, invoiceID, tenantID, recipient string
		attempts                           int
	}
	var candidates []emailCandidate
	for rows.Next() {
		var item emailCandidate
		if err := rows.Scan(&item.id, &item.invoiceID, &item.tenantID, &item.recipient, &item.attempts); err != nil {
			return nil, fmt.Errorf("scan invoice email outbox: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invoice email outbox: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	claims = make([]outbox.Claim[invoiceEmailPayload], 0, len(candidates))
	for _, candidate := range candidates {
		leaseToken := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			UPDATE purser.invoice_email_outbox
			SET claimed_at = NOW(), lease_token = $3, updated_at = NOW()
			WHERE id = $1 AND tenant_id = $2
		`, candidate.id, candidate.tenantID, leaseToken); err != nil {
			return nil, fmt.Errorf("lease invoice email outbox: %w", err)
		}
		claims = append(claims, outbox.Claim[invoiceEmailPayload]{
			ID:         invoiceEmailClaimID(candidate.tenantID, candidate.id),
			Attempts:   candidate.attempts,
			LeaseToken: leaseToken,
			Payload: invoiceEmailPayload{
				InvoiceID: candidate.invoiceID,
				TenantID:  candidate.tenantID,
				Recipient: candidate.recipient,
			},
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *invoiceEmailOutboxStore) MarkCompleted(ctx context.Context, claimID string) error {
	tenantID, id, err := parseInvoiceEmailClaimID(claimID)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE purser.invoice_email_outbox
		SET sent_at = NOW(), claimed_at = NULL, lease_token = NULL,
		    last_error = NULL, updated_at = NOW()
		WHERE id = $1 AND tenant_id = $2
	`, id, tenantID)
	return requireInvoiceEmailOutboxUpdate(result, err)
}

func (s *invoiceEmailOutboxStore) MarkCompletedToken(ctx context.Context, claimID, leaseToken string) error {
	tenantID, id, err := parseInvoiceEmailClaimID(claimID)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE purser.invoice_email_outbox
		SET sent_at = NOW(), claimed_at = NULL, lease_token = NULL,
		    last_error = NULL, updated_at = NOW()
		WHERE id = $1 AND tenant_id = $2 AND lease_token = $3
	`, id, tenantID, leaseToken)
	return requireInvoiceEmailOutboxUpdate(result, err)
}

func requireInvoiceEmailOutboxUpdate(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("invoice email lease no longer owned")
	}
	return nil
}

func (s *invoiceEmailOutboxStore) RecordFailure(ctx context.Context, claimID string, _ int, _ []string, cause error, backoff time.Duration) error {
	return s.recordFailure(ctx, claimID, cause, backoff, "")
}

func (s *invoiceEmailOutboxStore) RecordFailureToken(ctx context.Context, claimID string, _ int, _ []string, cause error, backoff time.Duration, leaseToken string) error {
	return s.recordFailure(ctx, claimID, cause, backoff, leaseToken)
}

func (s *invoiceEmailOutboxStore) recordFailure(ctx context.Context, claimID string, cause error, backoff time.Duration, leaseToken string) error {
	tenantID, id, err := parseInvoiceEmailClaimID(claimID)
	if err != nil {
		return err
	}
	message := "invoice email dispatch failed"
	if cause != nil {
		message = cause.Error()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE purser.invoice_email_outbox
		SET attempts = attempts + 1,
		    next_attempt_at = NOW() + ($3 * interval '1 millisecond'),
		    last_error = $4, claimed_at = NULL, lease_token = NULL,
		    updated_at = NOW()
		WHERE id = $1 AND tenant_id = $2
		  AND ($5 = '' OR lease_token::text = $5)
	`, id, tenantID, backoff.Milliseconds(), message, leaseToken)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected == 0 {
		return errors.New("invoice email lease no longer owned")
	}
	return nil
}

func (d *invoiceEmailDispatcher) Dispatch(ctx context.Context, payload invoiceEmailPayload) ([]string, error) {
	if d.jobs == nil || d.jobs.db == nil {
		return []string{"smtp"}, errors.New("invoice email dispatcher is not configured")
	}
	send := d.send
	if send == nil {
		if d.jobs.emailService == nil || !d.jobs.emailService.IsConfigured() {
			return []string{"smtp"}, errors.New("invoice email SMTP is not configured")
		}
		send = func(recipient, invoiceID string, amount, meteredAmount, grossMeteredAmount float64, currency string, dueDate time.Time, lineItems []EmailInvoiceLineItem) error {
			return d.jobs.emailService.SendInvoiceCreatedEmail(recipient, "", invoiceID, amount, meteredAmount, grossMeteredAmount, currency, dueDate, lineItems)
		}
	}

	var amount, meteredAmount, grossMeteredAmount float64
	var currency, status string
	var dueDate time.Time
	err := d.jobs.db.QueryRowContext(ctx, `
		SELECT amount::float8, metered_amount::float8,
		       gross_metered_amount::float8, currency, due_date, status
		FROM purser.billing_invoices
		WHERE id = $1 AND tenant_id = $2
	`, payload.InvoiceID, payload.TenantID).Scan(
		&amount, &meteredAmount, &grossMeteredAmount, &currency, &dueDate, &status,
	)
	if err != nil {
		return []string{"smtp"}, fmt.Errorf("load permanent invoice for email: %w", err)
	}
	if status == "draft" || status == "manual_review" {
		return []string{"smtp"}, fmt.Errorf("invoice %s is not permanent: status %s", payload.InvoiceID, status)
	}
	lineItems, err := d.jobs.loadEmailLineItems(ctx, payload.InvoiceID, payload.TenantID)
	if err != nil {
		return []string{"smtp"}, err
	}
	if len(lineItems) == 0 {
		return []string{"smtp"}, fmt.Errorf("invoice %s has no permanent line-item snapshot", payload.InvoiceID)
	}
	if err := send(payload.Recipient, payload.InvoiceID, amount, meteredAmount,
		grossMeteredAmount, currency, dueDate, lineItems); err != nil {
		return []string{"smtp"}, err
	}
	return nil, nil
}

func (jm *JobManager) runInvoiceEmailOutbox(ctx context.Context) {
	worker := &outbox.Worker[invoiceEmailPayload]{
		Config: outbox.Config{
			BaseBackoff:        time.Minute,
			MaxBackoff:         6 * time.Hour,
			BatchSize:          50,
			PollPeriod:         30 * time.Second,
			Lease:              2 * time.Minute,
			SettleTimeout:      30 * time.Second,
			AlertAfterAttempts: 5,
		},
		Store:      &invoiceEmailOutboxStore{db: jm.db},
		Dispatcher: &invoiceEmailDispatcher{jobs: jm},
		Logger:     jm.logger,
		AlertLabel: "invoice email outbox",
	}
	worker.Run(ctx)
}
