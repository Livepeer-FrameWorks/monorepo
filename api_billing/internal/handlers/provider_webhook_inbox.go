package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	billingmollie "frameworks/api_billing/internal/mollie"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	"github.com/google/uuid"
)

type providerWebhookPayload struct {
	InboxID    uuid.UUID
	LeaseToken string
	Provider   string
	EventKey   string
	ReceivedAt time.Time
	Headers    map[string]string
	Body       []byte
}

type providerWebhookInboxStore struct {
	db      *sql.DB
	metrics *PurserMetrics
}

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
	txErr := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		candidates, err := queries.ClaimProviderWebhooks(ctx, purserdb.ClaimProviderWebhooksParams{
			LeaseMilliseconds: lease.Milliseconds(),
			BatchSize:         int32(batchSize),
		})
		if err != nil {
			return fmt.Errorf("select provider webhook inbox: %w", err)
		}

		claims = make([]outbox.Claim[providerWebhookPayload], 0, len(candidates))
		for _, item := range candidates {
			leaseToken := uuid.NewString()
			if err := queries.MarkProviderWebhookProcessing(ctx, purserdb.MarkProviderWebhookProcessingParams{
				LeaseToken: leaseToken,
				ID:         item.ID,
			}); err != nil {
				return err
			}
			var headers map[string]string
			if err := json.Unmarshal(item.Headers, &headers); err != nil {
				return fmt.Errorf("decode provider webhook headers: %w", err)
			}
			claims = append(claims, outbox.Claim[providerWebhookPayload]{
				ID: webhookInboxClaimID(item.ID.String()), Attempts: int(item.Attempts), LeaseToken: leaseToken,
				Payload: providerWebhookPayload{InboxID: item.ID, LeaseToken: leaseToken, Provider: item.Provider, EventKey: item.EventKey, ReceivedAt: item.CreatedAt, Headers: headers, Body: item.RawPayload},
			})
		}
		return nil
	})
	if txErr != nil {
		return nil, txErr
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
	switch {
	case errors.Is(cause, fx.ErrUnsupportedCurrency):
		message = "provider settlement blocked: unsupported_currency"
	case errors.Is(cause, fx.ErrUnavailable):
		message = "provider settlement waiting: fx_unavailable"
	case errors.Is(cause, errProviderSettlementEvidenceConflict):
		message = "provider settlement blocked: evidence_conflict"
	}
	inboxID, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("fail provider webhook: invalid id %q: %w", id, err)
	}
	result, err := purserdb.New(s.db).FailProviderWebhook(ctx, purserdb.FailProviderWebhookParams{
		BackoffMilliseconds: backoff.Milliseconds(),
		LastError:           message,
		ID:                  inboxID,
		LeaseToken:          leaseToken,
		Waiting:             settlementOutcome(cause) == "waiting" || errors.Is(cause, billingmollie.ErrPaymentSettling),
		Blocked:             settlementOutcome(cause) == "blocked",
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
	if payload.Provider == "refund_settlement" {
		var plan billing.RefundSettlement
		if err := json.Unmarshal(payload.Body, &plan); err != nil {
			return nil, err
		}
		expected := "refund-settlement:" + plan.Provider + ":" + plan.ReferenceKind + ":" + plan.ReferenceID + ":" + plan.PaymentID
		if plan.PaymentID == "" || payload.EventKey != expected {
			return nil, errors.New("invalid internal refund settlement record")
		}
		if err := d.service.settlePurchaseRefund(ctx, plan); err != nil {
			return []string{"refund_settlement"}, err
		}
		return nil, nil
	}
	if payload.Provider == "tenant_suspension" {
		var suspension struct {
			TenantID string `json:"tenant_id"`
		}
		if err := json.Unmarshal(payload.Body, &suspension); err != nil {
			return nil, err
		}
		if suspension.TenantID == "" || payload.EventKey != "tenant-suspension:"+suspension.TenantID {
			return nil, errors.New("invalid internal suspension record")
		}
		if d.service.stopSuspendedTenantStreams == nil {
			return []string{"tenant_suspension"}, errors.New("stream suspension retry is not configured")
		}
		if err := d.service.stopSuspendedTenantStreams(ctx, suspension.TenantID); err != nil {
			return []string{"tenant_suspension"}, err
		}
		return nil, nil
	}
	if payload.Provider == "tier_upgrade" {
		var upgrade struct {
			ID       string `json:"id"`
			TenantID string `json:"tenant_id"`
		}
		if err := json.Unmarshal(payload.Body, &upgrade); err != nil {
			return nil, err
		}
		if upgrade.ID == "" || upgrade.TenantID == "" || payload.EventKey != "tier-upgrade:"+upgrade.ID {
			return nil, errors.New("invalid internal tier upgrade record")
		}
		_, err := d.service.ProcessTierUpgrade(ctx, upgrade.TenantID, upgrade.ID)
		if err == nil || errors.Is(err, billing.ErrTierUpgradeRejected) {
			return nil, nil
		}
		return []string{"tier_upgrade"}, err
	}
	if payload.Provider == "tier_downgrade" {
		var downgrade struct {
			TenantID string `json:"tenant_id"`
		}
		if err := json.Unmarshal(payload.Body, &downgrade); err != nil {
			return nil, err
		}
		if downgrade.TenantID == "" || payload.EventKey != "tier-downgrade:"+downgrade.TenantID {
			return nil, errors.New("invalid internal tier downgrade record")
		}
		if err := d.service.ReconcileProviderTier(ctx, downgrade.TenantID); err != nil {
			return []string{"tier_downgrade"}, err
		}
		return nil, nil
	}
	if payload.Provider == "tenant_closure" {
		var closure struct {
			TenantID string `json:"tenant_id"`
		}
		if err := json.Unmarshal(payload.Body, &closure); err != nil {
			return nil, err
		}
		if closure.TenantID == "" || payload.EventKey != "tenant-closure:"+closure.TenantID {
			return nil, errors.New("invalid tenant billing closure record")
		}
		if err := d.service.closeTenantBilling(ctx, closure.TenantID); err != nil {
			return []string{"tenant_closure"}, err
		}
		return nil, nil
	}
	if payload.Provider == "checkout_delivery_review" {
		var review checkoutReservationExpiry
		if err := json.Unmarshal(payload.Body, &review); err != nil {
			return nil, err
		}
		if review.TenantID == "" || review.IntentID == "" || payload.EventKey != "checkout-delivery-review:"+review.IntentID || review.Force {
			return nil, errors.New("invalid internal historical delivery review record")
		}
		if err := d.service.reviewHistoricalMollieDelivery(ctx, review); err != nil {
			return []string{"checkout_delivery_review"}, err
		}
		return nil, nil
	}
	var expiry checkoutReservationExpiry
	if payload.Provider == "checkout_recovery" {
		if err := json.Unmarshal(payload.Body, &expiry); err != nil {
			return nil, err
		}
		if expiry.IntentID == "" || payload.EventKey != "checkout-expiry:"+expiry.IntentID {
			return nil, errors.New("invalid internal checkout recovery record")
		}
		if recoveryErr := d.service.expireCheckoutReservation(ctx, expiry); recoveryErr != nil {
			return []string{"checkout_recovery"}, recoveryErr
		}
		return nil, d.service.finishCheckoutRecoveryAudits(ctx, expiry.TenantID, expiry.IntentID)
	}

	ctx = context.WithValue(ctx, providerReceiptTimeKey{}, payload.ReceivedAt)
	ctx = context.WithValue(ctx, providerInboxOwnerKey{}, payload)
	var ok bool
	var message string
	var processingErr error
	switch payload.Provider {
	case "stripe":
		var event StripeWebhookPayload
		if err := json.Unmarshal(payload.Body, &event); err != nil {
			return []string{"reconciliation"}, fmt.Errorf("decode verified Stripe webhook: %w", err)
		}
		ok, message, _, processingErr = d.service.processStripeWebhookDetailed(ctx, event, headerValue(payload.Headers, "Stripe-Signature"), payload.Body)
	case "mollie":
		paymentID, err := parseMollieWebhookID(payload.Body, headerValue(payload.Headers, "Content-Type"))
		if err != nil {
			return []string{"reconciliation"}, fmt.Errorf("decode accepted Mollie webhook: %w", err)
		}
		if paymentID == "" {
			return []string{"reconciliation"}, errors.New("accepted Mollie webhook has no payment id")
		}
		ok, message, _, processingErr = d.service.processMollieWebhookDetailed(ctx, paymentID, payload.Body)
	default:
		return []string{"reconciliation"}, fmt.Errorf("unsupported provider %q", payload.Provider)
	}
	if !ok {
		if processingErr != nil {
			return []string{"reconciliation"}, processingErr
		}
		return []string{"reconciliation"}, errors.New(message)
	}
	return nil, nil
}

type providerInboxOwnerKey struct{}

// Only a recorded provider payment binds a callback; request headers and charge
// IDs cannot establish tenant ownership of a PaymentIntent settlement.
func (s *Service) bindSettlementWebhookOwner(ctx context.Context, provider, paymentID string) error {
	payload, ok := ctx.Value(providerInboxOwnerKey{}).(providerWebhookPayload)
	if !ok || payload.InboxID == uuid.Nil {
		return nil
	}
	row, err := purserdb.New(s.db).GetProviderSettlement(ctx, purserdb.GetProviderSettlementParams{Provider: provider, ProviderPaymentID: paymentID})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	result, err := purserdb.New(s.db).BindProviderWebhookPaymentOwner(ctx, purserdb.BindProviderWebhookPaymentOwnerParams{
		ID: payload.InboxID, LeaseToken: payload.LeaseToken, TenantID: row.TenantID, Provider: provider, ProviderPaymentID: paymentID,
	})
	return requireWebhookInboxUpdate(result, err)
}

func (jm *JobManager) runProviderWebhookInbox(ctx context.Context) {
	var metrics *PurserMetrics
	if jm.billing != nil {
		metrics = jm.billing.metrics
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	store := &providerWebhookInboxStore{db: jm.db, metrics: metrics}
	observed := make(chan struct{})
	go func() { defer close(observed); store.observePeriodically(runCtx) }()
	worker := &outbox.Worker[providerWebhookPayload]{
		Config: providerWebhookWorkerConfig(),
		Store:  store, Dispatcher: &providerWebhookDispatcher{service: jm.billing},
		Logger: jm.logger, AlertLabel: "provider webhook inbox",
	}
	worker.Run(runCtx)
	cancel()
	<-observed
}

type providerReceiptTimeKey struct{}

// providerReceiptTime fixes the metering boundary at the first durable webhook receipt.
// A held metering anomaly keeps this boundary pending under PurserProviderWebhookInboxStuck.
func providerReceiptTime(ctx context.Context) time.Time {
	if at, ok := ctx.Value(providerReceiptTimeKey{}).(time.Time); ok && !at.IsZero() {
		return at
	}
	return time.Now()
}

// observeFailures retains the last good count if the inventory cannot be read.
func (s *providerWebhookInboxStore) observeFailures(ctx context.Context) {
	if s.metrics == nil || s.metrics.ProviderWebhookInboxFailures == nil {
		return
	}
	observeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	count, err := purserdb.New(s.db).ProviderWebhookInboxRepeatedFailures(observeCtx)
	if err != nil {
		if s.metrics.ProviderWebhookInboxReadFailures != nil {
			s.metrics.ProviderWebhookInboxReadFailures.WithLabelValues().Inc()
		}
		return
	}
	s.metrics.ProviderWebhookInboxFailures.WithLabelValues().Set(float64(count))
}

// observeHeldDowngrades retains the last good count if the inventory cannot be read.
func (s *providerWebhookInboxStore) observeHeldDowngrades(ctx context.Context) {
	if s.metrics == nil || s.metrics.TierDowngradesHeld == nil {
		return
	}
	observeCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	count, err := purserdb.New(s.db).CountHeldConfirmedDowngrades(observeCtx, int64(HeldDowngradeGrace/time.Second))
	if err != nil {
		// Its own counter: a failed held-downgrade read is not a stuck
		// provider webhook inbox.
		if s.metrics.TierDowngradesHeldReadFailures != nil {
			s.metrics.TierDowngradesHeldReadFailures.WithLabelValues().Inc()
		}
		return
	}
	s.metrics.TierDowngradesHeld.WithLabelValues().Set(float64(count))
}

// observePeriodically keeps inventory reads off the callback claim/activation path.
func (s *providerWebhookInboxStore) observePeriodically(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		discoverCtx, discoverCancel := context.WithTimeout(ctx, time.Second)
		discoverErr := purserdb.New(s.db).EnqueueUnresolvedCheckoutRecovery(discoverCtx)
		discoverCancel()
		downgradeCtx, downgradeCancel := context.WithTimeout(ctx, time.Second)
		downgradeErr := purserdb.New(s.db).EnqueueUnconfirmedTierDowngrades(downgradeCtx, int64(FreeDowngradeStopLead/time.Second))
		downgradeCancel()
		if discoverErr != nil || downgradeErr != nil {
			if s.metrics != nil && s.metrics.ProviderWebhookInboxReadFailures != nil {
				s.metrics.ProviderWebhookInboxReadFailures.WithLabelValues().Inc()
			}
		}
		s.observeFailures(ctx)
		s.observeHeldDowngrades(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DefersFailedClaims confirms that next_attempt_at excludes failed claims from the current drain.
func (*providerWebhookInboxStore) DefersFailedClaims() bool { return true }

func providerWebhookWorkerConfig() outbox.Config {
	return outbox.Config{
		// Dispatch is sequential: claim one bounded call so settlement fits the lease.
		BaseBackoff: time.Minute, MaxBackoff: 6 * time.Hour, BatchSize: 1,
		PollPeriod: 5 * time.Second, Lease: 2 * time.Minute,
		SettleTimeout: 30 * time.Second,
	}
}
