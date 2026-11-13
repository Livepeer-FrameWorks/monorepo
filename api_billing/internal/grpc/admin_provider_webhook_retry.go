package grpc

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func providerWebhookRetryState(row purserdb.GetProviderWebhookRetryStateRow) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x1f%s\x1f%s\x1f%d\x1f%s\x1f%s", row.ID, row.Provider, row.Status, row.Attempts, row.LastErrorCode, row.UpdatedAt.UTC().Format(time.RFC3339Nano))))
	return hex.EncodeToString(sum[:])
}

// AdminRetryProviderWebhook queues the same callback after operator review.
// The native settlement and ledger remain immutable; conflicting provider facts
// block the next attempt again rather than being accepted by an override.
func (s *PurserServer) AdminRetryProviderWebhook(ctx context.Context, r *purserpb.AdminRetryProviderWebhookRequest) (*purserpb.AdminRetryProviderWebhookResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(r.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if _, err := uuid.Parse(r.GetWebhookId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "webhook_id must be a UUID")
	}
	reason := strings.TrimSpace(r.GetReason())
	if r.GetConfirmed() && (reason == "" || len(reason) > 1024 || r.GetExpectedState() == "") {
		return nil, status.Error(codes.InvalidArgument, "confirmation requires a reason of 1..1024 bytes and the preview state")
	}
	if s.db == nil {
		return nil, status.Error(codes.Unavailable, "provider recovery database unavailable")
	}
	out := &purserpb.AdminRetryProviderWebhookResponse{WebhookId: r.GetWebhookId()}
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := purserdb.New(tx)
		if r.GetConfirmed() {
			audit, err := q.FindProviderWebhookRetryAudit(ctx, purserdb.FindProviderWebhookRetryAuditParams{TenantID: r.GetTenantId(), WebhookID: r.GetWebhookId(), ExpectedState: r.GetExpectedState()})
			if err == nil {
				out.RetryRequested = true
				out.AuditId = audit
				out.Status = "retry_requested"
				return nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		row, err := q.GetProviderWebhookRetryState(ctx, purserdb.GetProviderWebhookRetryStateParams{TenantID: r.GetTenantId(), WebhookID: r.GetWebhookId()})
		if err != nil {
			return err
		}
		out.Provider = row.Provider
		out.ProviderPaymentId = row.ProviderPaymentID
		out.Status = row.Status
		out.ErrorCode = row.LastErrorCode
		out.ExpectedState = providerWebhookRetryState(row)
		// Polled Mollie conflicts cannot replay through the payment webhook:
		// the original payment event is already claimed, so it would skip the
		// conflicting settlement and incorrectly mark the evidence resolved.
		out.Eligible = row.Status == "blocked" && (row.Provider == "stripe" || (row.Provider == "mollie" && row.LastErrorCode != "evidence_conflict"))
		if !r.GetConfirmed() {
			return nil
		}
		if !out.Eligible || out.ExpectedState != r.GetExpectedState() {
			// A concurrent confirmation may have committed while the row lock waited.
			audit, readErr := q.FindProviderWebhookRetryAudit(ctx, purserdb.FindProviderWebhookRetryAuditParams{TenantID: r.GetTenantId(), WebhookID: r.GetWebhookId(), ExpectedState: r.GetExpectedState()})
			if readErr == nil {
				out.RetryRequested = true
				out.AuditId = audit
				out.Status = "retry_requested"
				out.Eligible = false
				return nil
			}
			if !errors.Is(readErr, sql.ErrNoRows) {
				return readErr
			}
			return status.Error(codes.FailedPrecondition, "provider callback changed or is not eligible; inspect a fresh preview")
		}
		audit, err := q.InsertProviderWebhookRetryAudit(ctx, purserdb.InsertProviderWebhookRetryAuditParams{TenantID: r.GetTenantId(), WebhookID: r.GetWebhookId(), Provider: row.Provider, ProviderPaymentID: row.ProviderPaymentID, PreviousErrorCode: row.LastErrorCode, ExpectedState: out.ExpectedState, ActorID: ctxkeys.GetUserID(ctx), Reason: reason})
		if err != nil {
			return err
		}
		changed, err := q.RequestProviderWebhookRetry(ctx, purserdb.RequestProviderWebhookRetryParams{TenantID: r.GetTenantId(), WebhookID: r.GetWebhookId()})
		if err != nil {
			return err
		}
		if changed != 1 {
			return status.Error(codes.FailedPrecondition, "provider callback is no longer blocked")
		}
		out.RetryRequested = true
		out.AuditId = audit
		out.Status = "retry_requested"
		out.Eligible = false
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "provider callback not found for tenant")
	}
	if err != nil {
		if status.Code(err) == codes.FailedPrecondition {
			return nil, err
		}
		return nil, status.Error(codes.Unavailable, "provider callback recovery could not be recorded")
	}
	return out, nil
}
