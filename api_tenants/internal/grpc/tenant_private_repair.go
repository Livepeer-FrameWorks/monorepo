package grpc

import (
	"context"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

const tenantPrivateBaseURLRepairBatchSize = 100

func (s *QuartermasterServer) runTenantPrivateBaseURLRepair(ctx context.Context) {
	for {
		rows, err := s.repairTenantPrivateBaseURLBatch(ctx)
		if err != nil {
			s.logger.WithError(err).Warn("Tenant-private base_url repair failed; will retry on next Quartermaster start")
			return
		}
		if rows == 0 {
			return
		}
		s.logger.WithField("rows", rows).Info("Repaired tenant-private cluster base_url rows")
		if rows < tenantPrivateBaseURLRepairBatchSize {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *QuartermasterServer) repairTenantPrivateBaseURLBatch(ctx context.Context) (int64, error) {
	var rows int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var retryErr error
		rows, retryErr = quartermasterdb.New(s.db).RepairTenantPrivateBaseURLBatch(ctx, tenantPrivateBaseURLRepairBatchSize)
		return retryErr
	})
	if err != nil {
		return 0, err
	}
	return rows, nil
}
