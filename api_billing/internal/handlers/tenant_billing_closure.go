package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/google/uuid"
)

type closedTenantProviderSubscription struct {
	Provider       string `json:"provider"`
	CustomerID     string `json:"customer_id"`
	SubscriptionID string `json:"subscription_id"`
}

// queueTenantBillingClosure acknowledges account closure only after its billing
// cutoff and recovery work are durable. External cancellation runs in the inbox.
func (jm *JobManager) queueTenantBillingClosure(ctx context.Context, msg kafka.Message) error {
	if msg.Headers[events.HeaderType] != "user.tenant_account_closed" {
		return nil
	}
	headers := make([]events.Header, 0, len(msg.Headers))
	for key, value := range msg.Headers {
		headers = append(headers, events.Header{Key: key, Value: []byte(value)})
	}
	record, parseErr := events.ParseRecord(msg.Key, headers, msg.Value)
	if parseErr != nil {
		return fmt.Errorf("decode tenant account closure: %w", parseErr)
	}
	if record.Source != "commodore" || record.AggregateID != record.TenantID || record.Time.IsZero() {
		return errors.New("tenant account closure has invalid source or tenant scope")
	}
	if _, err := uuid.Parse(record.TenantID); err != nil {
		return fmt.Errorf("tenant account closure scope: %w", err)
	}
	for delay := time.Second; ; delay = min(30*time.Second, delay*2) {
		queueErr := database.WithRetryablePostgresTx(ctx, jm.db, nil, func(tx *sql.Tx) error {
			q := purserdb.New(tx)
			if err := q.LockSubscriptionForPeriodClose(ctx, record.TenantID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err := q.RecordTenantBillingClosure(ctx, purserdb.RecordTenantBillingClosureParams{TenantID: record.TenantID, ClosedAt: record.Time, EventID: record.ID}); err != nil {
				return err
			}
			if err := q.HaltClosedTenantBilling(ctx, record.TenantID); err != nil {
				return err
			}
			if err := q.EndClosedTenantGrantAtCutoff(ctx, record.TenantID); err != nil {
				return err
			}
			ids, readErr := q.CompensateClosedTenantCheckouts(ctx, record.TenantID)
			if readErr != nil {
				return readErr
			}
			for _, id := range ids {
				if err := q.RequestCheckoutRecovery(ctx, purserdb.RequestCheckoutRecoveryParams{TenantID: record.TenantID, IntentID: id, Reason: "tenant account closed"}); err != nil {
					return err
				}
			}
			return q.EnqueueTenantBillingClosure(ctx, record.TenantID)
		})
		if queueErr == nil {
			return nil
		}
		jm.logger.WithError(queueErr).WithField("tenant_id", record.TenantID).Warn("Tenant billing closure not queued; retrying before committing the event")
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// closeTenantBilling stops recurring provider subscriptions before finalizing
// the last local phase. Both steps resume from durable state after a crash.
func (s *Service) closeTenantBilling(ctx context.Context, tenantID string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	q := purserdb.New(s.db)
	closure, loadErr := q.GetTenantBillingClosure(ctx, tenantID)
	if loadErr != nil {
		return loadErr
	}
	if closure.FinalizedAt.Valid && closure.ProvidersCancelledAt.Valid {
		return s.collectClosedTenantInvoices(ctx, tenantID)
	}
	if !closure.ProvidersCancelledAt.Valid {
		var refs []closedTenantProviderSubscription
		if err := json.Unmarshal(closure.ProviderSubscriptions, &refs); err != nil {
			return err
		}
		for _, ref := range refs {
			if err := s.cancelClosedTenantProvider(ctx, ref); err != nil {
				return fmt.Errorf("cancel closed tenant %s subscription %s: %w", ref.Provider, ref.SubscriptionID, err)
			}
		}
		rows, err := q.MarkClosedTenantProvidersCancelled(ctx, purserdb.MarkClosedTenantProvidersCancelledParams{TenantID: tenantID, ExpectedReferences: closure.ProviderSubscriptions})
		if err != nil {
			return err
		}
		if rows == 0 {
			return errors.New("tenant closure received another provider subscription during cancellation")
		}
	}
	if closure.FinalizedAt.Valid {
		return s.collectClosedTenantInvoices(ctx, tenantID)
	}
	upgrades, upgradeErr := q.ListUnfinishedTierUpgrades(ctx, tenantID)
	if upgradeErr != nil {
		return upgradeErr
	}
	for _, id := range upgrades {
		if _, err := s.ProcessTierUpgrade(ctx, tenantID, id); err != nil {
			return fmt.Errorf("settle closed account tier upgrade: %w", err)
		}
		if err := q.EnqueueTierUpgrade(ctx, purserdb.EnqueueTierUpgradeParams{TenantID: tenantID, ID: id}); err != nil {
			return err
		}
	}

	if _, err := q.GetSubscriptionOperatorGrant(ctx, tenantID); err == nil {
		return errors.New("tenant closure is waiting for its billing grant to close at the recorded cutoff; inspect grant show")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	postpaid, postpaidErr := PrepareProviderBillingBoundary(ctx, s.db, s.logger, s, tenantID, closure.ClosedAt)
	if postpaidErr != nil {
		return postpaidErr
	}
	prepaid, prepaidErr := preparePrepaidPhaseClose(ctx, s.db, s.logger, s, tenantID, closure.ClosedAt, false)
	if prepaidErr != nil {
		return prepaidErr
	}
	if prepaid != nil {
		clock, err := q.GetSubscriptionPrepaidPhase(ctx, tenantID)
		if err != nil {
			return err
		}
		tier, err := q.LoadSubscriptionEffectiveTier(ctx, tenantID)
		if err != nil {
			return err
		}
		if tier.MeteringEnabled && clock.BillingPeriodStart.Valid && closure.ClosedAt.After(clock.BillingPeriodStart.Time) {
			jobs := &JobManager{db: s.db, logger: s.logger, billing: s}
			if meteringErr := jobs.assertMeteringComplete(ctx, tenantID, clock.BillingPeriodStart.Time, closure.ClosedAt); meteringErr != nil {
				return meteringErr
			}
		}
	}
	commitErr := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		tq := purserdb.New(tx)
		if err := tq.LockSubscriptionForPeriodClose(ctx, tenantID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		current, readErr := tq.GetTenantBillingClosure(ctx, tenantID)
		if readErr != nil || current.FinalizedAt.Valid {
			return readErr
		}
		if !current.ProvidersCancelledAt.Valid {
			return errors.New("tenant closure has additional provider cancellation work")
		}
		if _, err := tq.GetSubscriptionOperatorGrant(ctx, tenantID); err == nil {
			return errors.New("tenant grant changed while finalizing closure")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := postpaid.CommitTx(ctx, tx); err != nil {
			return err
		}
		if _, err := prepaid.CommitTx(ctx, tx); err != nil {
			return err
		}
		if err := WriteOffCollectionCarryTx(ctx, tq, tenantID, CollectionWriteoffAccountClosed); err != nil {
			return err
		}
		if err := tq.CancelClosedTenantClusterSubscriptions(ctx, purserdb.CancelClosedTenantClusterSubscriptionsParams{TenantID: tenantID, ClosedAt: closure.ClosedAt}); err != nil {
			return err
		}
		if err := tq.CancelClosedTenantSubscription(ctx, purserdb.CancelClosedTenantSubscriptionParams{TenantID: tenantID, ClosedAt: closure.ClosedAt}); err != nil {
			return err
		}
		if err := tq.CompleteTenantBillingClosure(ctx, tenantID); err != nil {
			return err
		}
		if state, err := tq.LockTenantSubscriptionForProviderChange(ctx, tenantID); err == nil {
			return billingevents.NewAndEnqueue(ctx, tx, tenantID, state.SubscriptionID, &internalv1.SubscriptionUpdated{SubscriptionId: state.SubscriptionID, TierId: state.TierID, Status: "cancelled", ChangedFields: []string{"status"}}, events.Actor{})
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return nil
	})
	if commitErr == nil {
		postpaid.AfterCommit(ctx)
		return s.collectClosedTenantInvoices(ctx, tenantID)
	}
	return commitErr
}

// Reasons a sub-minimum balance is written off.
const (
	CollectionWriteoffAccountClosed = "account_closed"
	CollectionWriteoffMovedToFree   = "moved_to_free"
)

// WriteOffCollectionCarryTx records the sub-minimum balance no later invoice can
// collect, then clears it. Call in the transaction that commits the final invoice,
// so the amount that invoice deferred is included.
func WriteOffCollectionCarryTx(ctx context.Context, q *purserdb.Queries, tenantID, reason string) error {
	if _, err := q.RecordCollectionWriteoffs(ctx, purserdb.RecordCollectionWriteoffsParams{TenantID: tenantID, Reason: reason}); err != nil {
		return fmt.Errorf("record collection balance writeoff: %w", err)
	}
	if _, err := q.ClearCollectionBalances(ctx, tenantID); err != nil {
		return fmt.Errorf("clear written-off collection balance: %w", err)
	}
	return nil
}

// queueClosedTenantStripeSubscription records a late provider identity under the
// subscription lock. Its worker cancels it without reopening final billing.
func queueClosedTenantStripeSubscription(ctx context.Context, tx *sql.Tx, tenantID, customerID, subscriptionID string) error {
	if subscriptionID == "" {
		return nil
	}
	q := purserdb.New(tx)
	rows, err := q.AppendClosedTenantStripeSubscription(ctx, purserdb.AppendClosedTenantStripeSubscriptionParams{TenantID: tenantID, CustomerID: customerID, SubscriptionID: subscriptionID})
	if err != nil || rows == 0 {
		return err
	}
	return q.RequeueTenantBillingClosure(ctx, tenantID)
}

func (s *Service) cancelClosedTenantProvider(ctx context.Context, ref closedTenantProviderSubscription) error {
	switch ref.Provider {
	case "stripe":
		if s.stripeClient == nil {
			return errors.New("stripe cancellation is not configured")
		}
		return s.stripeClient.CancelEstablishedSubscription(ctx, ref.SubscriptionID)
	case "mollie":
		if s.mollieClient == nil || ref.CustomerID == "" {
			return errors.New("mollie cancellation needs its configured client and customer mapping")
		}
		return s.mollieClient.CancelEstablishedSubscription(ctx, ref.CustomerID, ref.SubscriptionID)
	default:
		return fmt.Errorf("unsupported subscription provider %q", ref.Provider)
	}
}

// The closure inbox remains pending until existing debt has a durable payment
// attempt. The payment ledger then owns settlement and provider retries.
func (s *Service) collectClosedTenantInvoices(ctx context.Context, tenantID string) error {
	q := purserdb.New(s.db)
	invoices, err := q.ListClosedTenantUncollectedInvoices(ctx, tenantID)
	if err != nil {
		return err
	}
	jm := &JobManager{db: s.db, logger: s.logger, billing: s}
	for _, invoice := range invoices {
		if err = jm.chargeInvoice(ctx, invoice.Provider, tenantID, invoice.InvoiceID, invoice.PresentmentAmountCents.Int64, invoice.Currency); err != nil {
			return err
		}
	}
	remaining, err := q.ListClosedTenantUncollectedInvoices(ctx, tenantID)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return errors.New("closed account has issued invoices without a collection attempt; restore its saved payment method and retry recovery")
	}
	return nil
}
