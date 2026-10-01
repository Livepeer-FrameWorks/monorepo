package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	grantCollectionProvider = "provider"
	grantCollectionInvoice  = "invoice"
)

// operatorGrantChange is a validated AdminSetBillingGrant request.
type operatorGrantChange struct {
	basePrice  sql.NullString
	waiveUsage bool
	collection string
	expiresAt  sql.NullTime
	reason     string
}

func parseOperatorGrantChange(req *purserpb.AdminSetBillingGrantRequest, now time.Time) (operatorGrantChange, error) {
	change := operatorGrantChange{
		waiveUsage: req.GetWaiveUsage(),
		collection: strings.ToLower(strings.TrimSpace(req.GetCollection())),
		reason:     strings.TrimSpace(req.GetReason()),
	}
	if change.reason == "" {
		return change, status.Error(codes.InvalidArgument, "reason is required; it is recorded on the grant and the audit event")
	}
	switch change.collection {
	case "":
		change.collection = grantCollectionProvider
	case grantCollectionProvider, grantCollectionInvoice:
	default:
		return change, status.Errorf(codes.InvalidArgument, `collection must be "provider" or "invoice", got %q`, req.GetCollection())
	}
	if req.BasePrice != nil {
		base, err := decimal.NewFromString(strings.TrimSpace(req.GetBasePrice()))
		if err != nil || base.IsNegative() || base.Exponent() < -2 {
			return change, status.Errorf(codes.InvalidArgument, "base_price must be a non-negative EUR amount with at most two decimals, got %q", req.GetBasePrice())
		}
		change.basePrice = sql.NullString{String: base.StringFixed(2), Valid: true}
	}
	if req.GetExpiresAt() != nil {
		expires := req.GetExpiresAt().AsTime()
		if !expires.After(now) {
			return change, status.Errorf(codes.InvalidArgument, "expires_at %s is not in the future", expires.Format(time.RFC3339))
		}
		change.expiresAt = sql.NullTime{Time: expires, Valid: true}
	}
	if !change.basePrice.Valid && !change.waiveUsage && change.collection == grantCollectionProvider {
		return change, status.Error(codes.InvalidArgument, "the grant changes nothing: set a base price, waive usage, or collection invoice")
	}
	return change, nil
}

// AdminSetBillingGrant records an operator's billing arrangement for a
// tenant, optionally assigning a tier first. A grant that sets the base fee is
// refused while a Stripe or Mollie subscription exists: that subscription
// bills the tier's price on the provider's side, outside Purser's invoice.
func (s *PurserServer) AdminSetBillingGrant(ctx context.Context, req *purserpb.AdminSetBillingGrantRequest) (*purserpb.AdminBillingGrantResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	change, err := parseOperatorGrantChange(req, time.Now())
	if err != nil {
		return nil, err
	}
	if change.basePrice.Valid {
		if refuseErr := s.refuseBaseFeeGrantWithProviderSubscription(ctx, tenantID); refuseErr != nil {
			return nil, refuseErr
		}
	}

	resp := &purserpb.AdminBillingGrantResponse{}
	if tierName := strings.TrimSpace(req.GetTierName()); tierName != "" {
		assigned, assignErr := s.AdminAssignTier(ctx, &purserpb.AdminAssignTierRequest{
			TenantId: tenantID, TierName: tierName, Reason: change.reason,
		})
		if assignErr != nil {
			return nil, assignErr
		}
		resp.Assignment = assigned
	}

	userID := middleware.GetUserID(ctx)
	stage, err := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		sub, subErr := queries.GetProviderSubscriptionState(ctx, tenantID)
		if errors.Is(subErr, sql.ErrNoRows) {
			return status.Error(codes.FailedPrecondition, "tenant has no billing subscription; assign a tier first (--tier)")
		}
		if subErr != nil {
			return txStatusErrorf(subErr, codes.Internal, "load subscription: %v", subErr)
		}
		if change.basePrice.Valid && (sub.HasStripeSubscription || sub.HasMollieSubscription) {
			return providerSubscriptionBillsBaseFee()
		}
		if upsertErr := queries.UpsertSubscriptionOperatorGrant(ctx, purserdb.UpsertSubscriptionOperatorGrantParams{
			SubscriptionID: sub.SubscriptionID, BasePrice: change.basePrice, WaiveUsage: change.waiveUsage,
			Collection: change.collection, ExpiresAt: change.expiresAt, Reason: change.reason, GrantedBy: userID,
		}); upsertErr != nil {
			return txStatusErrorf(upsertErr, codes.Internal, "store grant: %v", upsertErr)
		}
		return s.enqueueGrantChangedTx(ctx, tx, tenantID, sub.SubscriptionID, userID, change.reason)
	})
	if err != nil {
		return nil, grantTxError(stage, "grant", err)
	}
	s.afterGrantChange(ctx, tenantID)

	if err := s.fillGrantResponse(ctx, tenantID, resp); err != nil {
		return nil, err
	}
	s.logger.WithFields(logging.Fields{
		"tenant_id":    tenantID,
		"operator":     userID,
		"base_price":   change.basePrice.String,
		"waive_usage":  change.waiveUsage,
		"collection":   change.collection,
		"expires_at":   change.expiresAt.Time,
		"grant_reason": change.reason,
	}).Info("Operator set billing grant")
	return resp, nil
}

// AdminGetBillingGrant returns the tenant's grant, including one that expired.
func (s *PurserServer) AdminGetBillingGrant(ctx context.Context, req *purserpb.AdminGetBillingGrantRequest) (*purserpb.AdminBillingGrantResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	resp := &purserpb.AdminBillingGrantResponse{}
	if err := s.fillGrantResponse(ctx, tenantID, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// AdminRevokeBillingGrant removes the tenant's grant; the tenant is billed
// and admitted by the tier and its own payment setup again.
func (s *PurserServer) AdminRevokeBillingGrant(ctx context.Context, req *purserpb.AdminRevokeBillingGrantRequest) (*purserpb.AdminBillingGrantResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	reason := strings.TrimSpace(req.GetReason())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required; it is recorded on the audit event")
	}
	userID := middleware.GetUserID(ctx)
	stage, err := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		sub, subErr := queries.GetProviderSubscriptionState(ctx, tenantID)
		if errors.Is(subErr, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "tenant has no billing subscription")
		}
		if subErr != nil {
			return txStatusErrorf(subErr, codes.Internal, "load subscription: %v", subErr)
		}
		removed, delErr := queries.DeleteSubscriptionOperatorGrant(ctx, sub.SubscriptionID)
		if delErr != nil {
			return txStatusErrorf(delErr, codes.Internal, "remove grant: %v", delErr)
		}
		if removed == 0 {
			return status.Error(codes.NotFound, "tenant has no billing grant")
		}
		return s.enqueueGrantChangedTx(ctx, tx, tenantID, sub.SubscriptionID, userID, reason)
	})
	if err != nil {
		return nil, grantTxError(stage, "grant revocation", err)
	}
	s.afterGrantChange(ctx, tenantID)

	resp := &purserpb.AdminBillingGrantResponse{}
	if err := s.fillGrantResponse(ctx, tenantID, resp); err != nil {
		return nil, err
	}
	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID, "operator": userID, "revoke_reason": reason,
	}).Info("Operator revoked billing grant")
	return resp, nil
}

// AdminRecordInvoicePayment records the full open balance of a pending or
// overdue invoice as a bank transfer the operator received, and settles it.
func (s *PurserServer) AdminRecordInvoicePayment(ctx context.Context, req *purserpb.AdminRecordInvoicePaymentRequest) (*purserpb.AdminRecordInvoicePaymentResponse, error) {
	tenantID := strings.TrimSpace(req.GetTenantId())
	invoiceID := strings.TrimSpace(req.GetInvoiceId())
	reference := strings.TrimSpace(req.GetReference())
	reason := strings.TrimSpace(req.GetReason())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if _, err := uuid.Parse(invoiceID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invoice_id must be a UUID")
	}
	if reference == "" {
		return nil, status.Error(codes.InvalidArgument, "reference is required: the bank transfer or other reference the payment arrived with")
	}
	if reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required; it is recorded on the audit event")
	}
	userID := middleware.GetUserID(ctx)
	resp := &purserpb.AdminRecordInvoicePaymentResponse{}
	stage, err := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		*resp = purserpb.AdminRecordInvoicePaymentResponse{}
		if err := lockInvoicePaymentTx(ctx, tx, invoiceID); err != nil {
			return txStatusErrorf(err, codes.Internal, "lock invoice: %v", err)
		}
		balance, err := loadInvoiceBalanceTx(ctx, tx, invoiceID, tenantID)
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "invoice not found for this tenant, or not pending or overdue")
		}
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "load invoice balance: %v", err)
		}
		if !balance.AmountDue.IsPositive() {
			return status.Error(codes.FailedPrecondition, "invoice has no outstanding balance")
		}
		paymentFX, err := handlers.InvoicePaymentFX(ctx, tx, tenantID, invoiceID, balance.Currency, balance.AmountDueMinor)
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "record payment in EUR: %v", err)
		}
		paymentID := uuid.New().String()
		now := time.Now()
		queries := purserdb.New(tx)
		if err = queries.CreateConfirmedOperatorInvoicePayment(ctx, purserdb.CreateConfirmedOperatorInvoicePaymentParams{
			PaymentID: paymentID, InvoiceID: invoiceID, Amount: balance.AmountDue.StringFixed(2),
			Currency: balance.Currency, Reference: reference,
			ConfirmedAt:         sql.NullTime{Time: now, Valid: true},
			OriginalAmountCents: paymentFX.OriginalMinor, EurAmountCents: paymentFX.EURMinor,
			FxUnitsPerEur: paymentFX.UnitsText(), FxSource: paymentFX.Source, FxReferenceDate: paymentFX.ReferenceDate,
		}); err != nil {
			return txStatusErrorf(err, codes.Internal, "store payment: %v", err)
		}
		paid, err := handlers.SettleCoveredInvoiceTx(ctx, tx, queries, invoiceID, now)
		if err != nil {
			return txStatusErrorf(err, codes.Internal, "settle invoice: %v", err)
		}
		if !paid {
			return status.Error(codes.Internal, "recorded payment did not settle the invoice")
		}
		resp.PaymentId = paymentID
		resp.Amount = balance.AmountDue.StringFixed(2)
		resp.Currency = balance.Currency
		resp.InvoiceStatus = "paid"
		return nil
	})
	if err != nil {
		return nil, grantTxError(stage, "payment recording", err)
	}
	s.logger.WithFields(logging.Fields{
		"tenant_id": tenantID, "invoice_id": invoiceID, "payment_id": resp.GetPaymentId(),
		"amount": resp.GetAmount(), "currency": resp.GetCurrency(), "reference": reference,
		"operator": userID, "record_reason": reason,
	}).Info("Operator recorded invoice payment")
	return resp, nil
}

// operatorGrantSetsBaseFee reports whether an operator grant in force sets
// the tenant's base fee.
func (s *PurserServer) operatorGrantSetsBaseFee(ctx context.Context, tenantID string) (bool, error) {
	overrides, err := purserdb.New(s.db).GetActiveOperatorBaseFeeOverride(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, status.Errorf(codes.Internal, "load operator grant: %v", err)
	}
	return overrides, nil
}

func (s *PurserServer) refuseBaseFeeGrantWithProviderSubscription(ctx context.Context, tenantID string) error {
	sub, err := purserdb.New(s.db).GetProviderSubscriptionState(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return status.Errorf(codes.Internal, "load subscription: %v", err)
	}
	if sub.HasStripeSubscription || sub.HasMollieSubscription {
		return providerSubscriptionBillsBaseFee()
	}
	return nil
}

func providerSubscriptionBillsBaseFee() error {
	return status.Error(codes.FailedPrecondition,
		"the tenant's Stripe or Mollie subscription bills the tier's base fee on the provider's side; cancel it before setting a base fee by grant")
}

func (s *PurserServer) enqueueGrantChangedTx(ctx context.Context, tx *sql.Tx, tenantID, subscriptionID, userID, reason string) error {
	current, err := purserdb.New(tx).LockTenantSubscriptionForOperatorAssignment(ctx, tenantID)
	if err != nil {
		return txStatusErrorf(err, codes.Internal, "load subscription: %v", err)
	}
	updated, err := billingevents.New(tenantID, subscriptionID, &internalv1.SubscriptionUpdated{
		SubscriptionId: subscriptionID, TierId: current.TierID, Status: current.Status,
		ChangedFields: []string{"operator_grant"}, Reason: reason,
	}, s.domainActor(ctx))
	if err != nil {
		return txStatusErrorf(err, codes.Internal, "build subscription_updated: %v", err)
	}
	if _, err = s.EnqueueBillingEventTx(ctx, tx, eventSubscriptionUpdated, tenantID, userID, "subscription", subscriptionID, &ipcpb.BillingEvent{
		SubscriptionId: subscriptionID, Status: current.Status,
	}, updated); err != nil {
		return txStatusErrorf(err, codes.Internal, "enqueue subscription_updated: %v", err)
	}
	return nil
}

// afterGrantChange drops cached tenant billing state so admission reads the
// new arrangement. A failure is logged, not returned: the grant is stored and
// caches expire on their own.
func (s *PurserServer) afterGrantChange(ctx context.Context, tenantID string) {
	if err := s.invalidateTenantCache(ctx, tenantID, "billing_grant_changed"); err != nil {
		s.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Could not invalidate tenant cache after billing grant change")
	}
}

func (s *PurserServer) fillGrantResponse(ctx context.Context, tenantID string, resp *purserpb.AdminBillingGrantResponse) error {
	grant, err := s.loadOperatorGrant(ctx, tenantID)
	if err != nil {
		return err
	}
	resp.Grant = grant
	tier, err := purserdb.New(s.db).GetSubscriptionTierForGrant(ctx, tenantID)
	switch {
	case err == nil:
		resp.Tier = &purserpb.BillingGrantTier{
			SubscriptionId: tier.SubscriptionID, TierId: tier.TierID, TierName: tier.TierName,
			TierLevel: tier.TierLevel, BillingModel: tier.BillingModel,
		}
	case !errors.Is(err, sql.ErrNoRows):
		return status.Errorf(codes.Internal, "load subscription tier: %v", err)
	}
	admission, err := s.GetTenantAdmissionStatus(ctx, &purserpb.GetTenantAdmissionStatusRequest{TenantId: tenantID})
	if err != nil {
		return err
	}
	resp.CollectionReady = admission.GetCollectionReady()
	resp.CollectionProvider = admission.GetCollectionProvider()
	return nil
}

// loadOperatorGrant returns the tenant's grant, or nil when it has none.
func (s *PurserServer) loadOperatorGrant(ctx context.Context, tenantID string) (*purserpb.OperatorBillingGrant, error) {
	row, err := purserdb.New(s.db).GetSubscriptionOperatorGrant(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load operator grant: %v", err)
	}
	grant := &purserpb.OperatorBillingGrant{
		SubscriptionId: row.SubscriptionID, WaiveUsage: row.WaiveUsage, Collection: row.Collection,
		Reason: row.Reason, GrantedBy: row.GrantedBy.String, GrantedAt: timestamppb.New(row.GrantedAt),
		Active: row.Active,
	}
	if row.BasePrice.Valid {
		base := row.BasePrice.String
		grant.BasePrice = &base
	}
	if row.ExpiresAt.Valid {
		grant.ExpiresAt = timestamppb.New(row.ExpiresAt.Time)
	}
	return grant, nil
}

func grantTxError(stage txStage, what string, err error) error {
	switch stage {
	case txStageBegin:
		return status.Errorf(codes.Internal, "begin %s: %v", what, err)
	case txStageCommit:
		return status.Errorf(codes.Internal, "commit %s: %v", what, err)
	default:
		return err
	}
}
