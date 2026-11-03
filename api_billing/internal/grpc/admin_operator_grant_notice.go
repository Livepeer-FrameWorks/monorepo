package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminResendOperatorGrantNotice requeues only the abandoned notice shown to
// the operator. It never changes the grant or the subscription.
func (s *PurserServer) AdminResendOperatorGrantNotice(ctx context.Context, r *purserpb.AdminResendOperatorGrantNoticeRequest) (*purserpb.AdminResendOperatorGrantNoticeResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(r.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if !r.GetConfirmed() {
		row, err := purserdb.New(s.db).GetAbandonedOperatorGrantNotice(ctx, r.GetTenantId())
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tenant has no abandoned grant notice")
		}
		if err != nil {
			return nil, status.Errorf(codes.Unavailable, "read grant notice: %v", err)
		}
		return &purserpb.AdminResendOperatorGrantNoticeResponse{
			Status: "notice_abandoned", Recipient: row.Recipient, Kind: row.Kind,
			EndsAt: timestamppb.New(row.EndsAt), Detail: row.LastError,
		}, nil
	}
	kind := r.GetKind()
	if strings.TrimSpace(r.GetReason()) == "" || (kind != handlers.OperatorGrantNoticeEnding && kind != handlers.OperatorGrantNoticeEnded) ||
		r.GetEndsAt() == nil || r.GetEndsAt().CheckValid() != nil {
		return nil, status.Error(codes.InvalidArgument, "confirmation requires a reason and the preview's kind and ends_at")
	}
	key := purserdb.GetOperatorGrantNoticeRecoveryParams{TenantID: r.GetTenantId(), Kind: kind, EndsAt: r.GetEndsAt().AsTime()}
	out := &purserpb.AdminResendOperatorGrantNoticeResponse{Kind: kind, EndsAt: r.GetEndsAt()}
	_, err := runRetryableTx(ctx, s.db, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		current, readErr := q.GetOperatorGrantNoticeRecovery(ctx, key)
		if readErr != nil {
			return readErr
		}
		out.Recipient = current.Recipient
		if current.Notified {
			return status.Error(codes.FailedPrecondition, "grant notice was already sent")
		}
		if current.Status == "cancelled" {
			return status.Error(codes.FailedPrecondition, "account is closed; its grant notices are not sent")
		}
		if strings.TrimSpace(current.Recipient) == "" {
			return status.Error(codes.FailedPrecondition, "set billing_email before retrying the notice")
		}
		rows, updateErr := q.RequeueAbandonedOperatorGrantNotice(ctx, purserdb.RequeueAbandonedOperatorGrantNoticeParams(key))
		if updateErr != nil {
			return updateErr
		}
		if rows == 0 {
			out.Status = "already_queued"
			return nil
		}
		return billingevents.NewAndEnqueue(ctx, tx, r.GetTenantId(), current.SubscriptionID, &internalv1.SubscriptionUpdated{
			SubscriptionId: current.SubscriptionID,
			ChangedFields:  []string{"operator_grant_notice_retry"}, Reason: strings.TrimSpace(r.GetReason()),
		}, s.domainActor(ctx))
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "grant notice changed after preview; preview it again")
	}
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return nil, err
		}
		return nil, status.Errorf(codes.Unavailable, "queue grant notice: %v", err)
	}
	if out.Status == "already_queued" {
		out.Detail = "A previous request already queued this notice; no additional operator action was recorded."
		return out, nil
	}
	out.Status, out.Detail = "queued", "The notice is queued for the grant notice worker; the grant and the subscription are unchanged."
	return out, nil
}
