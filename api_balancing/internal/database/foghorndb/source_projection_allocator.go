package foghorndb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// AllocateSourceProjectionRevisionAfter is the rare repair allocator. It
// advances the same per-stream counter used by normal source transitions and
// can atomically jump past a Redis watermark recovered after a DB restore.
func AllocateSourceProjectionRevisionAfter(ctx context.Context, db *sql.DB, tenantID, internalName string, minimumRevision int64) (int64, error) {
	if db == nil {
		return 0, errors.New("allocate source projection repair revision: no database configured")
	}
	if tenantID == "" || internalName == "" {
		return 0, errors.New("allocate source projection repair revision: missing stream identity")
	}
	if minimumRevision < 0 || minimumRevision >= 9007199254740991 {
		return 0, fmt.Errorf("allocate source projection repair revision: invalid minimum %d", minimumRevision)
	}
	var revision int64
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return db.QueryRowContext(ctx, `
INSERT INTO foghorn.source_projection_revision_counter (tenant_id, stream_internal_name, value)
VALUES ($1::uuid, $2, GREATEST(4503599627370497::bigint, $3::bigint + 1))
ON CONFLICT (tenant_id, stream_internal_name) DO UPDATE
SET value = GREATEST(foghorn.source_projection_revision_counter.value + 1, $3::bigint + 1)
RETURNING value`, tenantID, internalName, minimumRevision).Scan(&revision)
	})
	if err != nil {
		return 0, fmt.Errorf("allocate source projection repair revision: %w", err)
	}
	return revision, nil
}
