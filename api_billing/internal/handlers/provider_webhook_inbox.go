package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"frameworks/api_billing/internal/database/purserdb"
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
	if err := purserdb.New(s.db).EnqueueProviderWebhook(ctx, purserdb.EnqueueProviderWebhookParams{
		Provider: provider, EventKey: eventKey, Headers: headerJSON, RawPayload: body,
	}); err != nil {
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

	queries := purserdb.New(tx)
	candidates, err := queries.ClaimProviderWebhooks(ctx, purserdb.ClaimProviderWebhooksParams{
		LeaseMilliseconds: lease.Milliseconds(),
		BatchSize:         int32(batchSize),
	})
	if err != nil {
		return nil, fmt.Errorf("select provider webhook inbox: %w", err)
	}

	claims = make([]outbox.Claim[providerWebhookPayload], 0, len(candidates))
	for _, item := range candidates {
		leaseToken := uuid.NewString()
		if err := queries.MarkProviderWebhookProcessing(ctx, purserdb.MarkProviderWebhookProcessingParams{
			LeaseToken: leaseToken,
			ID:         item.ID,
		}); err != nil {
			return nil, err
		}
		var headers map[string]string
		if err := json.Unmarshal(item.Headers, &headers); err != nil {
			return nil, fmt.Errorf("decode provider webhook headers: %w", err)
		}
		claims = append(claims, outbox.Claim[providerWebhookPayload]{
			ID: webhookInboxClaimID(item.ID.String()), Attempts: int(item.Attempts), LeaseToken: leaseToken,
			Payload: providerWebhookPayload{Provider: item.Provider, Headers: headers, Body: item.RawPayload},
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
	inboxID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("complete provider webhook: invalid id %q: %w", id, err)
	}
	result, err := purserdb.New(s.db).CompleteProviderWebhook(ctx, purserdb.CompleteProviderWebhookParams{
		ID: inboxID, LeaseToken: leaseToken,
	})
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
	inboxID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("fail provider webhook: invalid id %q: %w", id, err)
	}
	result, err := purserdb.New(s.db).FailProviderWebhook(ctx, purserdb.FailProviderWebhookParams{
		BackoffMilliseconds: backoff.Milliseconds(),
		LastError:           sql.NullString{String: message, Valid: true},
		ID:                  inboxID,
		LeaseToken:          leaseToken,
	})
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
