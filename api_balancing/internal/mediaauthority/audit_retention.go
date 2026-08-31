package mediaauthority

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	auditRetention      = 30 * 24 * time.Hour
	auditRetentionBatch = 1000
	auditPruneInterval  = 6 * time.Hour
)

// RunAuditRetention bounds the local verification/apply diagnostic ledger.
// Decisions remain in the signed authority tables; this removes only old audit
// observations in small batches so cleanup cannot stall media requests.
func (s *Store) RunAuditRetention(ctx context.Context, logger logging.Logger) {
	if s == nil || s.db == nil {
		return
	}
	prune := func() {
		rows, err := s.pruneApplyAudit(ctx)
		if err != nil {
			if ctx.Err() == nil {
				logger.WithError(err).Warn("Failed to prune media authority apply audit")
			}
			return
		}
		if rows > 0 {
			logger.WithField("rows", rows).Info("Pruned expired media authority apply audit rows")
		}
	}
	prune()
	ticker := time.NewTicker(auditPruneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

func (s *Store) pruneApplyAudit(ctx context.Context) (int64, error) {
	return foghorndb.New(s.db).PruneMediaAuthorityApplyAudit(ctx, foghorndb.PruneMediaAuthorityApplyAuditParams{
		RetentionSeconds: int64(auditRetention / time.Second), BatchSize: auditRetentionBatch,
	})
}
