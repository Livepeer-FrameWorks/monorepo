package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

type invoiceEmailPayload struct {
	InvoiceID        string
	TenantID         string
	Recipient        string
	NotificationType string
	ReminderStage    int
}

const (
	invoiceCreatedNotification  = "invoice_created"
	overdueReminderNotification = "overdue_reminder"
)

type invoiceEmailOutboxStore struct {
	db *sql.DB
}

type invoiceEmailDispatcher struct {
	jobs         *JobManager
	send         func(recipient, invoiceID string, amount, meteredAmount, grossMeteredAmount float64, currency string, dueDate time.Time, lineItems []EmailInvoiceLineItem) error
	sendReminder func(recipient, invoiceID string, amount float64, currency string, daysPastDue int) error
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
	err := purserdb.New(tx).EnqueueInvoiceEmail(ctx, purserdb.EnqueueInvoiceEmailParams{
		InvoiceID: invoiceID,
		TenantID:  tenantID,
		Recipient: strings.TrimSpace(recipient),
	})
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

	queries := purserdb.New(tx)
	candidates, err := queries.ClaimInvoiceEmailCandidates(ctx, purserdb.ClaimInvoiceEmailCandidatesParams{
		LeaseMilliseconds: lease.Milliseconds(),
		BatchSize:         int32(batchSize),
	})
	if err != nil {
		return nil, fmt.Errorf("select invoice email outbox: %w", err)
	}

	claims = make([]outbox.Claim[invoiceEmailPayload], 0, len(candidates))
	for _, candidate := range candidates {
		leaseToken := uuid.New()
		affected, leaseErr := queries.LeaseInvoiceEmail(ctx, purserdb.LeaseInvoiceEmailParams{
			LeaseToken: uuid.NullUUID{UUID: leaseToken, Valid: true},
			ID:         candidate.ID,
			TenantID:   candidate.TenantID,
		})
		if leaseErr != nil {
			return nil, fmt.Errorf("lease invoice email outbox: %w", leaseErr)
		}
		if affected != 1 {
			return nil, errors.New("lease invoice email outbox: selected row disappeared")
		}
		claims = append(claims, outbox.Claim[invoiceEmailPayload]{
			ID:         invoiceEmailClaimID(candidate.TenantID.String(), candidate.ID.String()),
			Attempts:   int(candidate.Attempts),
			LeaseToken: leaseToken.String(),
			Payload: invoiceEmailPayload{
				InvoiceID:        candidate.InvoiceID.String(),
				TenantID:         candidate.TenantID.String(),
				Recipient:        candidate.Recipient,
				NotificationType: candidate.NotificationType,
				ReminderStage:    int(candidate.ReminderStage),
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
	affected, err := purserdb.New(s.db).CompleteInvoiceEmail(ctx, purserdb.CompleteInvoiceEmailParams{ID: id, TenantID: tenantID})
	return requireInvoiceEmailOutboxUpdate(affected, err)
}

func (s *invoiceEmailOutboxStore) MarkCompletedToken(ctx context.Context, claimID, leaseToken string) error {
	tenantID, id, err := parseInvoiceEmailClaimID(claimID)
	if err != nil {
		return err
	}
	affected, err := purserdb.New(s.db).CompleteInvoiceEmailWithLease(ctx, purserdb.CompleteInvoiceEmailWithLeaseParams{
		ID: id, TenantID: tenantID, LeaseToken: leaseToken,
	})
	return requireInvoiceEmailOutboxUpdate(affected, err)
}

func requireInvoiceEmailOutboxUpdate(affected int64, err error) error {
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
	queries := purserdb.New(s.db)
	var affected int64
	if leaseToken == "" {
		affected, err = queries.FailInvoiceEmail(ctx, purserdb.FailInvoiceEmailParams{
			BackoffMilliseconds: backoff.Milliseconds(),
			LastError:           sql.NullString{String: message, Valid: true},
			ID:                  id,
			TenantID:            tenantID,
		})
	} else {
		affected, err = queries.FailInvoiceEmailWithLease(ctx, purserdb.FailInvoiceEmailWithLeaseParams{
			BackoffMilliseconds: backoff.Milliseconds(),
			LastError:           sql.NullString{String: message, Valid: true},
			ID:                  id,
			TenantID:            tenantID,
			LeaseToken:          leaseToken,
		})
	}
	return requireInvoiceEmailOutboxUpdate(affected, err)
}

func (d *invoiceEmailDispatcher) Dispatch(ctx context.Context, payload invoiceEmailPayload) ([]string, error) {
	if d.jobs == nil || d.jobs.db == nil {
		return []string{"smtp"}, errors.New("invoice email dispatcher is not configured")
	}
	if payload.NotificationType == overdueReminderNotification {
		return d.dispatchOverdueReminder(ctx, payload)
	}
	if payload.NotificationType != "" && payload.NotificationType != invoiceCreatedNotification {
		return []string{"smtp"}, fmt.Errorf("unsupported invoice notification type %q", payload.NotificationType)
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

	header, err := purserdb.New(d.jobs.db).GetInvoiceEmailHeader(ctx, purserdb.GetInvoiceEmailHeaderParams{
		InvoiceID: payload.InvoiceID,
		TenantID:  payload.TenantID,
	})
	if err != nil {
		return []string{"smtp"}, fmt.Errorf("load permanent invoice for email: %w", err)
	}
	if header.Status == "draft" || header.Status == "manual_review" {
		return []string{"smtp"}, fmt.Errorf("invoice %s is not permanent: status %s", payload.InvoiceID, header.Status)
	}
	lineItems, err := d.jobs.loadEmailLineItems(ctx, payload.InvoiceID, payload.TenantID)
	if err != nil {
		return []string{"smtp"}, err
	}
	if len(lineItems) == 0 {
		return []string{"smtp"}, fmt.Errorf("invoice %s has no permanent line-item snapshot", payload.InvoiceID)
	}
	if err := send(payload.Recipient, payload.InvoiceID, header.Amount, header.MeteredAmount,
		header.GrossMeteredAmount, header.Currency, header.DueDate, lineItems); err != nil {
		return []string{"smtp"}, err
	}
	return nil, nil
}

func (d *invoiceEmailDispatcher) dispatchOverdueReminder(ctx context.Context, payload invoiceEmailPayload) ([]string, error) {
	reminder, err := purserdb.New(d.jobs.db).GetOverdueInvoiceReminder(ctx, purserdb.GetOverdueInvoiceReminderParams{
		InvoiceID: payload.InvoiceID,
		TenantID:  payload.TenantID,
	})
	if err != nil {
		return []string{"smtp"}, fmt.Errorf("load overdue invoice for reminder: %w", err)
	}
	if reminder.AmountDue <= 0 || (reminder.Status != "pending" && reminder.Status != "overdue") || !reminder.DueDate.Before(time.Now()) {
		return nil, nil
	}
	daysPastDue := int(time.Since(reminder.DueDate).Hours() / 24)
	if daysPastDue < payload.ReminderStage || payload.ReminderStage != int(reminder.LatestReminderStage) {
		return nil, nil
	}
	send := d.sendReminder
	if send == nil {
		if d.jobs.emailService == nil || !d.jobs.emailService.IsConfigured() {
			return []string{"smtp"}, errors.New("invoice email SMTP is not configured")
		}
		send = func(recipient, invoiceID string, amount float64, currency string, daysPastDue int) error {
			return d.jobs.emailService.SendOverdueReminderEmail(recipient, "", invoiceID, amount, currency, daysPastDue)
		}
	}
	if err := send(payload.Recipient, payload.InvoiceID, reminder.AmountDue, reminder.Currency, daysPastDue); err != nil {
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
