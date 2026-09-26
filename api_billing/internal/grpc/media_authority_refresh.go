package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
)

const (
	mediaAuthorityRefreshPollInterval = time.Second
	mediaAuthorityRefreshLease        = 30 * time.Second
	mediaAuthorityRefreshBatchSize    = 8
	mediaAuthorityRefreshRPCTimeout   = 10 * time.Second
)

type mediaAuthorityRefreshClient interface {
	RequestMediaAuthorityRefresh(context.Context, string, string, string, string) (*commodorepb.RequestMediaAuthorityRefreshResponse, error)
}

func mediaAuthorityRefreshBackoff(attempts int32) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	shift := attempts - 1
	if shift > 7 {
		shift = 7
	}
	return time.Second * time.Duration(1<<shift)
}

func (s *PurserServer) runMediaAuthorityRefreshOutboxWorker(ctx context.Context) {
	client, ok := s.commodoreClient.(mediaAuthorityRefreshClient)
	if !ok || client == nil || s.db == nil {
		if s.metrics != nil && s.metrics.MediaAuthorityRefreshWorkerReady != nil {
			s.metrics.MediaAuthorityRefreshWorkerReady.WithLabelValues().Set(0)
		}
		if s.logger != nil {
			s.logger.Error("Media authority refresh worker disabled: database or Commodore refresh client unavailable")
		}
		return
	}
	if s.metrics != nil && s.metrics.MediaAuthorityRefreshWorkerReady != nil {
		s.metrics.MediaAuthorityRefreshWorkerReady.WithLabelValues().Set(1)
		defer s.metrics.MediaAuthorityRefreshWorkerReady.WithLabelValues().Set(0)
	}

	ticker := time.NewTicker(mediaAuthorityRefreshPollInterval)
	defer ticker.Stop()
	for {
		if err := s.deliverMediaAuthorityRefreshBatch(ctx, client); err != nil && ctx.Err() == nil {
			s.logger.WithError(err).Warn("Media authority refresh outbox delivery failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *PurserServer) deliverMediaAuthorityRefreshBatch(ctx context.Context, client mediaAuthorityRefreshClient) error {
	queries := purserdb.New(s.db)
	s.observeMediaAuthorityRefreshQueue(ctx, queries)
	rows, err := queries.ClaimMediaAuthorityRefreshBatch(ctx, purserdb.ClaimMediaAuthorityRefreshBatchParams{
		LeaseMs:   mediaAuthorityRefreshLease.Milliseconds(),
		BatchSize: mediaAuthorityRefreshBatchSize,
	})
	if err != nil {
		s.incMediaAuthorityRefreshFailure("claim")
		return fmt.Errorf("claim refresh batch: %w", err)
	}

	var group sync.WaitGroup
	errorsCh := make(chan error, len(rows))
	for _, row := range rows {
		row := row
		group.Add(1)
		go func() {
			defer group.Done()
			if rowErr := s.deliverMediaAuthorityRefreshRow(ctx, client, row); rowErr != nil {
				errorsCh <- rowErr
			}
		}()
	}
	group.Wait()
	close(errorsCh)
	for rowErr := range errorsCh {
		return rowErr
	}
	s.observeMediaAuthorityRefreshQueue(ctx, queries)
	return nil
}

func mediaAuthorityRefreshRequiresEntitlementReconcile(reason string) bool {
	switch reason {
	case "subscription_authority_changed", "subscription_entitlement_changed", "tier_entitlement_changed", "billing_tier_authority_changed":
		return true
	default:
		return false
	}
}

func (s *PurserServer) deliverMediaAuthorityRefreshRow(ctx context.Context, client mediaAuthorityRefreshClient, row purserdb.ClaimMediaAuthorityRefreshBatchRow) error {
	queries := purserdb.New(s.db)
	id, parseErr := uuid.Parse(row.ID)
	if parseErr != nil {
		return fmt.Errorf("parse refresh id %q: %w", row.ID, parseErr)
	}
	// Subscription/status changes create this row in the same owner
	// transaction. Converge Quartermaster before refreshing Commodore and only
	// complete the row after both obligations succeed, so a crash or remote
	// outage cannot leave cluster/DNS authority partially applied.
	if mediaAuthorityRefreshRequiresEntitlementReconcile(row.Reason) {
		if _, _, reconcileErr := s.reconcileCanonicalTierClusterAccess(ctx, row.TenantID); reconcileErr != nil {
			s.incMediaAuthorityRefreshFailure("entitlement_reconcile")
			return s.failMediaAuthorityRefresh(ctx, queries, id, row, fmt.Errorf("reconcile tenant entitlements: %w", reconcileErr))
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, mediaAuthorityRefreshRPCTimeout)
	_, callErr := client.RequestMediaAuthorityRefresh(callCtx, "purser", row.SourceEventID, row.TenantID, row.Reason)
	cancel()
	if callErr != nil {
		s.incMediaAuthorityRefreshFailure("commodore_delivery")
		return s.failMediaAuthorityRefresh(ctx, queries, id, row, callErr)
	}
	completed, err := queries.CompleteMediaAuthorityRefresh(ctx, purserdb.CompleteMediaAuthorityRefreshParams{ID: id, Revision: row.Revision})
	if err != nil {
		s.incMediaAuthorityRefreshFailure("complete")
		return fmt.Errorf("complete refresh delivery: %w", err)
	}
	if completed == 0 {
		released, releaseErr := queries.ReleaseSupersededMediaAuthorityRefresh(ctx, purserdb.ReleaseSupersededMediaAuthorityRefreshParams{ID: id, Revision: row.Revision})
		if releaseErr != nil {
			s.incMediaAuthorityRefreshFailure("superseded_release")
			return fmt.Errorf("release superseded refresh delivery: %w", releaseErr)
		}
		if released == 0 {
			s.incMediaAuthorityRefreshFailure("completion_fence_miss")
			return fmt.Errorf("refresh completion fence missed without a superseding revision")
		}
		s.incMediaAuthorityRefreshCompletion("superseded")
		return nil
	}
	s.incMediaAuthorityRefreshCompletion("delivered")
	return nil
}

func (s *PurserServer) incMediaAuthorityRefreshFailure(stage string) {
	if s.metrics != nil && s.metrics.MediaAuthorityRefreshFailures != nil {
		s.metrics.MediaAuthorityRefreshFailures.WithLabelValues(stage).Inc()
	}
}

func (s *PurserServer) incMediaAuthorityRefreshCompletion(outcome string) {
	if s.metrics != nil && s.metrics.MediaAuthorityRefreshCompletions != nil {
		s.metrics.MediaAuthorityRefreshCompletions.WithLabelValues(outcome).Inc()
	}
}

func (s *PurserServer) observeMediaAuthorityRefreshQueue(ctx context.Context, queries *purserdb.Queries) {
	if s.metrics == nil || (s.metrics.MediaAuthorityRefreshPending == nil && s.metrics.MediaAuthorityRefreshOldest == nil) {
		return
	}
	stats, err := queries.GetMediaAuthorityRefreshOutboxStats(ctx)
	if err != nil {
		s.incMediaAuthorityRefreshFailure("observe")
		if s.logger != nil && ctx.Err() == nil {
			s.logger.WithError(err).Warn("Media authority refresh queue stats read failed")
		}
		return
	}
	if s.metrics.MediaAuthorityRefreshPending != nil {
		s.metrics.MediaAuthorityRefreshPending.WithLabelValues().Set(float64(stats.PendingCount))
	}
	if s.metrics.MediaAuthorityRefreshOldest != nil {
		s.metrics.MediaAuthorityRefreshOldest.WithLabelValues().Set(stats.OldestPendingSeconds)
	}
}

func (s *PurserServer) failMediaAuthorityRefresh(ctx context.Context, queries *purserdb.Queries, id uuid.UUID, row purserdb.ClaimMediaAuthorityRefreshBatchRow, cause error) error {
	retryIn := mediaAuthorityRefreshBackoff(row.Attempts)
	// The row keeps last_error, but the delivery is retried in the background:
	// without this line a failure is visible only as a metric.
	if s.logger != nil {
		s.logger.WithError(cause).WithFields(logging.Fields{
			"tenant_id":       row.TenantID,
			"source_event_id": row.SourceEventID,
			"reason":          row.Reason,
			"attempts":        row.Attempts,
			"retry_in":        retryIn.String(),
		}).Warn("Media authority refresh delivery failed; retrying")
	}
	_, failErr := queries.FailMediaAuthorityRefresh(ctx, purserdb.FailMediaAuthorityRefreshParams{
		NextAttemptAt: time.Now().UTC().Add(retryIn),
		LastError:     sql.NullString{String: cause.Error(), Valid: true},
		ID:            id,
		Revision:      row.Revision,
	})
	if failErr != nil {
		s.incMediaAuthorityRefreshFailure("reschedule")
		return fmt.Errorf("record refresh failure: %w", failErr)
	}
	return nil
}
