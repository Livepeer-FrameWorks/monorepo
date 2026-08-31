package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
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
		return
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
	rows, err := queries.ClaimMediaAuthorityRefreshBatch(ctx, purserdb.ClaimMediaAuthorityRefreshBatchParams{
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
	return nil
}

func (s *PurserServer) deliverMediaAuthorityRefreshRow(ctx context.Context, client mediaAuthorityRefreshClient, row purserdb.ClaimMediaAuthorityRefreshBatchRow) error {
	queries := purserdb.New(s.db)
	id, parseErr := uuid.Parse(row.ID)
	if parseErr != nil {
		return fmt.Errorf("parse refresh id %q: %w", row.ID, parseErr)
	}
	callCtx, cancel := context.WithTimeout(ctx, mediaAuthorityRefreshRPCTimeout)
	_, callErr := client.RequestMediaAuthorityRefresh(callCtx, "purser", row.SourceEventID, row.TenantID, row.Reason)
	cancel()
	if callErr != nil {
		message := callErr.Error()
		_, failErr := queries.FailMediaAuthorityRefresh(ctx, purserdb.FailMediaAuthorityRefreshParams{
			NextAttemptAt: time.Now().UTC().Add(mediaAuthorityRefreshBackoff(row.Attempts)),
			LastError:     sql.NullString{String: message, Valid: true},
			ID:            id,
			Revision:      row.Revision,
		})
		if failErr != nil {
			return fmt.Errorf("record refresh failure: %w", failErr)
		}
		return nil
	}
	if _, err := queries.CompleteMediaAuthorityRefresh(ctx, purserdb.CompleteMediaAuthorityRefreshParams{ID: id, Revision: row.Revision}); err != nil {
		return fmt.Errorf("complete refresh delivery: %w", err)
	}
	return nil
}
