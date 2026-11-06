package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
)

// StageTierUpgradeTx records a purchase and its retry work under the subscription
// lock. An outstanding checkout or grant must settle before accepting the upgrade.
func (s *Service) StageTierUpgradeTx(ctx context.Context, tx *sql.Tx, tenantID, tierID string, start, end time.Time) (string, error) {
	if err := subscriptions.CheckGrantCheckout(ctx, tx, tenantID); err != nil {
		return "", err
	}
	q := purserdb.New(tx)
	current, err := q.LockTenantSubscriptionForProviderChange(ctx, tenantID)
	if err != nil {
		return "", err
	}
	if current.Status != "active" || current.PendingReason.String != "" {
		return "", fmt.Errorf("%w: an active subscription without a scheduled downgrade or cancellation is required", billing.ErrTierUpgradeRejected)
	}
	// The purchase credits and replaces the provider's price, so that price must
	// have settled first.
	unfinished, err := q.HasUnfinishedProviderTierSync(ctx, tenantID)
	if err != nil {
		return "", err
	}
	if unfinished {
		return "", fmt.Errorf("%w: the provider subscription is still being updated for an earlier tier change; retry shortly", billing.ErrTierUpgradeRejected)
	}
	id, err := q.InsertTierUpgrade(ctx, purserdb.InsertTierUpgradeParams{ID: uuid.NewString(), TenantID: tenantID, TierID: tierID, ActorID: ctxkeys.GetUserID(ctx), PeriodStart: start, PeriodEnd: end})
	if err != nil {
		return "", err
	}
	row, err := q.GetTierUpgrade(ctx, purserdb.GetTierUpgradeParams{TenantID: tenantID, ID: id})
	if err != nil {
		return "", err
	}
	target, err := q.GetTierForBillingChange(ctx, tierID)
	if err != nil {
		return "", err
	}
	if eligibilityErr := subscriptions.ValidateSelfServiceTier(target); eligibilityErr != nil {
		return "", errors.Join(billing.ErrTierUpgradeRejected, eligibilityErr)
	}
	if row.BaseCents < row.OldBaseCents {
		return "", fmt.Errorf("%w: self-service upgrades require a published tier priced at least as high as the current tier", billing.ErrTierUpgradeRejected)
	}
	if !row.RequestedAt.Before(row.OldPeriodEnd) {
		return "", fmt.Errorf("%w: the current billing period must roll over before upgrading", billing.ErrTierUpgradeRejected)
	}
	if row.Provider == "advance" && row.OldBaseCents > 0 {
		previous, readErr := planPreviousBaseFee(ctx, q, tenantID, row.RequestedAt)
		if readErr != nil {
			if errors.Is(readErr, ErrBaseFeePaymentPending) {
				return "", fmt.Errorf("%w: %w", billing.ErrTierUpgradeRejected, readErr)
			}
			return "", readErr
		}
		if !previous.found {
			return "", fmt.Errorf("%w: %w; the current advance period has not been invoiced yet; retry after billing runs", billing.ErrTierUpgradeRejected, billing.ErrTierUpgradeUnsettled)
		}
	}
	if row.Provider == "stripe" && (s.stripeClient == nil || row.StripePriceID == "" || row.CustomerID == "") {
		return "", fmt.Errorf("%w: Stripe monthly collection and a target monthly price are required for this upgrade", billing.ErrTierUpgradeRejected)
	}
	if row.Provider == "mollie" && (s.mollieClient == nil || row.CustomerID == "") {
		return "", fmt.Errorf("%w: Mollie monthly collection is required for this upgrade", billing.ErrTierUpgradeRejected)
	}
	if err = q.StageTierUpgrade(ctx, purserdb.StageTierUpgradeParams{TenantID: tenantID, TierID: tierID, RequestedAt: row.RequestedAt}); err != nil {
		return "", err
	}
	return id, q.EnqueueTierUpgrade(ctx, purserdb.EnqueueTierUpgradeParams{TenantID: tenantID, ID: id})
}

// ProcessTierUpgrade reconciles provider pricing before committing the matching
// usage boundary. The internal inbox retries the same durable identity. Provider
// metadata locates an update whose response or local transaction was lost.
func (s *Service) ProcessTierUpgrade(ctx context.Context, tenantID, id string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var row purserdb.GetTierUpgradeRow
	var receipt billing.TierUpgradeReceipt
	var rejected error
	var accountClosed bool
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		rejected = nil
		receipt = billing.TierUpgradeReceipt{}
		locked, err := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
		if err != nil {
			return err
		}
		q := purserdb.New(tx)
		row, err = q.GetTierUpgrade(ctx, purserdb.GetTierUpgradeParams{TenantID: tenantID, ID: id})
		if err != nil || row.State == "completed" || row.State == "cancelled" {
			return err
		}
		if err = json.Unmarshal(row.ProviderReceipt, &receipt); err != nil {
			return err
		}
		accountClosed = locked.AccountClosed
		unapplied := row.State == "requested" || row.State == "provider_applied"
		// Local receipts become purchases only when pricing and their invoice
		// commit together. Cancellation before that commit ends the request.
		if unapplied && shouldCancelUnacceptedUpgrade(row, locked) {
			if !accountClosed && locked.Current.Status != "active" {
				rejected = fmt.Errorf("%w: subscription no longer active; the unapplied upgrade was cancelled", billing.ErrTierUpgradeRejected)
			}
			row.State = "cancelled"
			if err = q.ClearCancelledTierUpgrade(ctx, tenantID); err != nil {
				return err
			}
			return setTierUpgradeProgress(ctx, q, row, receipt)
		}
		if accountClosed && row.State == "provider_applied" {
			return s.settleClosedTierUpgradeTx(ctx, q, &row, receipt)
		}
		if row.State != "requested" {
			return nil
		}
		request := billing.TierUpgradeRequest{ID: id, TenantID: tenantID, OldTierID: row.OldTierID, TierID: row.TierID, CustomerID: row.CustomerID,
			SubscriptionID: row.ProviderSubscriptionID, PriceID: row.StripePriceID, RequestedAt: row.RequestedAt,
			OldPeriodStart: row.OldPeriodStart, OldPeriodEnd: row.OldPeriodEnd, BaseCents: row.BaseCents, OldBaseCents: row.OldBaseCents,
		}
		// Resolve a possibly committed purchase before applying eligibility
		// checks that may have changed while its response was lost.
		if row.Provider == "stripe" || row.Provider == "mollie" {
			request.LocateOnly = true
			receipt, err = s.providerTierUpgrade(ctx, row, request)
			if err == nil {
				if _, err = tierUpgradeBillingStart(row, receipt); err != nil {
					return err
				}
				row.State = "provider_applied"
				if accountClosed {
					return s.settleClosedTierUpgradeTx(ctx, q, &row, receipt)
				}
				return setTierUpgradeProgress(ctx, q, row, receipt)
			}
			if !errors.Is(err, billing.ErrTierUpgradeRejected) {
				return err
			}
			err = nil
			receipt = billing.TierUpgradeReceipt{}
			if accountClosed || row.RecoveryRequestedAt.Valid {
				row.State = "cancelled"
				if err = q.ClearCancelledTierUpgrade(ctx, tenantID); err != nil {
					return err
				}
				return setTierUpgradeProgress(ctx, q, row, receipt)
			}
			request.LocateOnly = false
		}
		if locked.HasGrant {
			return subscriptions.ErrOutstandingGrant
		}

		if !accountClosed && (locked.Current.TierID != row.OldTierID || locked.Current.BillingModel != "postpaid" || locked.Current.Status != "active") {
			rejected = fmt.Errorf("%w: the subscription changed before its accepted upgrade was reconciled", billing.ErrTierUpgradeRejected)
			row.State = "cancelled"
			if err = q.ClearCancelledTierUpgrade(ctx, tenantID); err != nil {
				return err
			}
			return setTierUpgradeProgress(ctx, q, row, receipt)
		}
		if !accountClosed && !row.RecoveryRequestedAt.Valid {
			if _, err = PrepareProviderBillingBoundary(ctx, s.db, s.logger, s, tenantID, row.RequestedAt); err != nil {
				return err
			}
		}
		if !accountClosed && (row.Provider == "advance" || row.Provider == "mollie") {
			if _, err = planPreviousBaseFee(ctx, q, tenantID, row.RequestedAt); err != nil {
				if !errors.Is(err, ErrBaseFeePaymentPending) {
					return err
				}
				err = fmt.Errorf("%w: %w", billing.ErrTierUpgradeRejected, err)
			}
		}
		// Leave the bounded provider request inside the old monthly period.
		// A located, already-committed purchase reconciles before this check.
		if err == nil && !time.Now().Add(30*time.Second).Before(row.OldPeriodEnd) {
			err = fmt.Errorf("%w: the previous billing period is ending; wait for renewal before purchasing an upgrade", billing.ErrTierUpgradeRejected)
		}
		if err == nil {
			receipt, err = s.providerTierUpgrade(ctx, row, request)
		}
		if errors.Is(err, billing.ErrTierUpgradeRejected) {
			if !accountClosed && !row.RecoveryRequestedAt.Valid {
				rejected = err
			}
			row.State = "cancelled"
		} else if err != nil {
			return err
		} else {
			if _, periodErr := tierUpgradeBillingStart(row, receipt); periodErr != nil {
				return periodErr
			}
			row.State = "provider_applied"
		}
		if accountClosed && row.State == "provider_applied" {
			return s.settleClosedTierUpgradeTx(ctx, q, &row, receipt)
		}
		if row.State == "cancelled" {
			if err = q.ClearCancelledTierUpgrade(ctx, tenantID); err != nil {
				return err
			}
		}
		return setTierUpgradeProgress(ctx, q, row, receipt)
	})
	if err != nil {
		return false, err
	}
	if rejected != nil || row.State == "cancelled" {
		return false, rejected
	}
	if row.State == "completed" {
		return true, nil
	}
	billingStart, err := tierUpgradeBillingStart(row, receipt)
	if err != nil {
		return false, err
	}
	var closing *PostpaidPhaseClose
	if row.State == "provider_applied" {
		// A provider-applied purchase has no cancellation path, so an audited
		// operator waiver is the only exit from a metering hold.
		if row.MeteringWaivedAt.Valid {
			closing, err = prepareAcceptedProviderBoundary(ctx, s.db, s.logger, s, tenantID, billingStart)
		} else {
			closing, err = PrepareProviderBillingBoundary(ctx, s.db, s.logger, s, tenantID, billingStart)
		}
		if err != nil {
			return false, err
		}
		err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
			rejected = nil
			locked, lockErr := subscriptions.LockChange(ctx, tx, tenantID, subscriptions.PreservePricing)
			if lockErr != nil {
				return lockErr
			}
			q := purserdb.New(tx)
			current, readErr := q.GetTierUpgrade(ctx, purserdb.GetTierUpgradeParams{TenantID: tenantID, ID: id})
			if readErr != nil {
				return readErr
			}
			row = current
			if row.State != "provider_applied" {
				return nil
			}
			if shouldCancelUnacceptedUpgrade(row, locked) {
				if !locked.AccountClosed && locked.Current.Status != "active" {
					rejected = fmt.Errorf("%w: subscription no longer active; the unapplied upgrade was cancelled", billing.ErrTierUpgradeRejected)
				}
				row.State = "cancelled"
				if clearErr := q.ClearCancelledTierUpgrade(ctx, tenantID); clearErr != nil {
					return clearErr
				}
				return setTierUpgradeProgress(ctx, q, row, receipt)
			}
			if locked.AccountClosed {
				closure, readErr := q.GetTenantBillingClosure(ctx, tenantID)
				if readErr != nil {
					return readErr
				}
				if !billingStart.Before(closure.ClosedAt) {
					return errors.New("the upgrade starts after the recorded account cutoff")
				}
			}
			if locked.HasGrant {
				return subscriptions.ErrOutstandingGrant
			}
			if closing != nil {
				if _, closeErr := closing.CommitTx(ctx, tx); closeErr != nil {
					return closeErr
				}
			}
			rows, applyErr := q.ApplyTierUpgradePeriod(ctx, purserdb.ApplyTierUpgradePeriodParams{TenantID: tenantID, TierID: row.TierID, OldTierID: row.OldTierID, PeriodStart: billingStart, PeriodEnd: receipt.PeriodEnd})
			if applyErr != nil {
				return applyErr
			}
			if rows != 1 {
				return errors.New("subscription pricing changed before the upgrade boundary committed")
			}
			if row.Provider == "mollie" || row.Provider == "advance" {
				if invoiceErr := createUpgradeInvoiceTx(ctx, tx, row, receipt); invoiceErr != nil {
					return invoiceErr
				}
			}
			tenantUUID, parseErr := uuid.Parse(tenantID)
			if parseErr != nil {
				return parseErr
			}
			if _, refreshErr := q.EnqueueMediaAuthorityRefresh(ctx, purserdb.EnqueueMediaAuthorityRefreshParams{SourceEventID: "tier-upgrade:" + id, TenantID: tenantUUID, Reason: "tier_changed"}); refreshErr != nil {
				return refreshErr
			}
			row.State = "pricing_applied"
			return setTierUpgradeProgress(ctx, q, row, receipt)
		})
		err = RecordBillingPhaseTaxReview(ctx, err)
		if err != nil {
			return false, err
		}
		if row.State == "cancelled" {
			return false, rejected
		}
		if closing != nil {
			closing.AfterCommit(ctx)
		}
	}
	if row.Provider == "mollie" || row.Provider == "advance" {
		if s.chargeTierUpgradeInvoice == nil {
			return false, errors.New("tier upgrade invoice collection is not configured")
		}
		if err = s.chargeTierUpgradeInvoice(ctx, tenantID, row.CollectionProvider, receipt.PeriodStart); err != nil {
			return false, err
		}
	}
	if !accountClosed && s.convergeTenantEntitlements != nil {
		if err = s.convergeTenantEntitlements(ctx, tenantID); err != nil {
			return false, err
		}
	}
	row.State = "completed"
	return true, setTierUpgradeProgress(ctx, purserdb.New(s.db), row, receipt)
}

// A local receipt is unaccepted until pricing and its invoice commit. A paid
// Stripe purchase must instead reconcile against the recorded closure cutoff.
func shouldCancelUnacceptedUpgrade(row purserdb.GetTierUpgradeRow, locked subscriptions.LockedSubscription) bool {
	if row.State != "requested" && row.State != "provider_applied" {
		return false
	}
	if locked.AccountClosed {
		return row.Provider != "stripe"
	}
	// Mollie's price PATCH collects no upgrade payment. A confirmed stop of
	// that same native subscription leaves only the old paid term to retain.
	if row.Provider == "mollie" && locked.Current.Status == "pending_cancellation" && locked.Current.FreeFallbackProviderEndedAt.Valid && locked.Current.MollieSubscriptionID.String == row.ProviderSubscriptionID {
		return true
	}
	if row.Provider != "advance" && row.Provider != "invoice" {
		return false
	}
	return row.RecoveryRequestedAt.Valid || locked.Current.Status != "active"
}

// Stripe's anchors have second precision; a preceding grant boundary has
// microseconds. Keep the provider's receipt for event ordering, while preventing
// its rounded anchor from moving the local billing phase backwards.
func tierUpgradeBillingStart(row purserdb.GetTierUpgradeRow, receipt billing.TierUpgradeReceipt) (time.Time, error) {
	earliest := row.OldPeriodStart
	if row.Provider == "stripe" {
		earliest = earliest.Truncate(time.Second)
	}
	start := receipt.PeriodStart
	if !receipt.Applied || start.Before(earliest) {
		return time.Time{}, errors.New("provider upgrade has no valid replacement period")
	}
	if start.Before(row.OldPeriodStart) {
		start = row.OldPeriodStart
	}
	if !receipt.PeriodEnd.After(start) {
		return time.Time{}, errors.New("provider upgrade has no valid replacement period")
	}
	return start, nil
}

func setTierUpgradeProgress(ctx context.Context, q *purserdb.Queries, row purserdb.GetTierUpgradeRow, receipt billing.TierUpgradeReceipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("encode tier upgrade receipt: %w", err)
	}
	return q.SetTierUpgradeProgress(ctx, purserdb.SetTierUpgradeProgressParams{TenantID: row.TenantID, ID: row.ID, State: row.State, Receipt: raw})
}

// A confirmed purchase before closure keeps its paid month and is applied up
// to the cutoff. A purchase starting at or after closure never delivered its
// replacement month, so only that purchase's receipt is compensated.
func (s *Service) settleClosedTierUpgradeTx(ctx context.Context, q *purserdb.Queries, row *purserdb.GetTierUpgradeRow, receipt billing.TierUpgradeReceipt) error {
	closure, err := q.GetTenantBillingClosure(ctx, row.TenantID)
	if err != nil {
		return err
	}
	if receipt.PeriodStart.Before(closure.ClosedAt) {
		return setTierUpgradeProgress(ctx, q, *row, receipt)
	}
	if row.Provider == "stripe" {
		if s.stripeClient == nil {
			return errors.New("stripe upgrade compensation is not configured")
		}
		result, refundErr := s.stripeClient.RefundUnappliedTierUpgrade(ctx, row.ID, receipt.PaidReceiptID)
		if refundErr != nil {
			return refundErr
		}
		if err = enqueueRefundSettlements(ctx, q, row.TenantID, result.RefundSettlements); err != nil {
			return err
		}
	}
	row.State = "cancelled"
	if err = q.ClearCancelledTierUpgrade(ctx, row.TenantID); err != nil {
		return err
	}
	return setTierUpgradeProgress(ctx, q, *row, receipt)
}

func (s *Service) providerTierUpgrade(ctx context.Context, row purserdb.GetTierUpgradeRow, request billing.TierUpgradeRequest) (billing.TierUpgradeReceipt, error) {
	switch row.Provider {
	case "stripe":
		if s.stripeClient == nil {
			return billing.TierUpgradeReceipt{}, errors.New("stripe collection is not configured")
		}
		if !request.LocateOnly {
			profile, taxErr := AssessStripeTaxProfile(ctx, s.db, row.TenantID, ctxkeys.GetClientIP(ctx))
			if taxErr != nil {
				return billing.TierUpgradeReceipt{}, fmt.Errorf("retain paid-upgrade tax assessment: %w", taxErr)
			}
			if taxErr = s.stripeClient.SyncSubscriptionTax(ctx, row.TenantID, request.CustomerID, request.SubscriptionID, *profile); taxErr != nil {
				return billing.TierUpgradeReceipt{}, fmt.Errorf("synchronize paid-upgrade tax: %w", taxErr)
			}
		}
		return s.stripeClient.UpgradeMonthlySubscription(ctx, request)
	case "mollie":
		if s.mollieClient == nil {
			return billing.TierUpgradeReceipt{}, errors.New("mollie collection is not configured")
		}
		if !request.LocateOnly {
			if err := s.syncMollieUpgradeTax(ctx, row.TenantID, request.CustomerID, request.SubscriptionID); err != nil {
				return billing.TierUpgradeReceipt{}, fmt.Errorf("synchronize paid-upgrade tax: %w", err)
			}
		}
		return s.mollieClient.UpgradeMonthlySubscription(ctx, request)
	case "advance", "invoice":
		at := row.RequestedAt.UTC().Truncate(time.Microsecond)
		return billing.TierUpgradeReceipt{Applied: true, PeriodStart: at, PeriodEnd: billing.NextMonthlyAnniversary(at)}, nil
	default:
		return billing.TierUpgradeReceipt{}, errors.New("unknown tier upgrade collection provider")
	}
}
