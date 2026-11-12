package grpc

import (
	"context"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
)

func (s *CommodoreServer) sweepSessionSecurityRetention(ctx context.Context) (int64, error) {
	queries := commodoredb.New(s.db)
	var deleted int64
	devices, err := queries.ListExpiredSessionDeviceKeys(ctx, authCodeRetentionBatch)
	if err != nil {
		return deleted, err
	}
	for _, key := range devices {
		count, deleteErr := queries.DeleteExpiredSessionDevice(ctx, commodoredb.DeleteExpiredSessionDeviceParams(key))
		if deleteErr != nil {
			return deleted, deleteErr
		}
		deleted += count
	}
	emails, err := queries.ListTerminalAccountEmailKeys(ctx, authCodeRetentionBatch)
	if err != nil {
		return deleted, err
	}
	for _, key := range emails {
		count, deleteErr := queries.DeleteTerminalAccountEmail(ctx, commodoredb.DeleteTerminalAccountEmailParams(key))
		if deleteErr != nil {
			return deleted, deleteErr
		}
		deleted += count
	}
	return deleted, nil
}

func (s *CommodoreServer) runSessionSecurityRetention(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		sweepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		deleted, err := s.sweepSessionSecurityRetention(sweepCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			s.logger.WithError(err).Warn("Security history retention sweep failed")
		} else if deleted > 0 {
			s.logger.WithField("deleted", deleted).Debug("Expired security history removed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
