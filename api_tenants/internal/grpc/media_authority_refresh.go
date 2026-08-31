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
		message := callErr.Error()
		_, failErr := queries.FailMediaAuthorityRefresh(ctx, quartermasterdb.FailMediaAuthorityRefreshParams{
			NextAttemptAt: time.Now().UTC().Add(mediaAuthorityRefreshBackoff(row.Attempts)),
			LastError:     sql.NullString{String: message, Valid: true},
			ID:            row.ID,
		})
		if failErr != nil {
			return fmt.Errorf("record refresh failure: %w", failErr)
		}
		return nil
	}
	if _, err := queries.CompleteMediaAuthorityRefresh(ctx, row.ID); err != nil {
		return fmt.Errorf("complete refresh delivery: %w", err)
	}
	return nil
}
