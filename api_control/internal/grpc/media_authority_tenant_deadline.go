package grpc

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"golang.org/x/sync/errgroup"
)

const (
	tenantMediaAuthorityDeadlineReason  = "tenant_authority:deadline_refresh"
	tenantMediaAuthorityDeadlineTimeout = 25 * time.Second
	tenantMediaAuthorityDeadlineLease   = 35 * time.Second
)

type tenantMediaAuthorityDeadlineContextKey struct{}

func scheduleTenantMediaAuthorityRefresh(ctx context.Context, queries *commodoredb.Queries, payload *mediapb.TenantAuthority, version int64, refreshAt time.Time) error {
	if payload.GetLifecycle() == mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		rows, err := queries.ScheduleMediaAuthorityRefresh(ctx, commodoredb.ScheduleMediaAuthorityRefreshParams{
			SourceEventID: mediaAuthorityDeadlineEvent("tenant", payload.GetTenantId(), version), TenantID: payload.GetTenantId(), Reason: tenantMediaAuthorityDeadlineReason, NextAttemptAt: refreshAt,
		})
		if err != nil {
			return fmt.Errorf("schedule tenant authority renewal: %w", err)
		}
		if rows != 1 {
			return fmt.Errorf("schedule tenant authority renewal: rows=%d, want 1", rows)
		}
	}
	if deadline, ok := ctx.Value(tenantMediaAuthorityDeadlineContextKey{}).(bool); !ok || !deadline {
		return nil
	}
	// Dependent enumeration runs on the ordinary worker. Its obligation commits
	// with the parent so a superseded deadline job cannot lose the fanout on retry.
	rows, err := queries.InsertMediaAuthorityRefreshInbox(ctx, commodoredb.InsertMediaAuthorityRefreshInboxParams{
		SourceService: "commodore", SourceEventID: "tenant-deadline-fanout:" + payload.GetTenantId() + ":" + strconv.FormatInt(version, 10),
		TenantID: payload.GetTenantId(), Reason: "tenant_media_objects:deadline_refresh",
	})
	if err != nil {
		return fmt.Errorf("enqueue deadline tenant dependents: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("enqueue deadline tenant dependents: rows=%d, want 1", rows)
	}
	return nil
}

func (s *CommodoreServer) processTenantMediaAuthorityDeadlineRefreshBatch(ctx context.Context) {
	if !s.mediaAuthorityEnabled() {
		return
	}
	rows, err := commodoredb.New(s.db).ClaimTenantMediaAuthorityDeadlineRefresh(ctx, commodoredb.ClaimTenantMediaAuthorityDeadlineRefreshParams{
		LeaseMs: tenantMediaAuthorityDeadlineLease.Milliseconds(), BatchSize: mediaAuthorityRefreshBatch,
	})
	if err != nil {
		s.logger.WithError(err).Warn("Failed to claim tenant deadline authority refreshes")
		return
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(mediaAuthorityRefreshWorkers)
	for _, row := range rows {
		group.Go(func() error {
			rowCtx, cancel := context.WithTimeout(groupCtx, tenantMediaAuthorityDeadlineTimeout)
			defer cancel()
			s.processMediaAuthorityDeadlineRefreshRow(rowCtx, commodoredb.ClaimMediaAuthorityRefreshInboxRow(row))
			return nil
		})
	}
	if err := group.Wait(); err != nil && ctx.Err() == nil {
		s.logger.WithError(err).Warn("Tenant deadline authority refresh batch ended early")
	}
}
