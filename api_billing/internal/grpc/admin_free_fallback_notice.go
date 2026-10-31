package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/subscriptions"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminResendFreeFallbackNotice requeues only the abandoned notification shown
// to the operator. It never changes the subscription or authorizes a refund.
func (s *PurserServer) AdminResendFreeFallbackNotice(ctx context.Context, r *purserpb.AdminResendFreeFallbackNoticeRequest) (*purserpb.AdminResendFreeFallbackNoticeResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(r.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if r.GetConfirmed() && (strings.TrimSpace(r.GetReason()) == "" || r.GetScheduledAt() == nil || r.GetScheduledAt().CheckValid() != nil) {
		return nil, status.Error(codes.InvalidArgument, "confirmation requires a reason and the preview's scheduled_at")
	}
	row, err := purserdb.New(s.db).GetFreeFallbackNoticeRecovery(ctx, r.GetTenantId())
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant has no pending fallback notice")
	}
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "read fallback notice: %v", err)
	}
	out := &purserpb.AdminResendFreeFallbackNoticeResponse{Status: "notice_abandoned", Recipient: row.Recipient, ScheduledAt: timestamppb.New(row.ScheduledAt), Moved: row.Moved, Detail: row.LastError}
	if !r.GetConfirmed() {
		if !row.FreeFallbackNoticeAbandonedAt.Valid {
			return nil, status.Error(codes.FailedPrecondition, "fallback notice is already queued; inspect recovery list")
		}
		return out, nil
	}
	_, err = runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		locked, lockErr := subscriptions.LockChange(ctx, tx, r.GetTenantId(), subscriptions.PreservePricing)
		if lockErr != nil {
			return lockErr
		}
		q := purserdb.New(tx)
		current, readErr := q.GetFreeFallbackNoticeRecovery(ctx, r.GetTenantId())
		if readErr != nil {
			return readErr
		}
		if !current.ScheduledAt.Equal(r.GetScheduledAt().AsTime()) || current.Moved != r.GetMoved() {
			return status.Error(codes.FailedPrecondition, "fallback notice changed after preview; preview it again")
		}
		if strings.TrimSpace(current.Recipient) == "" {
			return status.Error(codes.FailedPrecondition, "set billing_email before retrying the notice")
		}
		out.Recipient = current.Recipient
		rows, updateErr := q.RequeueAbandonedFreeFallbackNotice(ctx, purserdb.RequeueAbandonedFreeFallbackNoticeParams{TenantID: r.GetTenantId(), ScheduledAt: current.ScheduledAt, Moved: current.Moved})
		if updateErr != nil || rows == 0 {
			return updateErr
		}
		return billingevents.NewAndEnqueue(ctx, tx, r.GetTenantId(), locked.Current.SubscriptionID, &internalv1.SubscriptionUpdated{SubscriptionId: locked.Current.SubscriptionID, ChangedFields: []string{"free_fallback_notice_retry"}, Reason: strings.TrimSpace(r.GetReason())}, s.domainActor(ctx))
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "tenant has no pending fallback notice")
	}
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return nil, err
		}
		return nil, status.Errorf(codes.Unavailable, "queue fallback notice: %v", err)
	}
	out.Status, out.Detail = "queued", "The notification is durably queued for the fallback email worker; billing and payment objects are unchanged."
	return out, nil
}
