package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

type providerWebhookPayload struct {
	Provider string
	Headers  map[string]string
	Body     []byte
}

type providerWebhookInboxStore struct{ db *sql.DB }

type providerWebhookDispatcher struct{ service *Service }

func (s *Service) enqueueProviderWebhook(ctx context.Context, provider, eventKey string, headers map[string]string, body []byte) (bool, string, int) {
	if s.db == nil {
		return false, "Webhook inbox is not configured", 503
	}
	headerJSON, err := json.Marshal(providerWebhookHeaders(headers))
	if err != nil {
		return false, "Invalid webhook headers", 400
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO purser.provider_webhook_inbox
			(provider, event_key, headers, raw_payload)
		VALUES ($1, $2, $3::jsonb, $4)
		ON CONFLICT (provider, event_key) DO NOTHING
	`, provider, eventKey, headerJSON, body); err != nil {
		s.logger.WithError(err).WithField("provider", provider).Error("Failed to persist provider webhook")
		return false, "Failed to persist webhook", 503
	}
	return true, "", 200
}

func providerWebhookHeaders(headers map[string]string) map[string]string {
	kept := make(map[string]string, 2)
	if signature := headerValue(headers, "Stripe-Signature"); signature != "" {
		kept["Stripe-Signature"] = signature
	}
	if contentType := headerValue(headers, "Content-Type"); contentType != "" {
		kept["Content-Type"] = contentType
	}
	return kept
}

func webhookInboxClaimID(id string) string { return id }

func (s *providerWebhookInboxStore) ClaimBatch(ctx context.Context, batchSize int, lease time.Duration) (claims []outbox.Claim[providerWebhookPayload], err error) {
	if s.db == nil {
		return nil, errors.New("claim provider webhooks: nil database")
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
		SELECT id, provider, headers, raw_payload, attempts
		FROM purser.provider_webhook_inbox
		WHERE next_attempt_at <= NOW()
		  AND (status IN ('pending', 'failed')
		       OR (status = 'processing' AND claimed_at < NOW() - ($1 * interval '1 millisecond')))
		ORDER BY next_attempt_at, created_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`, lease.Milliseconds(), batchSize)
	if err != nil {
		return nil, fmt.Errorf("select provider webhook inbox: %w", err)
	}
	defer rows.Close()

	type candidate struct {
		id, provider string
		headers      []byte
		body         []byte
		attempts     int
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.provider, &item.headers, &item.body, &item.attempts); err != nil {
			return nil, fmt.Errorf("scan provider webhook inbox: %w", err)
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	claims = make([]outbox.Claim[providerWebhookPayload], 0, len(candidates))
	for _, item := range candidates {
		leaseToken := uuid.NewString()
		if _, err := tx.ExecContext(ctx, `
			UPDATE purser.provider_webhook_inbox
			SET status = 'processing', claimed_at = NOW(), lease_token = $2, updated_at = NOW()
			WHERE id = $1
		`, item.id, leaseToken); err != nil {
			return nil, err
		}
		var headers map[string]string
		if err := json.Unmarshal(item.headers, &headers); err != nil {
			return nil, fmt.Errorf("decode provider webhook headers: %w", err)
		}
		claims = append(claims, outbox.Claim[providerWebhookPayload]{
			ID: webhookInboxClaimID(item.id), Attempts: item.attempts, LeaseToken: leaseToken,
			Payload: providerWebhookPayload{Provider: item.provider, Headers: headers, Body: item.body},
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claims, nil
}

func (s *providerWebhookInboxStore) MarkCompleted(ctx context.Context, id string) error {
	return s.complete(ctx, id, "")
}

func (s *providerWebhookInboxStore) MarkCompletedToken(ctx context.Context, id, leaseToken string) error {
	return s.complete(ctx, id, leaseToken)
}

func (s *providerWebhookInboxStore) complete(ctx context.Context, id, leaseToken string) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE purser.provider_webhook_inbox
		SET status = 'processed', processed_at = NOW(), claimed_at = NULL,
		    lease_token = NULL, last_error = NULL, updated_at = NOW()
		WHERE id = $1 AND ($2 = '' OR lease_token::text = $2)
	`, id, leaseToken)
	return requireWebhookInboxUpdate(result, err)
}

func (s *providerWebhookInboxStore) RecordFailure(ctx context.Context, id string, _ int, _ []string, cause error, backoff time.Duration) error {
	return s.fail(ctx, id, cause, backoff, "")
}

func (s *providerWebhookInboxStore) RecordFailureToken(ctx context.Context, id string, _ int, _ []string, cause error, backoff time.Duration, leaseToken string) error {
	return s.fail(ctx, id, cause, backoff, leaseToken)
}

func (s *providerWebhookInboxStore) fail(ctx context.Context, id string, cause error, backoff time.Duration, leaseToken string) error {
	message := "provider webhook reconciliation failed"
	if cause != nil {
		message = cause.Error()
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE purser.provider_webhook_inbox
		SET status = 'failed', attempts = attempts + 1,
		    next_attempt_at = NOW() + ($2 * interval '1 millisecond'),
		    claimed_at = NULL, lease_token = NULL, last_error = $3, updated_at = NOW()
		WHERE id = $1 AND ($4 = '' OR lease_token::text = $4)
	`, id, backoff.Milliseconds(), message, leaseToken)
	return requireWebhookInboxUpdate(result, err)
}

func requireWebhookInboxUpdate(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("provider webhook lease no longer owned")
	}
	return nil
}

func (d *providerWebhookDispatcher) Dispatch(ctx context.Context, payload providerWebhookPayload) ([]string, error) {
	if d.service == nil {
		return []string{"reconciliation"}, errors.New("provider webhook service is not configured")
	}
	var ok bool
	var message string
	switch payload.Provider {
	case "stripe":
		var event StripeWebhookPayload
		if err := json.Unmarshal(payload.Body, &event); err != nil {
			return []string{"reconciliation"}, fmt.Errorf("decode verified Stripe webhook: %w", err)
		}
		ok, message, _ = d.service.processStripeWebhookPayload(ctx, event, headerValue(payload.Headers, "Stripe-Signature"), payload.Body)
	case "mollie":
		paymentID, err := parseMollieWebhookID(payload.Body, headerValue(payload.Headers, "Content-Type"))
		if err != nil {
			return []string{"reconciliation"}, fmt.Errorf("decode accepted Mollie webhook: %w", err)
		}
		if paymentID == "" {
			return []string{"reconciliation"}, errors.New("accepted Mollie webhook has no payment id")
		}
		ok, message, _ = d.service.processMollieWebhookPayload(ctx, paymentID, payload.Body)
	default:
		return []string{"reconciliation"}, fmt.Errorf("unsupported provider %q", payload.Provider)
	}
	if !ok {
		return []string{"reconciliation"}, errors.New(message)
	}
	return nil, nil
}

func (jm *JobManager) runProviderWebhookInbox(ctx context.Context) {
	worker := &outbox.Worker[providerWebhookPayload]{
		Config: outbox.Config{
			BaseBackoff: time.Minute, MaxBackoff: 6 * time.Hour, BatchSize: 50,
			PollPeriod: 5 * time.Second, Lease: 2 * time.Minute,
			SettleTimeout: 30 * time.Second, AlertAfterAttempts: 5,
		},
		Store: &providerWebhookInboxStore{db: jm.db}, Dispatcher: &providerWebhookDispatcher{service: jm.billing},
		Logger: jm.logger, AlertLabel: "provider webhook inbox",
	}
	worker.Run(ctx)
}
