package grpc

import (
	"context"
	"errors"
	"strings"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminListCheckoutRecovery exposes bounded provider-work diagnostics to platform operators.
func (s *PurserServer) AdminListCheckoutRecovery(ctx context.Context, r *purserpb.AdminListCheckoutRecoveryRequest) (*purserpb.AdminListCheckoutRecoveryResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if r.GetTenantId() != "" {
		if _, err := uuid.Parse(r.GetTenantId()); err != nil {
			return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
		}
	}
	limit := r.GetLimit()
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || r.GetOffset() < 0 {
		return nil, status.Error(codes.InvalidArgument, "limit must be 1..100 and offset non-negative")
	}
	if r.GetHistory() {
		if r.GetTenantId() == "" {
			return nil, status.Error(codes.InvalidArgument, "history requires tenant_id")
		}
		rows, err := purserdb.New(s.db).ListCheckoutRecoveryAudit(ctx, purserdb.ListCheckoutRecoveryAuditParams{TenantID: r.GetTenantId(), RowLimit: int64(limit) + 1, RowOffset: int64(r.GetOffset())})
		if err != nil {
			return nil, status.Errorf(codes.Internal, "read recovery audit: %v", err)
		}
		out := &purserpb.AdminListCheckoutRecoveryResponse{HasMore: len(rows) > int(limit)}
		if out.HasMore {
			rows = rows[:limit]
		}
		for _, row := range rows {
			out.Entries = append(out.Entries, &purserpb.CheckoutRecoveryAudit{Id: row.ID, ReservationId: row.IntentID, ActorId: row.ActorID, Reason: row.Reason, Outcome: row.Outcome, Detail: row.Detail, CreatedAt: timestamppb.New(row.CreatedAt)})
		}
		return out, nil
	}
	rows, err := purserdb.New(s.db).ListCheckoutRecovery(ctx, purserdb.ListCheckoutRecoveryParams{TenantID: r.GetTenantId(), RowLimit: int64(limit) + 1, RowOffset: int64(r.GetOffset())})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read recovery inventory: %v", err)
	}
	out := &purserpb.AdminListCheckoutRecoveryResponse{HasMore: len(rows) > int(limit)}
	if out.HasMore {
		rows = rows[:limit]
	}
	for _, row := range rows {
		item := &purserpb.CheckoutRecoveryItem{Id: row.ID, TenantId: row.TenantID, ReservationId: row.ReservationID, Provider: row.Provider, EventKey: row.EventKey, Status: row.Status, Attempts: row.Attempts, LastError: row.LastError.String, NextAttemptAt: timestamppb.New(row.NextAttemptAt), AmountCents: row.AmountCents, Currency: row.Currency, ReservationStatus: row.ReservationStatus, AmountKnown: row.Provider == "mollie" && row.ReservationID != ""}
		if row.Status == "delivery_review" {
			item.NextAttemptAt = nil
		}
		if row.ExpiresAt.Valid {
			item.ExpiresAt = timestamppb.New(row.ExpiresAt.Time)
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}

// AdminRecoverCheckout previews a reservation or persists a confirmed recovery request.
func (s *PurserServer) AdminRecoverCheckout(ctx context.Context, r *purserpb.AdminRecoverCheckoutRequest) (*purserpb.AdminRecoverCheckoutResponse, error) {
	return s.adminRecoverCheckout(ctx, r, false)
}

// AdminRetainDeliveredCheckout records delivery without authorizing compensation.
func (s *PurserServer) AdminRetainDeliveredCheckout(ctx context.Context, r *purserpb.AdminRecoverCheckoutRequest) (*purserpb.AdminRecoverCheckoutResponse, error) {
	return s.adminRecoverCheckout(ctx, r, true)
}

func (s *PurserServer) adminRecoverCheckout(ctx context.Context, r *purserpb.AdminRecoverCheckoutRequest, retainDelivered bool) (*purserpb.AdminRecoverCheckoutResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(r.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if _, err := uuid.Parse(r.GetReservationId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "reservation_id must be a UUID")
	}
	if strings.TrimSpace(r.GetReason()) == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	if s.billing == nil {
		return nil, status.Error(codes.Unavailable, "checkout recovery is unavailable")
	}
	var out *purserpb.AdminRecoverCheckoutResponse
	var err error
	if retainDelivered && !r.GetConfirmed() {
		out, err = s.billing.PreviewRetainDeliveredCheckout(ctx, r.GetTenantId(), r.GetReservationId())
	} else if retainDelivered {
		out, err = s.billing.RetainDeliveredCheckout(ctx, r.GetTenantId(), r.GetReservationId(), r.GetReason())
	} else if !r.GetConfirmed() {
		out, err = s.billing.PreviewCheckoutRecovery(ctx, r.GetTenantId(), r.GetReservationId())
	} else {
		out, err = s.billing.RecoverCheckoutResult(ctx, r.GetTenantId(), r.GetReservationId(), r.GetReason())
	}
	if err != nil {
		if errors.Is(err, handlers.ErrCheckoutReservationNotFound) {
			return nil, status.Error(codes.NotFound, "tenant checkout reservation not found")
		}
		if errors.Is(err, handlers.ErrCheckoutDeliveryDecisionNotAllowed) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, status.Errorf(codes.Unavailable, "checkout recovery request could not be reconciled; inspect recovery list: %v", err)
	}
	return out, nil
}
