package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AdminSetUserActive changes a user's access and revokes their credentials while
// retaining tenant media. Only platform-operator JWTs may call it.
func (s *CommodoreServer) AdminSetUserActive(ctx context.Context, req *commodorepb.AdminSetUserActiveRequest) (*commodorepb.AdminSetUserActiveResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	tenantID := strings.TrimSpace(req.GetTenantId())
	userID, email := strings.TrimSpace(req.GetUserId()), normalizeEmail(req.GetEmail())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	tenantID = uuid.MustParse(tenantID).String()
	if (userID == "") == (email == "") {
		return nil, status.Error(codes.InvalidArgument, "exactly one of user_id or email is required")
	}
	if userID != "" {
		if _, err := uuid.Parse(userID); err != nil {
			return nil, status.Error(codes.InvalidArgument, "user_id must be a UUID")
		}
	}
	if strings.TrimSpace(req.GetReason()) == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	var response *commodorepb.AdminSetUserActiveResponse
	err := s.withEventTx(ctx, func(tx *sql.Tx) error {
		q := commodoredb.New(tx)
		if err := q.EnsureTenantAccountState(ctx, tenantID); err != nil {
			return err
		}
		closed, err := q.LockTenantAccountState(ctx, tenantID)
		if err != nil {
			return err
		}
		if closed.Valid {
			return status.Error(codes.FailedPrecondition, "tenant account is permanently closed")
		}
		prior, err := q.GetAccountUserForUpdate(ctx, commodoredb.GetAccountUserForUpdateParams{TenantID: tenantID, UserID: userID, Email: email})
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "user not found in tenant")
		}
		if err != nil {
			return err
		}
		response = &commodorepb.AdminSetUserActiveResponse{UserId: prior.ID, TenantId: tenantID, Email: prior.Email, Role: prior.Role, IsActive: prior.IsActive, Verified: prior.Verified}
		if prior.IsActive == req.GetActive() {
			return nil
		}
		if !req.GetActive() && prior.IsPlatformOperator {
			count, countErr := q.CountActivePlatformOperators(ctx, tenantID)
			if countErr != nil {
				return countErr
			}
			if count <= 1 {
				return status.Error(codes.FailedPrecondition, "cannot deactivate the last active platform operator")
			}
		}
		user, err := q.SetAccountUserActive(ctx, commodoredb.SetAccountUserActiveParams{TenantID: tenantID, UserID: prior.ID, Active: req.GetActive()})
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "user not found in tenant")
		}
		if err != nil {
			return err
		}
		if !req.GetActive() {
			if err = q.RevokeRefreshTokensForUser(ctx, commodoredb.RevokeRefreshTokensForUserParams{UserID: user.ID, TenantID: tenantID}); err != nil {
				return err
			}
			if err = s.revokeUserSessionsTx(ctx, tx, user.ID, tenantID, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_OPERATOR_DEACTIVATION); err != nil {
				return err
			}
		}
		streams, err := q.ListUserLiveStreamAuthorityTargets(ctx, commodoredb.ListUserLiveStreamAuthorityTargetsParams{TenantID: tenantID, UserID: user.ID})
		if err != nil {
			return err
		}
		for _, streamID := range streams {

			if !req.GetActive() {
				target := commodoredb.LiveStreamMediaAuthorityTarget(streamID)
				if enqueueErr := q.EnqueueMediaAuthorityObligation(ctx, commodoredb.EnqueueMediaAuthorityObligationParams{Lane: commodoredb.MediaAuthorityLaneIngestRevocation, TargetKey: target.Key, TargetKind: target.Kind, TenantID: tenantID, Reason: sharedauthority.ReasonAccountInactive, SourceService: "commodore", SourceEventID: "user-deactivated-stop:" + user.ID}); enqueueErr != nil {
					return enqueueErr
				}
			}
			if enqueueErr := q.EnqueueMediaAuthorityEvent(ctx, commodoredb.LiveStreamMediaAuthorityTarget(streamID), tenantID, "user_active_changed", "commodore", "user_active_changed:"+user.ID); enqueueErr != nil {
				return enqueueErr
			}
		}
		if err = q.RecordOperatorAccountAudit(ctx, commodoredb.RecordOperatorAccountAuditParams{TenantID: tenantID, ActorUserID: middleware.GetUserID(ctx), TargetID: user.ID, Action: fmt.Sprintf("user_active:%t", req.GetActive()), Reason: req.GetReason()}); err != nil {
			return err
		}
		_, err = s.enqueueDomainEventTx(ctx, tx, tenantID, &domainEvent{msg: &internalv1.UserActiveChanged{UserId: user.ID, Active: req.GetActive()}, aggregateID: user.ID}, time.Now().UTC())
		response = &commodorepb.AdminSetUserActiveResponse{UserId: user.ID, TenantId: tenantID, Email: user.Email, Role: user.Role, IsActive: user.IsActive, Verified: user.Verified}
		return err
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

// AdminListAccountHistory exposes private lifecycle reasons only to platform operators.
func (s *CommodoreServer) AdminListAccountHistory(ctx context.Context, req *commodorepb.AdminListAccountHistoryRequest) (*commodorepb.AdminListAccountHistoryResponse, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	tenant, err := uuid.Parse(strings.TrimSpace(req.GetTenantId()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	limit := req.GetLimit()
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		return nil, status.Error(codes.InvalidArgument, "limit must be 1..500")
	}
	beforeAt, beforeID := sql.NullTime{}, ""
	if req.GetBefore() != "" {
		cursor, cursorErr := pagination.DecodeCursor(req.GetBefore())
		if cursorErr != nil || cursor == nil || cursor.IsSortKey {
			return nil, status.Error(codes.InvalidArgument, "before must be a timestamp account-history cursor")
		}
		id, idErr := uuid.Parse(cursor.ID)
		if idErr != nil {
			return nil, status.Error(codes.InvalidArgument, "before cursor must contain an audit UUID")
		}
		beforeAt, beforeID = sql.NullTime{Time: cursor.Timestamp, Valid: true}, id.String()
	}
	rows, err := commodoredb.New(s.db).ListOperatorAccountAudit(ctx, commodoredb.ListOperatorAccountAuditParams{TenantID: tenant.String(), RowLimit: limit + 1, BeforeAt: beforeAt, BeforeID: beforeID})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "read private account history: %v", err)
	}
	out := &commodorepb.AdminListAccountHistoryResponse{HasMore: len(rows) > int(limit)}
	if out.HasMore {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		out.NextCursor = pagination.EncodeCursor(last.CreatedAt, last.ID)
	}
	for _, row := range rows {
		out.Entries = append(out.Entries, &commodorepb.OperatorAccountAuditEntry{Id: row.ID, ActorUserId: row.ActorUserID, TargetId: row.TargetID, Action: row.Action, Reason: row.Reason, CreatedAt: timestamppb.New(row.CreatedAt)})
	}
	if req.GetBefore() == "" {
		if out.AccountState, err = s.tenantAccountState(ctx, tenant.String()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// AdminDeactivateTenant closes an existing tenant locally and queues its inactive
// state for Quartermaster. It requires an operator JWT and the exact tenant name.
func (s *CommodoreServer) AdminDeactivateTenant(ctx context.Context, req *commodorepb.AdminDeactivateTenantRequest) (*emptypb.Empty, error) {
	if err := middleware.RequirePlatformOperatorJWT(ctx); err != nil {
		return nil, err
	}
	tenantID := strings.TrimSpace(req.GetTenantId())
	if _, err := uuid.Parse(tenantID); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	tenantID = uuid.MustParse(tenantID).String()
	if tenantID == s.systemTenantID.String() {
		return nil, status.Error(codes.FailedPrecondition, "the system tenant cannot be closed")
	}
	if strings.TrimSpace(req.GetReason()) == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}
	closedAlready, err := commodoredb.New(s.db).IsTenantAccountClosed(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if !closedAlready {
		source := s.authorityTenantSource
		if source == nil && s.quartermasterClient != nil {
			source = s.quartermasterClient
		}
		if source == nil {
			return nil, status.Error(codes.Unavailable, "Quartermaster must confirm the tenant before closure")
		}
		tenant, lookupErr := source.GetTenant(ctx, tenantID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if tenant.GetTenant().GetId() != tenantID {
			return nil, status.Error(codes.NotFound, "tenant not found")
		}
		if req.GetTenantName() == "" || req.GetTenantName() != tenant.GetTenant().GetName() {
			return nil, status.Error(codes.FailedPrecondition, "tenant name confirmation does not match Quartermaster")
		}
	}
	err = s.withEventTx(ctx, func(tx *sql.Tx) error {
		q := commodoredb.New(tx)
		if ensureErr := q.EnsureTenantAccountState(ctx, tenantID); ensureErr != nil {
			return ensureErr
		}
		closed, lockErr := q.LockTenantAccountState(ctx, tenantID)
		if lockErr != nil {
			return lockErr
		}
		if closed.Valid {
			return nil
		}
		if err = q.CloseTenantAccount(ctx, commodoredb.CloseTenantAccountParams{TenantID: tenantID, Reason: req.GetReason(), ClosedBy: middleware.GetUserID(ctx)}); err != nil {
			return err
		}
		// A closed account never signs in again, so its wallet addresses are released for a new sign-up or a
		// declared bootstrap account instead of staying bound to it.
		if err = q.DeleteTenantWalletIdentities(ctx, tenantID); err != nil {
			return err
		}
		users, listErr := q.ListTenantAccountUsers(ctx, tenantID)
		if listErr != nil {
			return listErr
		}
		for _, userID := range users {
			if _, err = q.SetAccountUserActive(ctx, commodoredb.SetAccountUserActiveParams{TenantID: tenantID, UserID: userID, Active: false}); err != nil {
				return err
			}
			if err = q.RevokeRefreshTokensForUser(ctx, commodoredb.RevokeRefreshTokensForUserParams{UserID: userID, TenantID: tenantID}); err != nil {
				return err
			}
			if err = s.revokeUserSessionsTx(ctx, tx, userID, tenantID, internalv1.SessionRevocationReason_SESSION_REVOCATION_REASON_OPERATOR_DEACTIVATION); err != nil {
				return err
			}
		}
		if enqueueErr := q.EnqueueMediaAuthorityObligation(ctx, commodoredb.EnqueueMediaAuthorityObligationParams{Lane: commodoredb.MediaAuthorityLaneIngestRevocation, TargetKey: commodoredb.TenantMediaAuthorityTarget(tenantID).Key, TargetKind: commodoredb.MediaAuthorityTargetTenant, TenantID: tenantID, Reason: sharedauthority.ReasonAccountInactive, SourceService: "commodore", SourceEventID: "account-closed-stop:" + tenantID}); enqueueErr != nil {
			return enqueueErr
		}
		if err = q.EnqueueMediaAuthorityEvent(ctx, commodoredb.TenantMediaAuthorityTarget(tenantID), tenantID, "account_closed", "commodore", "account_closed:"+tenantID); err != nil {
			return err
		}
		if err = q.RecordOperatorAccountAudit(ctx, commodoredb.RecordOperatorAccountAuditParams{TenantID: tenantID, ActorUserID: middleware.GetUserID(ctx), TargetID: tenantID, Action: "tenant_closed", Reason: req.GetReason()}); err != nil {
			return err
		}
		_, err = s.enqueueDomainEventTx(ctx, tx, tenantID, &domainEvent{msg: &internalv1.TenantAccountClosed{}, aggregateID: tenantID}, time.Now().UTC())
		return err
	})
	if err != nil {
		return nil, err
	}
	if err = s.finishTenantAccountClosure(ctx, tenantID); err != nil {
		return nil, status.Errorf(codes.Unavailable, "local access revoked; tenant synchronization will retry: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// finishTenantAccountClosure synchronizes the committed local closure independently of session-stop delivery.
func (s *CommodoreServer) finishTenantAccountClosure(ctx context.Context, tenantID string) error {
	if s.quartermasterClient == nil {
		return errors.New("quartermaster is unavailable")
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.quartermasterClient.DeleteTenant(callCtx, tenantID); err != nil && status.Code(err) != codes.NotFound {
		return err
	}
	return commodoredb.New(s.db).CompleteTenantAccountClosure(ctx, tenantID)
}

// runTenantAccountClosures retries the durable closure queue until shutdown.
func (s *CommodoreServer) runTenantAccountClosures(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		s.retryTenantAccountClosuresOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// retryTenantAccountClosuresOnce claims each due closure independently so failures cannot starve later tenants.
func (s *CommodoreServer) retryTenantAccountClosuresOnce(ctx context.Context) {
	for attempted := 0; attempted < 100; attempted++ {
		rows, err := commodoredb.New(s.db).ClaimPendingTenantAccountClosures(ctx)
		if err != nil {
			s.logger.WithError(err).Warn("Claim pending tenant closures")
			break
		}
		if len(rows) == 0 {
			break
		}
		row := rows[0]
		if err := s.finishTenantAccountClosure(ctx, row.TenantID); err != nil {
			s.logger.WithError(err).WithField("tenant_id", row.TenantID).Warn("Finish tenant closure")
			if recordErr := commodoredb.New(s.db).RecordTenantAccountClosureFailure(ctx, commodoredb.RecordTenantAccountClosureFailureParams{TenantID: row.TenantID, AttemptedAt: row.ClosureAttemptedAt, ExpectedFailures: row.ClosureFailures, Failure: err.Error()}); recordErr != nil {
				s.logger.WithError(recordErr).Warn("Record tenant closure retry")
			}
		}
	}
	if s.metrics != nil && s.metrics.TenantAccountClosureFailures != nil {
		if count, err := commodoredb.New(s.db).CountFailedTenantAccountClosures(ctx); err != nil {
			if s.metrics.TenantAccountClosureReadFailures != nil {
				s.metrics.TenantAccountClosureReadFailures.WithLabelValues().Inc()
			}
			s.logger.WithError(err).Warn("Could not count failed tenant closures; retaining last successful count")
		} else {
			s.metrics.TenantAccountClosureFailures.WithLabelValues().Set(float64(count))
		}
	}
	if s.metrics != nil && s.metrics.TenantAccountClosureRepeatedFailures != nil {
		if count, err := commodoredb.New(s.db).CountRepeatedTenantAccountClosureFailures(ctx); err == nil {
			s.metrics.TenantAccountClosureRepeatedFailures.WithLabelValues().Set(float64(count))
		} else {
			if s.metrics.TenantAccountClosureReadFailures != nil {
				s.metrics.TenantAccountClosureReadFailures.WithLabelValues().Inc()
			}
			s.logger.WithError(err).Warn("Could not count repeatedly failed recovery work; retaining last successful count")
		}
	}
}
