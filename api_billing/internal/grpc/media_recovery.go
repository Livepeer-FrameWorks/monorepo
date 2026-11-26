package grpc

import (
	"context"
	"database/sql"
	"errors"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

func requireMediaRecoveryService(ctx context.Context, tenant string) error {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return status.Error(codes.PermissionDenied, "media recovery requires service authentication")
	}
	if _, err := uuid.Parse(tenant); err != nil {
		return status.Error(codes.InvalidArgument, "valid tenant_id required")
	}
	return nil
}

func mediaRecoveryStatus(ctx context.Context, db purserdb.DBTX, tenant string) (*purserpb.MediaRecoveryStatus, error) {
	q := purserdb.New(db)
	row, err := q.GetMediaRecoverySnapshot(ctx, tenant)
	result := &purserpb.MediaRecoveryStatus{}
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	if row.CommittedCutoffAt.After(time.Unix(0, 0)) {
		result.CommittedCutoffAt = timestamppb.New(row.CommittedCutoffAt)
	}
	if row.CutoffAt.Valid {
		result.CutoffAt = timestamppb.New(row.CutoffAt.Time)
	}
	if row.FreeRecoveryUntil.Valid {
		result.RecoveryUntil = timestamppb.New(row.FreeRecoveryUntil.Time)
	}
	if row.TierName != "free" || row.BillingModel != "postpaid" || row.Status != "active" || row.PendingPurchase || row.BalanceCents > 0 && row.AutomaticPrepaidUsage || !row.CutoffAt.Valid || !row.FreeRecoveryUntil.Valid {
		return result, nil
	}
	closed, err := q.IsTenantBillingClosed(ctx, tenant)
	if err != nil {
		return nil, err
	}
	if closed {
		return result, nil
	}
	limits, err := handlers.ReadFreeTierLimits(ctx, db, row.TierID, row.SubscriptionID)
	if err != nil {
		return nil, err
	}
	// The 30-day recovery policy cannot shorten an operator's longer or unlimited retention grant.
	if limits.RetentionDays <= 0 || limits.RetentionDays > 30 {
		return result, nil
	}
	if !row.RecoveryNoticeAt.Valid || !row.RecoveryReminderAt.Valid {
		return result, nil
	}
	deadline := row.FreeRecoveryUntil.Time
	// Delayed mail extends protection; it never creates a surprise immediate expiry.
	for _, sent := range []time.Time{row.RecoveryNoticeAt.Time, row.RecoveryReminderAt.Time} {
		if sent.Add(7 * 24 * time.Hour).After(deadline) {
			deadline = sent.Add(7 * 24 * time.Hour)
		}
	}
	result.RecoveryUntil = timestamppb.New(deadline)
	result.Eligible = !row.DatabaseNow.Before(deadline)
	return result, nil
}

// GetMediaRecoveryStatus never serves last-good state for an irreversible expiry.
func (s *PurserServer) GetMediaRecoveryStatus(ctx context.Context, req *purserpb.GetMediaRecoveryStatusRequest) (*purserpb.MediaRecoveryStatus, error) {
	if err := requireMediaRecoveryService(ctx, req.GetTenantId()); err != nil {
		return nil, err
	}
	result, err := mediaRecoveryStatus(ctx, s.db, req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.Unavailable, "media recovery evidence unavailable")
	}
	return result, nil
}

// CommitMediaRecoveryExpiry serializes an expiry receipt with funding and tier changes.
// Funding before this commit cancels expiry; funding afterwards cannot revive already-expired media.
func (s *PurserServer) CommitMediaRecoveryExpiry(ctx context.Context, req *purserpb.CommitMediaRecoveryExpiryRequest) (*purserpb.MediaRecoveryStatus, error) {
	if err := requireMediaRecoveryService(ctx, req.GetTenantId()); err != nil {
		return nil, err
	}
	if req.ExpectedCutoffAt == nil || req.ExpectedRecoveryUntil == nil || req.ExpectedCutoffAt.CheckValid() != nil || req.ExpectedRecoveryUntil.CheckValid() != nil {
		return nil, status.Error(codes.InvalidArgument, "reviewed recovery cutoff and deadline required")
	}
	var result *purserpb.MediaRecoveryStatus
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		if _, err := q.LockTenantSubscriptionForOperatorAssignment(ctx, req.TenantId); err != nil {
			return err
		}
		if _, err := q.LockPrepaidBalance(ctx, purserdb.LockPrepaidBalanceParams{TenantID: req.TenantId, Currency: "EUR"}); err != nil {
			return err
		}
		current, err := mediaRecoveryStatus(ctx, tx, req.TenantId)
		if err != nil {
			return err
		}
		result = current
		if current.CommittedCutoffAt != nil && !current.CommittedCutoffAt.AsTime().Before(req.ExpectedCutoffAt.AsTime()) {
			return nil
		}
		if !current.Eligible {
			return status.Error(codes.FailedPrecondition, "media remains protected or paid service was restored")
		}
		if !current.CutoffAt.AsTime().Equal(req.ExpectedCutoffAt.AsTime()) || !current.RecoveryUntil.AsTime().Equal(req.ExpectedRecoveryUntil.AsTime()) {
			return status.Error(codes.Aborted, "media recovery changed; reload before expiry")
		}
		if err := q.InsertMediaRecoveryExpiry(ctx, purserdb.InsertMediaRecoveryExpiryParams{TenantID: req.TenantId, CutoffAt: current.CutoffAt.AsTime(), RecoveryUntil: current.RecoveryUntil.AsTime()}); err != nil {
			return err
		}
		result.CommittedCutoffAt = current.CutoffAt
		return nil
	})
	if err != nil {
		if status.Code(err) != codes.Unknown {
			return nil, err
		}
		return nil, status.Error(codes.Unavailable, "media expiry receipt unavailable")
	}
	return result, nil
}
