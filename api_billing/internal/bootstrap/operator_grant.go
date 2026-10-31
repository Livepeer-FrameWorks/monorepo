package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/shopspring/decimal"
)

// PreparedCustomerGrant fences a declaration against changes since its billing phase was rated.
type PreparedCustomerGrant struct {
	deferred          error
	applied           bool
	tenantID          string
	checkGrant        bool
	close             *handlers.PostpaidPhaseClose
	prepaid           *handlers.PrepaidPhaseClose
	expectedGrantedAt *time.Time
}

// customerDeclarationDeferred identifies business state requiring operator recovery.
type customerDeclarationDeferred struct{ error }

var errCustomerGrantChanged = errors.New("operator grant changed while bootstrap was prepared; retry bootstrap")

func checkCustomerBillingModel(ctx context.Context, exec DBTX, tenantID string, entry CustomerBilling) error {
	current, err := purserdb.New(exec).GetSubscriptionTierForGrant(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.Status == "cancelled" {
		return &customerDeclarationDeferred{fmt.Errorf("tenant %s subscription is cancelled: revoke its outstanding grant before starting provider checkout", tenantID)}
	}
	if current.BillingModel != entry.Model {
		return &customerDeclarationDeferred{fmt.Errorf("tenant %s runs %s billing; declaration requires %s: run frameworks admin billing set-tier --tenant-id %s --tier %s --billing-model %s --reason <reason>, then re-run bootstrap", tenantID, current.BillingModel, entry.Model, tenantID, entry.Tier, entry.Model)}
	}
	return nil
}

// PrepareCustomerGrants rates pricing changes and reports per-tenant failures without
// preventing healthy tenants from reconciling. A nil expiry operation is read-only.
func PrepareCustomerGrants(ctx context.Context, db *sql.DB, entries []CustomerBilling, qm QMBootstrapClient, logger logging.Logger, service *handlers.Service, endExpired func(context.Context, string) error) (map[string]*PreparedCustomerGrant, error) {
	prepared := map[string]*PreparedCustomerGrant{}
	for _, entry := range entries {
		alias, err := aliasFromRef(entry.Tenant.Ref)
		if err != nil {
			return nil, err
		}
		var grant *PreparedCustomerGrant
		err = database.RetryPostgres(ctx, 3, 25*time.Millisecond, func() error {
			var prepareErr error
			grant, prepareErr = prepareCustomerGrant(ctx, db, entry, alias, qm, logger, service, endExpired)
			return prepareErr
		})
		if err != nil {
			var deferred *customerDeclarationDeferred
			if !errors.As(err, &deferred) {
				return nil, fmt.Errorf("customer_billing[%s]: %w", alias, err)
			}
			logger.WithError(err).WithField("alias", alias).Warn("Bootstrap billing declaration deferred")
			grant = &PreparedCustomerGrant{deferred: err}
		}
		prepared[alias] = grant
	}
	return prepared, nil
}

// prepareCustomerGrant prepares one tenant before the shared reconciliation transaction.
func prepareCustomerGrant(ctx context.Context, db *sql.DB, entry CustomerBilling, alias string, qm QMBootstrapClient, logger logging.Logger, service *handlers.Service, endExpired func(context.Context, string) error) (*PreparedCustomerGrant, error) {
	tenantID, err := qm.Resolve(ctx, alias)
	if err != nil {
		return nil, &customerDeclarationDeferred{err}
	}
	prepared := &PreparedCustomerGrant{tenantID: tenantID}
	_, grantErr := purserdb.New(db).GetSubscriptionOperatorGrant(ctx, tenantID)
	if grantErr != nil && !errors.Is(grantErr, sql.ErrNoRows) {
		return nil, grantErr
	}
	declaredChange, changeErr := customerGrantNeedsWrite(ctx, db, tenantID, entry)
	if changeErr != nil {
		return nil, changeErr
	}
	if grantErr == nil && !declaredChange {
		current, loadErr := purserdb.New(db).GetSubscriptionTierForGrant(ctx, tenantID)
		if loadErr != nil {
			return nil, loadErr
		}
		if current.Status != "cancelled" {
			return prepared, nil
		}
	}
	if modelErr := checkCustomerBillingModel(ctx, db, tenantID, entry); modelErr != nil {
		return nil, modelErr
	}
	if entry.OperatorGrant == nil {
		return prepared, nil
	}
	if validationErr := entry.OperatorGrant.Validate(); validationErr != nil {
		return nil, &customerDeclarationDeferred{validationErr}
	}
	changed, err := customerGrantNeedsWrite(ctx, db, tenantID, entry)
	if err != nil || !changed {
		return prepared, err
	}
	current, err := purserdb.New(db).GetSubscriptionOperatorGrant(ctx, tenantID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil && current.ExpiresAt.Valid && !current.ExpiresAt.Time.After(time.Now()) {
		if endExpired == nil {
			return nil, &customerDeclarationDeferred{errors.New("expired grant must be ended before this declaration; dry-run defers this tenant")}
		}
		if expiryErr := endExpired(ctx, tenantID); expiryErr != nil {
			if database.IsRetryablePostgresError(expiryErr) {
				return nil, expiryErr
			}
			return nil, &customerDeclarationDeferred{expiryErr}
		}
		current, err = purserdb.New(db).GetSubscriptionOperatorGrant(ctx, tenantID)
		if err == nil {
			return nil, &customerDeclarationDeferred{errors.New("expired grant is being processed by another replica")}
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if modelErr := checkCustomerBillingModel(ctx, db, tenantID, entry); modelErr != nil {
			return nil, modelErr
		}
	}
	prepared.checkGrant = true
	if err == nil {
		logger.WithFields(logging.Fields{"tenant_id": tenantID, "previous_granted_by": current.GrantedBy.String}).Info("Bootstrap declaration updates billing grant")
		prepared.expectedGrantedAt = &current.GrantedAt
		tier, tierErr := purserdb.New(db).GetSubscriptionTierForGrant(ctx, tenantID)
		if tierErr != nil {
			return nil, tierErr
		}
		g := entry.OperatorGrant
		collection := g.Collection
		if collection == "" {
			collection = "provider"
		}
		sameBase := g.BaseFee == nil && !current.BasePrice.Valid
		if g.BaseFee != nil && current.BasePrice.Valid {
			a, aErr := decimal.NewFromString(*g.BaseFee)
			b, bErr := decimal.NewFromString(current.BasePrice.String)
			sameBase = aErr == nil && bErr == nil && a.Equal(b)
		}
		if tier.TierName == entry.Tier && tier.BillingModel == entry.Model && sameBase && current.WaiveUsage == g.WaiveUsage && current.Collection == collection {
			return prepared, nil
		}
	}
	prepared.close, err = handlers.PreparePostpaidGrantBoundary(ctx, db, logger, service, tenantID, time.Time{}, false)
	if err != nil {
		return nil, err
	}
	prepared.prepaid, err = handlers.PreparePrepaidGrantBoundary(ctx, db, logger, service, tenantID, time.Time{})
	return prepared, err
}

// customerGrantNeedsWrite compares the declaration with the tenant's current grant.
func customerGrantNeedsWrite(ctx context.Context, exec DBTX, tenantID string, entry CustomerBilling) (bool, error) {
	if entry.OperatorGrant == nil {
		return false, nil
	}
	g := entry.OperatorGrant
	until, err := g.Expiry()
	if err != nil {
		return false, err
	}
	if !until.IsZero() && !until.After(time.Now()) {
		return false, nil
	}
	current, err := purserdb.New(exec).GetSubscriptionOperatorGrant(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	tier, err := purserdb.New(exec).GetSubscriptionTierForGrant(ctx, tenantID)
	if err != nil {
		return false, err
	}
	if tier.TierName != entry.Tier || current.AssignedTierID != tier.TierID {
		return true, nil
	}
	collection := g.Collection
	if collection == "" {
		collection = "provider"
	}
	sameBase := g.BaseFee == nil && !current.BasePrice.Valid
	if g.BaseFee != nil && current.BasePrice.Valid {
		desired, parseErr := decimal.NewFromString(strings.TrimSpace(*g.BaseFee))
		if parseErr != nil {
			return false, parseErr
		}
		actual, err := decimal.NewFromString(current.BasePrice.String)
		sameBase = err == nil && desired.Equal(actual)
	}
	sameUntil := until.IsZero() && !current.ExpiresAt.Valid || current.ExpiresAt.Valid && current.ExpiresAt.Time.Equal(until)
	return current.FallbackTierID != "" || !sameBase || !sameUntil || current.WaiveUsage != g.WaiveUsage || current.Collection != collection || current.Reason != g.Reason, nil
}

// reconcileCustomerGrant writes a prepared grant in the subscription transaction.
func reconcileCustomerGrant(ctx context.Context, exec DBTX, tenantID, tierID string, entry CustomerBilling, prepared *PreparedCustomerGrant) (bool, error) {
	changed, err := customerGrantNeedsWrite(ctx, exec, tenantID, entry)
	if err != nil || !changed {
		return false, err
	}
	q := purserdb.New(exec)
	subscription, err := q.LockTenantSubscriptionForOperatorAssignment(ctx, tenantID)
	if err != nil {
		return false, err
	}
	// Existing billable time must be finalized before new grant prices apply.
	if prepared == nil || !prepared.checkGrant {
		return false, fmt.Errorf("operator grant mutation was not prepared before reconciliation")
	}
	provider, err := q.GetProviderSubscriptionState(ctx, tenantID)
	if err != nil {
		return false, err
	}
	g := entry.OperatorGrant
	if g.BaseFee != nil && (provider.HasStripeSubscription || provider.HasMollieSubscription) {
		return false, fmt.Errorf("provider subscription bills the base fee; cancel it before declaring a base fee grant")
	}
	base := sql.NullString{}
	if g.BaseFee != nil {
		amount, parseErr := decimal.NewFromString(strings.TrimSpace(*g.BaseFee))
		if parseErr != nil {
			return false, parseErr
		}
		base = sql.NullString{String: amount.StringFixed(2), Valid: true}
	}
	until, expiryErr := g.Expiry()
	if expiryErr != nil {
		return false, expiryErr
	}
	collection := g.Collection
	if collection == "" {
		collection = "provider"
	}
	effectiveAt := sql.NullTime{}
	if prepared.close != nil {
		effectiveAt = sql.NullTime{Time: prepared.close.BoundaryAt(), Valid: true}
	}
	if prepared.prepaid != nil {
		effectiveAt = sql.NullTime{Time: prepared.prepaid.BoundaryAt(), Valid: true}
	}
	if err = q.UpsertSubscriptionOperatorGrant(ctx, purserdb.UpsertSubscriptionOperatorGrantParams{
		SubscriptionID: subscription.SubscriptionID, BasePrice: base, WaiveUsage: g.WaiveUsage, Collection: collection,
		ExpiresAt: sql.NullTime{Time: until, Valid: !until.IsZero()}, Reason: g.Reason, GrantedBy: "bootstrap",
		AssignedTierID: sql.NullString{String: tierID, Valid: true}, EffectiveAt: effectiveAt,
	}); err != nil {
		return false, err
	}
	err = billingevents.NewAndEnqueue(ctx, exec, tenantID, subscription.SubscriptionID, &internalv1.SubscriptionUpdated{
		SubscriptionId: subscription.SubscriptionID, TierId: tierID, Status: subscription.Status, ChangedFields: []string{"operator_grant"}, Reason: g.Reason,
	}, events.Actor{AuthType: "service"})
	return true, err
}

// AfterCustomerGrantsCommit dispatches side effects of committed closing documents.
func AfterCustomerGrantsCommit(ctx context.Context, prepared map[string]*PreparedCustomerGrant) {
	for _, grant := range prepared {
		if grant == nil || !grant.applied {
			continue
		}
		if grant.prepaid != nil {
			grant.prepaid.Log()
		}
		if grant.close != nil {
			grant.close.AfterCommit(ctx)
		}
	}
}

// CheckUnchanged rejects a grant replaced after preparation.
func (p *PreparedCustomerGrant) CheckUnchanged(ctx context.Context, exec DBTX, tenantID string) error {
	if !p.checkGrant {
		return nil
	}
	current, err := purserdb.New(exec).GetSubscriptionOperatorGrant(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) && p.expectedGrantedAt == nil {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if p.expectedGrantedAt == nil || err != nil || !current.GrantedAt.Equal(*p.expectedGrantedAt) {
		return errCustomerGrantChanged
	}
	return nil
}
