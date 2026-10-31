package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/subscriptions"
	"frameworks/api_billing/internal/tieraccess"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// errOperatorGrantBusy means another expiry worker already holds the subscription lock.
var errOperatorGrantBusy = errors.New("operator grant is being processed by another replica")

// operatorGrantMutation fences a prospective pricing change and its fallback under the subscription lock.
type operatorGrantMutation struct {
	// mutated records a durable change, excluding an identical set under the lock.
	mutated bool
	// pricingBoundary controls whether the prepared closing document is committed.
	pricingBoundary bool
	// The expected tier and model fence the rating snapshot.
	expectedTierID string
	expectedModel  string
	// keepTier ends a grant without replacing the current subscription tier.
	keepTier bool
	// clearFallback makes an explicit tier decision independent of the original trial.
	clearFallback bool
	// reason is the audit explanation when ending the grant.
	reason string
	// A nil change ends the previous grant; otherwise it replaces its arrangement.
	change *operatorGrantChange
	// previous fences the current grant and preserves its fallback on renewal.
	previous *purserdb.GetSubscriptionOperatorGrantRow
	// fallbackTierName overrides the remembered fallback only when explicitly supplied.
	fallbackTierName *string
	// at is the exact end of a phase, including an expiry processed late.
	at time.Time
	// expiredOnly skips a subscription already claimed by another expiry worker.
	expiredOnly bool
}

// changesPrices compares the effective arrangement, excluding its deadline and audit metadata.
func (g *operatorGrantMutation) changesPrices() bool {
	before, after := operatorGrantChange{collection: grantCollectionProvider}, operatorGrantChange{collection: grantCollectionProvider}
	if g.previous != nil {
		before.basePrice, before.waiveUsage, before.collection = g.previous.BasePrice, g.previous.WaiveUsage, g.previous.Collection
	}
	if g.change != nil {
		after = *g.change
	}
	sameBase := before.basePrice.Valid == after.basePrice.Valid
	if sameBase && before.basePrice.Valid {
		a, aErr := decimal.NewFromString(before.basePrice.String)
		b, bErr := decimal.NewFromString(after.basePrice.String)
		sameBase = aErr == nil && bErr == nil && a.Equal(b)
	}
	return !sameBase || before.waiveUsage != after.waiveUsage || before.collection != after.collection
}

// loadGrantForMutation reads the current grant, treating absence as an ungranted subscription.
func loadGrantForMutation(ctx context.Context, db purserdb.DBTX, tenantID string) (*purserdb.GetSubscriptionOperatorGrantRow, error) {
	row, err := purserdb.New(db).GetSubscriptionOperatorGrant(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load operator grant: %v", err)
	}
	return &row, nil
}

// checkUnchanged fences the grant against replacement while its phase was rated.
func (g *operatorGrantMutation) checkUnchanged(ctx context.Context, q *purserdb.Queries, tenantID string) error {
	current, err := q.GetSubscriptionOperatorGrant(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) && g.previous == nil {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return txStatusErrorf(err, codes.Internal, "check operator grant revision: %v", err)
	}
	if g.previous == nil || err != nil || !current.GrantedAt.Equal(g.previous.GrantedAt) {
		return errPhaseChangedWhileRated
	}
	return nil
}

// writeOperatorGrantMutationTx archives and writes the arrangement with its subscription locked.
func (s *PurserServer) writeOperatorGrantMutationTx(ctx context.Context, tx *sql.Tx, current purserdb.LockTenantSubscriptionForOperatorAssignmentRow, tenantID, tierID string, g *operatorGrantMutation) error {
	q := purserdb.New(tx)
	var err error
	reason := g.reason
	if !g.at.IsZero() {
		if err = q.SetOperatorGrantBoundary(ctx, g.at.UTC().Format(time.RFC3339Nano)); err != nil {
			return txStatusErrorf(err, codes.Internal, "set operator grant boundary: %v", err)
		}
	}
	if g.change == nil {
		if _, err = q.DeleteSubscriptionOperatorGrant(ctx, current.SubscriptionID); err != nil {
			return txStatusErrorf(err, codes.Internal, "delete operator grant: %v", err)
		}
	} else {
		change := g.change
		if checkoutErr := subscriptions.CheckGrantCheckout(ctx, tx, tenantID); checkoutErr != nil {
			if errors.Is(checkoutErr, subscriptions.ErrPendingCheckout) || subscriptions.IsPricingRefusal(checkoutErr) {
				return status.Error(codes.FailedPrecondition, checkoutErr.Error())
			}
			return txStatusErrorf(checkoutErr, codes.Internal, "check provider checkout: %v", checkoutErr)
		}
		if change.expiresAt.Valid && !g.at.IsZero() && !change.expiresAt.Time.After(g.at) {
			return status.Error(codes.InvalidArgument, "grant expires before its billing boundary; choose a later expiry")
		}
		reason = change.reason
		provider, err := q.GetProviderSubscriptionState(ctx, tenantID)
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "load grant payment provider: %v", err)
		}
		if change.basePrice.Valid && (provider.HasStripeSubscription || provider.HasMollieSubscription) {
			return providerSubscriptionBillsBaseFee()
		}
		fallback := ""
		if g.previous != nil {
			fallback = g.previous.FallbackTierID
		}
		if fallback == "" && current.TierID != tierID && !g.clearFallback {
			fallback = current.TierID
		}
		if g.clearFallback {
			fallback = ""
		}
		if g.fallbackTierName != nil {
			requested := strings.TrimSpace(*g.fallbackTierName)
			if requested == "" {
				return status.Error(codes.InvalidArgument, "fallback tier must not be empty")
			}
			tier, tierErr := q.GetFallbackTierForOperatorGrant(ctx, requested)
			if errors.Is(tierErr, sql.ErrNoRows) {
				return status.Errorf(codes.NotFound, "fallback tier %q does not exist", requested)
			}
			if tierErr != nil {
				return txStatusErrorf(tierErr, codes.Internal, "load fallback tier: %v", tierErr)
			}
			if !tier.IsActive {
				return status.Error(codes.FailedPrecondition, "fallback tier is inactive")
			}
			fallback = tier.ID
		}
		if fallback != "" && g.fallbackTierName == nil {
			name := current.TierName
			if g.previous != nil && g.previous.FallbackTierID == fallback {
				name = g.previous.FallbackTierName
			}
			fallbackTier, lookupErr := q.GetFallbackTierForOperatorGrant(ctx, name)
			if errors.Is(lookupErr, sql.ErrNoRows) {
				return status.Errorf(codes.NotFound, "fallback tier %q does not exist", name)
			}
			if lookupErr != nil {
				return txStatusErrorf(lookupErr, codes.Internal, "load fallback tier: %v", lookupErr)
			}
			if !fallbackTier.IsActive {
				return status.Error(codes.FailedPrecondition, "fallback tier is inactive; choose an active --fallback-tier")
			}
		}
		if g.previous != nil && current.TierID == tierID && !g.changesPrices() &&
			g.previous.FallbackTierID == fallback && g.previous.Reason == change.reason &&
			g.previous.ExpiresAt.Valid == change.expiresAt.Valid &&
			(!change.expiresAt.Valid || g.previous.ExpiresAt.Time.Equal(change.expiresAt.Time)) {
			return nil
		}
		if err = q.UpsertSubscriptionOperatorGrant(ctx, purserdb.UpsertSubscriptionOperatorGrantParams{
			SubscriptionID: current.SubscriptionID, BasePrice: change.basePrice, WaiveUsage: change.waiveUsage,
			Collection: change.collection, ExpiresAt: change.expiresAt, Reason: reason, GrantedBy: middleware.GetUserID(ctx),
			FallbackTierID: sql.NullString{String: fallback, Valid: fallback != ""}, AssignedTierID: sql.NullString{String: tierID, Valid: true}, EffectiveAt: sql.NullTime{Time: g.at, Valid: !g.at.IsZero()},
		}); err != nil {
			return txStatusErrorf(err, codes.Internal, "store operator grant: %v", err)
		}
	}
	g.mutated = true
	return s.enqueueGrantChangedTx(ctx, tx, tenantID, current.SubscriptionID, middleware.GetUserID(ctx), reason)
}

// endOperatorGrant closes the granted phase and restores an eligible fallback atomically.
func (s *PurserServer) endOperatorGrant(ctx context.Context, tenantID, reason string, expiredOnly bool) error {
	return s.endOperatorGrantToTier(ctx, tenantID, reason, expiredOnly, "")
}

// endOperatorGrantToTier permits an operator to recover an outstanding grant
// whose remembered fallback is no longer assignable.
func (s *PurserServer) endOperatorGrantToTier(ctx context.Context, tenantID, reason string, expiredOnly bool, recoveryTier string) error {
	for attempt := 1; ; attempt++ {
		grant, err := loadGrantForMutation(ctx, s.db, tenantID)
		if err != nil {
			return err
		}
		if grant == nil {
			if expiredOnly {
				return nil
			}
			return status.Error(codes.NotFound, "tenant has no billing grant")
		}
		if expiredOnly && (!grant.ExpiresAt.Valid || grant.ExpiresAt.Time.After(time.Now())) {
			return nil
		}
		current, err := purserdb.New(s.db).GetSubscriptionTierForGrant(ctx, tenantID)
		if err != nil {
			return status.Errorf(codes.Internal, "load subscription tier: %v", err)
		}
		if current.Status == "cancelled" {
			cleanupErr := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
				q := purserdb.New(tx)
				var locked purserdb.LockTenantSubscriptionForOperatorAssignmentRow
				var lockErr error
				if expiredOnly {
					claimed, claimErr := q.LockTenantSubscriptionForGrantExpiry(ctx, tenantID)
					if errors.Is(claimErr, sql.ErrNoRows) {
						return errOperatorGrantBusy
					}
					locked, lockErr = purserdb.LockTenantSubscriptionForOperatorAssignmentRow(claimed), claimErr
				} else {
					locked, lockErr = q.LockTenantSubscriptionForOperatorAssignment(ctx, tenantID)
				}
				if lockErr != nil {
					return lockErr
				}
				if locked.Status != "cancelled" {
					return errPhaseChangedWhileRated
				}
				endedAt := time.Now().UTC()
				if grant.ExpiresAt.Valid && !grant.ExpiresAt.Time.After(endedAt) {
					endedAt = grant.ExpiresAt.Time
				}
				mutation := &operatorGrantMutation{previous: grant, reason: reason, at: endedAt}
				if checkErr := mutation.checkUnchanged(ctx, q, tenantID); checkErr != nil {
					return checkErr
				}
				return s.writeOperatorGrantMutationTx(ctx, tx, locked, tenantID, locked.TierID, mutation)
			})
			if errors.Is(cleanupErr, errOperatorGrantBusy) {
				return nil
			}
			if errors.Is(cleanupErr, errPhaseChangedWhileRated) && attempt < phaseCloseAttempts {
				continue
			}
			if cleanupErr == nil {
				if cacheErr := s.invalidateTenantCache(ctx, tenantID, "operator_grant_ended"); cacheErr != nil {
					s.logger.WithError(cacheErr).WithField("tenant_id", tenantID).Warn("Cancelled grant ended; cache convergence remains queued")
				}
			}
			return cleanupErr
		}
		target := current.TierName
		if grant.FallbackTierName != "" && current.TierID == grant.AssignedTierID {
			target = grant.FallbackTierName
		}
		if recoveryTier != "" {
			target = recoveryTier
		}
		at := time.Time{}
		if grant.ExpiresAt.Valid && !grant.ExpiresAt.Time.After(time.Now()) {
			at = grant.ExpiresAt.Time
		}
		requestedModel := ""
		if target == current.TierName {
			requestedModel = current.BillingModel
		}
		_, err = s.adminAssignTierOnce(ctx, tenantID, target, requestedModel, reason, &operatorGrantMutation{previous: grant, keepTier: recoveryTier == "" && target == current.TierName, at: at, expiredOnly: expiredOnly, reason: reason})
		if errors.Is(err, errSubscriptionCancelled) && attempt < phaseCloseAttempts {
			continue
		}
		if errors.Is(err, errPhaseChangedWhileRated) {
			if attempt < phaseCloseAttempts {
				continue
			}
			return status.Error(codes.Aborted, "billing phase changed while ending the grant; retry")
		}
		if errors.Is(err, errOperatorGrantBusy) {
			return nil
		}
		if err != nil {
			return err
		}
		fields := logging.Fields{"tenant_id": tenantID, "operator": middleware.GetUserID(ctx), "base_price": grant.BasePrice.String, "waive_usage": grant.WaiveUsage, "collection": grant.Collection, "reason": reason}
		if grant.ExpiresAt.Valid {
			fields["expires_at"] = grant.ExpiresAt.Time.UTC().Format(time.RFC3339)
		}
		s.logger.WithFields(fields).Info("Billing grant ended")
		return nil
	}
}

// runOperatorGrantExpirySweep retries due expiries until shutdown.
func (s *PurserServer) runOperatorGrantExpirySweep(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.expireOperatorGrantsOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// expireOperatorGrantsOnce attempts a bounded queue page without starving later tenants.
func (s *PurserServer) expireOperatorGrantsOnce(ctx context.Context) {
	rows, err := purserdb.New(s.db).ListExpiredOperatorGrants(ctx)
	if err != nil {
		if s.metrics != nil && s.metrics.OperatorGrantExpiryReadFailures != nil {
			s.metrics.OperatorGrantExpiryReadFailures.WithLabelValues().Inc()
		}
		s.logger.WithError(err).Warn("Could not list expired operator grants; retrying on the next sweep")
	} else {
		for _, row := range rows {
			tenantID := row.TenantID
			if err = s.endOperatorGrant(ctx, tenantID, "operator grant expired", true); err != nil {
				s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("End expired operator grant")
				if recordErr := purserdb.New(s.db).RecordOperatorGrantExpiryFailure(ctx, purserdb.RecordOperatorGrantExpiryFailureParams{TenantID: tenantID, GrantedAt: row.GrantedAt, Failure: err.Error()}); recordErr != nil {
					s.logger.WithError(recordErr).Warn("Record operator grant expiry retry")
				}
			}
		}
	}
	if s.metrics != nil && s.metrics.OperatorGrantExpiryFailures != nil {
		if failed, countErr := purserdb.New(s.db).CountFailedOperatorGrantExpiries(ctx); countErr == nil {
			s.metrics.OperatorGrantExpiryFailures.WithLabelValues().Set(float64(failed))
		} else {
			if s.metrics.OperatorGrantExpiryReadFailures != nil {
				s.metrics.OperatorGrantExpiryReadFailures.WithLabelValues().Inc()
			}
			s.logger.WithError(countErr).Warn("Could not count failed operator grant expiries; retaining last successful count")
		}
	}
	if s.metrics != nil && s.metrics.OperatorGrantExpiryRepeatedFailures != nil {
		if count, err := purserdb.New(s.db).CountRepeatedOperatorGrantExpiryFailures(ctx); err == nil {
			s.metrics.OperatorGrantExpiryRepeatedFailures.WithLabelValues().Set(float64(count))
		} else {
			if s.metrics.OperatorGrantExpiryReadFailures != nil {
				s.metrics.OperatorGrantExpiryReadFailures.WithLabelValues().Inc()
			}
			s.logger.WithError(err).Warn("Could not count repeatedly failed recovery work; retaining last successful count")
		}
	}
}

// NewBootstrapGrantExpiry returns the runtime expiry operation for bootstrap preparation.
// Expiry commits independently before the declaration is rated, using the same
// historical boundary and subscription lock as the sweep and operator commands.
func NewBootstrapGrantExpiry(db *sql.DB, logger logging.Logger, service *handlers.Service, qm *qmclient.GRPCClient, commodore handlers.CommodoreClient) func(context.Context, string) error {
	s := &PurserServer{db: db, logger: logger, billing: service, commodoreClient: commodore}
	if qm != nil {
		s.tierReconciler = tieraccess.NewReconciler(db, qm, logger, nil)
	}
	return func(ctx context.Context, tenantID string) error {
		return s.endOperatorGrant(ctx, tenantID, "operator grant expired", true)
	}
}
