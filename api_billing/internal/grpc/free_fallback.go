package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

var errFreeFallbackHeld = errors.New("free fallback held until the subscription's pricing state clears")

func isFreeFallbackHeld(err error) bool {
	return errors.Is(err, errFreeFallbackHeld) || errors.Is(err, subscriptions.ErrPendingCheckout) ||
		errors.Is(err, subscriptions.ErrPendingTierUpgrade)
}

// runFreeFallbackSweep reconciles due billing moves until shutdown.
func (s *PurserServer) runFreeFallbackSweep(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.sweepFreeFallbackMovesOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Notices have their own worker so slow provider reconciliation cannot starve email.
func (s *PurserServer) runFreeFallbackNotices(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.sweepFreeFallbackNoticesOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *PurserServer) sweepFreeFallbackMovesOnce(ctx context.Context) {
	defer s.observeFreeFallbacks(ctx)
	queries := purserdb.New(s.db)
	due, err := queries.ListDueFreeFallbacks(ctx, time.Now())
	if err != nil {
		s.freeFallbackReadFailed()
		s.logger.WithError(err).Warn("Could not list due moves to Free; retrying on the next sweep")
	}
	for _, tenantID := range due {
		if moveErr := s.moveTenantToFree(ctx, tenantID); moveErr != nil {
			if isFreeFallbackHeld(moveErr) {
				s.logger.WithError(moveErr).WithField("tenant_id", tenantID).Debug("Move to Free held by pending pricing")
			} else {
				s.logger.WithError(moveErr).WithField("tenant_id", tenantID).Warn("Move to Free not completed; retrying on the next sweep")
			}
		}
	}
}

func (s *PurserServer) sweepFreeFallbackNoticesOnce(ctx context.Context) {
	queries := purserdb.New(s.db)
	notices, err := queries.ListFreeFallbackNotices(ctx)
	if err != nil {
		s.freeFallbackReadFailed()
		s.logger.WithError(err).Warn("Could not list Free fallback notices; retrying on the next sweep")
	}
	for _, tenantID := range notices {
		if noticeErr := s.deliverFreeFallbackNotice(ctx, tenantID); noticeErr != nil {
			s.logger.WithError(noticeErr).WithField("tenant_id", tenantID).Warn("Free fallback notice delivery failed; inspect the billing recovery inventory")
		}
	}
}

// moveTenantToFree stops provider renewal before committing the final invoice
// and default postpaid tier. Concurrent usage or pricing changes retry rating.
func (s *PurserServer) moveTenantToFree(ctx context.Context, tenantID string) (resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	queries := purserdb.New(s.db)
	claimedAt, claimErr := queries.ClaimFreeFallbackMove(ctx, tenantID)
	if errors.Is(claimErr, sql.ErrNoRows) {
		return nil
	}
	if claimErr != nil {
		return claimErr
	}
	defer func() {
		if resultErr == nil {
			return
		}
		recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer recordCancel()
		if isFreeFallbackHeld(resultErr) {
			if err := queries.AcknowledgeFreeFallbackHold(recordCtx, purserdb.AcknowledgeFreeFallbackHoldParams{TenantID: tenantID, AttemptedAt: claimedAt}); err != nil {
				s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Could not acknowledge Free fallback hold")
			}
			return
		}
		if err := queries.RecordFreeFallbackMoveFailure(recordCtx, purserdb.RecordFreeFallbackMoveFailureParams{TenantID: tenantID, AttemptedAt: claimedAt, Failure: resultErr.Error()}); err != nil {
			s.logger.WithError(err).WithField("tenant_id", tenantID).Error("Could not record Free fallback failure")
		}
	}()
	scheduled, err := queries.GetSubscriptionFreeFallback(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the final invoice cutoff: %w", err)
	}
	target, err := queries.GetFreeFallbackTier(ctx)
	if err != nil {
		return fmt.Errorf("resolve active default postpaid fallback before stopping collection: %w", err)
	}
	staged, err := s.stageFreeFallbackProviderStop(ctx, tenantID, claimedAt, scheduled)
	if err != nil || !staged {
		return err
	}
	ended, err := s.endFreeFallbackProviders(ctx, tenantID, scheduled)
	if err != nil {
		return fmt.Errorf("end provider collection before moving to Free: %w", err)
	}
	if ended.Pending || ended.Recovered || ended.PaidUntil.After(scheduled.Cutoff) {
		return s.reconcileFreeFallbackProviderOutcome(ctx, tenantID, scheduled, ended)
	}
	closing, finalPeriod, err := handlers.PrepareFreeFallbackBoundary(ctx, s.db, s.logger, s.billing, tenantID, scheduled.Cutoff)
	if err != nil {
		return fmt.Errorf("prepare the final invoice: %w", err)
	}
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		locked, lockErr := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
		if lockErr != nil {
			return lockErr
		}
		if locked.HasGrant || locked.AccountClosed || locked.Current.PendingReason.String == "tier_upgrade" {
			return errFreeFallbackHeld
		}
		queries := purserdb.New(tx)
		current, readErr := queries.GetSubscriptionFreeFallback(ctx, tenantID)
		if errors.Is(readErr, sql.ErrNoRows) {
			return errFreeFallbackHeld
		}
		if readErr != nil {
			return readErr
		}
		if !locked.Current.FreeFallbackProviderStopping || !current.Cutoff.Equal(scheduled.Cutoff) || !current.ScheduledAt.Equal(scheduled.ScheduledAt) {
			return handlers.ErrPostpaidPhaseChanged
		}
		if closing != nil {
			if _, commitErr := closing.CommitTx(ctx, tx); commitErr != nil {
				return commitErr
			}
		}
		if !finalPeriod {
			rows, periodErr := queries.CompleteFreeFallbackPeriod(ctx, purserdb.CompleteFreeFallbackPeriodParams{TenantID: tenantID, AttemptedAt: claimedAt, ScheduledAt: scheduled.ScheduledAt})
			if periodErr != nil {
				return periodErr
			}
			if rows != 1 {
				return handlers.ErrPostpaidPhaseChanged
			}
			return nil
		}
		tier, tierErr := queries.GetFreeFallbackTier(ctx)
		if tierErr != nil {
			return fmt.Errorf("load the Free tier: %w", tierErr)
		}
		if tier != target {
			return handlers.ErrPostpaidPhaseChanged
		}
		moved, moveErr := queries.MoveSubscriptionToFreeFallback(ctx, purserdb.MoveSubscriptionToFreeFallbackParams{TierID: tier, TenantID: tenantID})
		if moveErr != nil {
			return moveErr
		}
		if moved == 0 {
			return errFreeFallbackHeld
		}
		// Free has no later collectable invoice for a sub-minimum final amount.
		if writeoffErr := handlers.WriteOffCollectionCarryTx(ctx, queries, tenantID); writeoffErr != nil {
			return writeoffErr
		}
		updated, buildErr := billingevents.New(tenantID, locked.Current.SubscriptionID, &internalv1.SubscriptionUpdated{
			SubscriptionId: locked.Current.SubscriptionID, TierId: tier, Status: "active",
			ChangedFields: []string{"tier_id", "status"}, Reason: "subscription ended",
		}, s.domainActor(ctx))
		if buildErr != nil {
			return buildErr
		}
		_, enqueueErr := s.EnqueueBillingEventTx(ctx, tx, eventSubscriptionUpdated, tenantID, "", "subscription", locked.Current.SubscriptionID, &ipcpb.BillingEvent{
			SubscriptionId: locked.Current.SubscriptionID, Status: "active",
		}, updated)
		return enqueueErr
	})
	if err != nil {
		return err
	}
	closing.AfterCommit(ctx)
	if !finalPeriod {
		return nil
	}
	if _, _, clusterErr := s.reconcileCanonicalTierClusterAccess(ctx, tenantID); clusterErr != nil {
		s.logger.WithError(clusterErr).WithField("tenant_id", tenantID).Error("Reconcile cluster access after the move to Free")
	}
	if cacheErr := s.invalidateTenantCache(ctx, tenantID, "tier_changed"); cacheErr != nil {
		s.logger.WithError(cacheErr).WithField("tenant_id", tenantID).Warn("Moved to Free; cache convergence remains queued")
	}
	s.logger.WithFields(logging.Fields{"tenant_id": tenantID, "invoice_id": closing.InvoiceID()}).Info("Moved tenant to Free")
	return nil
}

// deliverFreeFallbackNotice sends outside a transaction after claiming the notice.
// Failed sends back off; settlement is fenced against a newer fallback or move.
func (s *PurserServer) deliverFreeFallbackNotice(ctx context.Context, tenantID string) error {
	queries := purserdb.New(s.db)
	row, err := queries.ClaimFreeFallbackNotice(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if row.BillingEmail != "" {
		if sendErr := s.sendFreeFallbackNotice(handlers.FreeFallbackNotice{
			Recipient: row.BillingEmail, TenantName: row.BillingName, Reason: row.FreeFallbackReason,
			MoveAt: row.FreeFallbackAt, Moved: row.Moved,
		}); sendErr != nil {
			if recordErr := queries.RecordFreeFallbackNoticeFailure(ctx, purserdb.RecordFreeFallbackNoticeFailureParams{TenantID: tenantID, AttemptedAt: row.AttemptedAt, ScheduledAt: row.FreeFallbackAt, Failure: sendErr.Error()}); recordErr != nil {
				return errors.Join(sendErr, recordErr)
			}
			return sendErr
		}
	}
	return queries.CompleteFreeFallbackNotice(ctx, purserdb.CompleteFreeFallbackNoticeParams{TenantID: tenantID, AttemptedAt: row.AttemptedAt, ScheduledAt: row.FreeFallbackAt, Moved: row.Moved})
}

func (s *PurserServer) sendFreeFallbackNotice(notice handlers.FreeFallbackNotice) error {
	if s.freeFallbackNoticeSender != nil {
		return s.freeFallbackNoticeSender(notice)
	}
	return s.billing.SendFreeFallbackNotice(notice)
}

// observeFreeFallbacks retains the last good gauge through database outages.
func (s *PurserServer) observeFreeFallbacks(ctx context.Context) {
	if s.metrics == nil || s.metrics.FreeFallbackFailures == nil {
		return
	}
	row, err := purserdb.New(s.db).CountRepeatedFreeFallbackFailures(ctx)
	if err != nil {
		s.freeFallbackReadFailed()
		s.logger.WithError(err).Warn("Could not observe Free fallback recovery")
		return
	}
	s.metrics.FreeFallbackFailures.WithLabelValues("move").Set(float64(row.Moves))
	s.metrics.FreeFallbackFailures.WithLabelValues("notice").Set(float64(row.Notices))
}

func (s *PurserServer) freeFallbackReadFailed() {
	if s.metrics != nil && s.metrics.FreeFallbackReadFailures != nil {
		s.metrics.FreeFallbackReadFailures.WithLabelValues().Inc()
	}
}

// stageFreeFallbackProviderStop commits the fence before making a provider call.
// Checkouts, grants and other pricing writers take this same subscription lock.
func (s *PurserServer) stageFreeFallbackProviderStop(ctx context.Context, tenantID string, claimedAt time.Time, scheduled purserdb.GetSubscriptionFreeFallbackRow) (bool, error) {
	staged := false
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		staged = false
		locked, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
		if err != nil {
			return err
		}
		if locked.HasGrant || locked.AccountClosed || locked.Current.PendingReason.String == "tier_upgrade" {
			return errFreeFallbackHeld
		}
		q := purserdb.New(tx)
		if locked.Current.BillingModel != "postpaid" {
			_, clearErr := q.ClearSubscriptionFreeFallback(ctx, tenantID)
			return clearErr
		}
		if locked.Current.TierID != scheduled.TierID || locked.Current.StripeSubscriptionID.String != scheduled.StripeSubscriptionID || locked.Current.MollieSubscriptionID.String != scheduled.MollieSubscriptionID {
			return handlers.ErrPostpaidPhaseChanged
		}
		if !locked.Current.FreeFallbackProviderStopping {
			if err = subscriptions.CheckGrantCheckout(ctx, tx, tenantID); err != nil {
				return err
			}
		}
		rows, err := q.StartFreeFallbackProviderStop(ctx, purserdb.StartFreeFallbackProviderStopParams{TenantID: tenantID, ScheduledAt: scheduled.ScheduledAt, AttemptedAt: claimedAt})
		if err != nil {
			return err
		}
		if rows != 1 {
			return errFreeFallbackHeld
		}
		staged = true
		return nil
	})
	return staged, err
}

type fallbackProviderEnder interface {
	EndSubscriptionForFallback(context.Context, billing.SubscriptionEndRequest) (billing.SubscriptionEndResult, error)
}

func (s *PurserServer) endFreeFallbackProviders(ctx context.Context, tenantID string, scheduled purserdb.GetSubscriptionFreeFallbackRow) (billing.SubscriptionEndResult, error) {
	result := billing.SubscriptionEndResult{PaidUntil: scheduled.PaidAdvanceUntil, Pending: scheduled.AdvancePaymentPending}
	providers := []struct {
		client                       any
		kind, customer, subscription string
	}{
		{s.stripeClient, "stripe", scheduled.StripeCustomerID, scheduled.StripeSubscriptionID},
		{s.mollieClient, "mollie", scheduled.MollieCustomerID, scheduled.MollieSubscriptionID},
	}
	for _, provider := range providers {
		if provider.subscription == "" {
			continue
		}
		client, ok := provider.client.(fallbackProviderEnder)
		if !ok || client == nil {
			return result, errors.New("provider client does not support safe subscription shutdown")
		}
		ended, err := client.EndSubscriptionForFallback(ctx, billing.SubscriptionEndRequest{AllowRecovery: scheduled.PaymentMethod == provider.kind && scheduled.FreeFallbackReason.String == subscriptions.FallbackPaymentFailed && !scheduled.FreeFallbackProviderEndedAt.Valid, InitialPaymentID: scheduled.MollieInitialPaymentID, TenantID: tenantID, CustomerID: provider.customer, SubscriptionID: provider.subscription, TierID: scheduled.TierID, PeriodEnd: scheduled.PeriodEnd})
		if err != nil {
			return result, err
		}
		result.Pending = result.Pending || ended.Pending
		result.Recovered = result.Recovered || ended.Recovered
		if ended.PaidUntil.After(result.PaidUntil) {
			result.PaidUntil = ended.PaidUntil
		}
	}
	return result, nil
}

func (s *PurserServer) reconcileFreeFallbackProviderOutcome(ctx context.Context, tenantID string, scheduled purserdb.GetSubscriptionFreeFallbackRow, ended billing.SubscriptionEndResult) error {
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		locked, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
		if err != nil {
			return err
		}
		if locked.AccountClosed || locked.HasGrant || !locked.Current.FreeFallbackProviderStopping {
			return errFreeFallbackHeld
		}
		q := purserdb.New(tx)
		if ended.Pending {
			return q.WaitFreeFallbackSettlement(ctx, purserdb.WaitFreeFallbackSettlementParams{TenantID: tenantID, ScheduledAt: scheduled.ScheduledAt})
		}
		var rows int64
		if ended.Recovered {
			rows, err = q.RecoverFreeFallbackProvider(ctx, purserdb.RecoverFreeFallbackProviderParams{TenantID: tenantID, ScheduledAt: scheduled.ScheduledAt})
		} else {
			rows, err = q.RetainPaidFreeFallbackPeriod(ctx, purserdb.RetainPaidFreeFallbackPeriodParams{TenantID: tenantID, ScheduledAt: scheduled.ScheduledAt, PaidUntil: ended.PaidUntil})
		}
		if err != nil {
			return err
		}
		if rows != 1 {
			return errFreeFallbackHeld
		}
		state := "pending_cancellation"
		if ended.Recovered {
			state = "active"
		}
		updated, buildErr := billingevents.New(tenantID, locked.Current.SubscriptionID, &internalv1.SubscriptionUpdated{SubscriptionId: locked.Current.SubscriptionID, TierId: locked.Current.TierID, Status: state, ChangedFields: []string{"status"}, Reason: "provider collection reconciled"}, s.domainActor(ctx))
		if buildErr != nil {
			return buildErr
		}
		_, err = s.EnqueueBillingEventTx(ctx, tx, eventSubscriptionUpdated, tenantID, "", "subscription", locked.Current.SubscriptionID, &ipcpb.BillingEvent{SubscriptionId: locked.Current.SubscriptionID, Status: state}, updated)
		return err
	})
	if err == nil && !ended.Pending {
		if cacheErr := s.invalidateTenantCache(ctx, tenantID, "subscription_updated"); cacheErr != nil {
			s.logger.WithError(cacheErr).WithField("tenant_id", tenantID).Warn("Reconciled provider collection; cache convergence remains queued")
		}
	}
	return err
}
