package grpc

import (
	"context"
	"database/sql"
	"time"

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
	var result sql.Result
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var execErr error
		result, execErr = s.db.ExecContext(ctx, `
			WITH repair AS (
				SELECT c.id, control.base_url
				FROM quartermaster.infrastructure_clusters c
				JOIN quartermaster.infrastructure_clusters control
				  ON control.cluster_id = c.control_cell_id
				WHERE c.cluster_class = 'tenant_private'
				  AND NULLIF(c.base_url, '') IS NULL
				  AND control.cluster_class = 'platform_official'
				  AND NULLIF(control.base_url, '') IS NOT NULL
				ORDER BY c.created_at ASC, c.id ASC
				LIMIT $1
			)
			UPDATE quartermaster.infrastructure_clusters c
			SET base_url = repair.base_url,
			    updated_at = NOW()
			FROM repair
			WHERE c.id = repair.id
		`, tenantPrivateBaseURLRepairBatchSize)
		return execErr
	})
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
