package grpc

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
)

const authCodeRetentionBatch int32 = 100

func (s *CommodoreServer) sweepAuthCodeRetention(ctx context.Context) (int64, error) {
	queries := commodoredb.New(s.db)
	var deleted int64
	expired, err := queries.ListExpiredAuthorizationCodeKeys(ctx, authCodeRetentionBatch)
	if err != nil {
		return deleted, fmt.Errorf("discover expired authorization codes: %w", err)
	}
	for _, key := range expired {
		count, deleteErr := queries.DeleteRetainedAuthorizationCode(ctx, commodoredb.DeleteRetainedAuthorizationCodeParams(key))
		if deleteErr != nil {
			return deleted, fmt.Errorf("delete expired authorization code: %w", deleteErr)
		}
		deleted += count
	}
	consumed, err := queries.ListConsumedAuthorizationCodeKeys(ctx, authCodeRetentionBatch)
	if err != nil {
		return deleted, fmt.Errorf("discover consumed authorization codes: %w", err)
	}
	for _, key := range consumed {
		count, deleteErr := queries.DeleteRetainedAuthorizationCode(ctx, commodoredb.DeleteRetainedAuthorizationCodeParams(key))
		if deleteErr != nil {
			return deleted, fmt.Errorf("delete consumed authorization code: %w", deleteErr)
		}
		deleted += count
	}
	devices, err := queries.ListExpiredDeviceCodeKeys(ctx, authCodeRetentionBatch)
	if err != nil {
		return deleted, fmt.Errorf("discover expired device codes: %w", err)
	}
	for _, key := range devices {
		count, deleteErr := queries.DeleteRetainedDeviceCode(ctx, commodoredb.DeleteRetainedDeviceCodeParams(key))
		if deleteErr != nil {
			return deleted, fmt.Errorf("delete expired device code: %w", deleteErr)
		}
		deleted += count
	}
	return deleted, nil
}

func (s *CommodoreServer) runAuthCodeRetention(ctx context.Context) {
	if s.db == nil {
		return
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		sweepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		deleted, err := s.sweepAuthCodeRetention(sweepCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			s.logger.WithError(err).Warn("Auth code retention sweep failed")
		} else if deleted > 0 {
			s.logger.WithField("deleted", deleted).Debug("Expired auth codes removed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
