package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"frameworks/api_balancing/internal/database/foghorndb"
)

// Multipart completion verifies size_bytes against storage before processing starts.
// The locked artifact still holds that source size until readiness replaces it.
func stampVodOriginal(ctx context.Context, q *foghorndb.Queries, tenantID, hash string) error {
	key, err := q.GetVodOriginalToStamp(ctx, foghorndb.GetVodOriginalToStampParams{TenantID: tenantID, ArtifactHash: hash})
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read original upload: %w", err)
	}
	activated, err := q.IsVodOriginalRetentionActivated(ctx)
	if err != nil {
		return fmt.Errorf("read original retention activation: %w", err)
	}
	if !activated {
		return errors.New("original retention is waiting for release postdeploy activation")
	}
	if key.SizeBytes <= 0 {
		return errors.New("original upload has no verified source size")
	}
	affected, err := q.StampVodOriginal(ctx, foghorndb.StampVodOriginalParams{TenantID: tenantID, ArtifactHash: hash, ObjectKey: key.S3Key.String, SizeBytes: key.SizeBytes})
	if err != nil {
		return err
	}
	if affected != 1 {
		return errors.New("original upload changed before retention was stamped")
	}
	return nil
}
