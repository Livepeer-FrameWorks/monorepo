package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
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

func (s *QuartermasterServer) runMediaAuthorityRefreshOutboxWorker(ctx context.Context) {
	if s.mediaAuthorityRefreshClient == nil || s.db == nil {
		return
	}

	ticker := time.NewTicker(mediaAuthorityRefreshPollInterval)
	defer ticker.Stop()
	for {
		if err := s.deliverMediaAuthorityRefreshBatch(ctx); err != nil && ctx.Err() == nil {
			s.logger.WithError(err).Warn("Media authority refresh outbox delivery failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *QuartermasterServer) deliverMediaAuthorityRefreshBatch(ctx context.Context) error {
	queries := quartermasterdb.New(s.db)
	s.observeMediaAuthorityRefreshQueue(ctx, queries)
	rows, err := queries.ClaimMediaAuthorityRefreshBatch(ctx, quartermasterdb.ClaimMediaAuthorityRefreshBatchParams{
		LeaseMs:   mediaAuthorityRefreshLease.Milliseconds(),
		BatchSize: mediaAuthorityRefreshBatchSize,
	})
	if err != nil {
		return fmt.Errorf("claim refresh batch: %w", err)
	}

	var group sync.WaitGroup
	errorsCh := make(chan error, len(rows))
	for _, row := range rows {
		row := row
		group.Add(1)
		go func() {
			defer group.Done()
			if rowErr := s.deliverMediaAuthorityRefreshRow(ctx, row); rowErr != nil {
				errorsCh <- rowErr
			}
		}()
	}
	group.Wait()
	close(errorsCh)
	for rowErr := range errorsCh {
		return rowErr
	}
	return nil
}

func (s *QuartermasterServer) deliverMediaAuthorityRefreshRow(ctx context.Context, row quartermasterdb.ClaimMediaAuthorityRefreshBatchRow) error {
	queries := quartermasterdb.New(s.db)
	callCtx, cancel := context.WithTimeout(ctx, mediaAuthorityRefreshRPCTimeout)
	_, callErr := s.mediaAuthorityRefreshClient.RequestMediaAuthorityRefresh(callCtx, "quartermaster", row.SourceEventID, row.TenantID, row.Reason)
	cancel()
	if callErr != nil {
		s.incMediaAuthorityRefreshFailure("commodore_delivery")
		message := callErr.Error()
		_, failErr := queries.FailMediaAuthorityRefresh(ctx, quartermasterdb.FailMediaAuthorityRefreshParams{
			NextAttemptAt: time.Now().UTC().Add(mediaAuthorityRefreshBackoff(row.Attempts)),
			LastError:     sql.NullString{String: message, Valid: true},
			ID:            row.ID,
			Revision:      row.Revision,
		})
		if failErr != nil {
			return fmt.Errorf("record refresh failure: %w", failErr)
		}
		return nil
	}
	completed, err := queries.CompleteMediaAuthorityRefresh(ctx, quartermasterdb.CompleteMediaAuthorityRefreshParams{ID: row.ID, Revision: row.Revision})
	if err != nil {
		s.incMediaAuthorityRefreshFailure("complete")
		return fmt.Errorf("complete refresh delivery: %w", err)
	}
	if completed == 1 {
		return nil
	}
	// A change folded into this row while it was being delivered. The row is
	// already pending at a newer revision; releasing it makes that revision
	// claimable now instead of after the delivery lease runs out.
	released, err := queries.ReleaseSupersededMediaAuthorityRefresh(ctx, quartermasterdb.ReleaseSupersededMediaAuthorityRefreshParams{ID: row.ID, Revision: row.Revision})
	if err != nil {
		s.incMediaAuthorityRefreshFailure("superseded_release")
		return fmt.Errorf("release superseded refresh delivery: %w", err)
	}
	if released == 0 {
		s.incMediaAuthorityRefreshFailure("completion_fence_miss")
		return fmt.Errorf("refresh completion fence missed without a superseding revision")
	}
	return nil
}

func (s *QuartermasterServer) incMediaAuthorityRefreshFailure(stage string) {
	if s.metrics != nil && s.metrics.MediaAuthorityRefreshFailures != nil {
		s.metrics.MediaAuthorityRefreshFailures.WithLabelValues(stage).Inc()
	}
}

func (s *QuartermasterServer) observeMediaAuthorityRefreshQueue(ctx context.Context, queries *quartermasterdb.Queries) {
	if s.metrics == nil || s.metrics.MediaAuthorityRefreshPending == nil || s.metrics.MediaAuthorityRefreshOldest == nil {
		return
	}
	stats, err := queries.GetMediaAuthorityRefreshOutboxStats(ctx)
	if err != nil {
		s.incMediaAuthorityRefreshFailure("observe")
		return
	}
	s.metrics.MediaAuthorityRefreshPending.WithLabelValues().Set(float64(stats.PendingCount))
	s.metrics.MediaAuthorityRefreshOldest.WithLabelValues().Set(stats.OldestPendingSeconds)
}
