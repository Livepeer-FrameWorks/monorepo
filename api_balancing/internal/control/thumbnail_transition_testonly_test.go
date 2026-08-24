//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"fmt"
)

// TransitionThumbnailStatus is a TEST-ONLY guarded status transition, used by the real-PG suites to drive an attempt
// through the pre-publication states (assigned → uploading → verifying, or → failed) without the full completion.
// It lives in a _test.go file so it is NOT part of the production API: production reaches the token-gated
// 'publishing'/'published' states exclusively through EnterThumbnailPublishingToken / PublishThumbnailAttemptToken,
// and this helper refuses those targets so no test can fabricate publication state outside the token contract.
func TransitionThumbnailStatus(ctx context.Context, dbh *sql.DB, attemptID, from, to string) (moved bool, err error) {
	if dbh == nil {
		return false, nil
	}
	if to == "publishing" || to == "published" {
		return false, fmt.Errorf("thumbnail transition to %q must use the token-fenced publication path, not TransitionThumbnailStatus", to)
	}
	res, execErr := dbh.ExecContext(ctx, `
		UPDATE foghorn.thumbnail_task_assignment
		   SET status = $3, updated_at = NOW()
		 WHERE attempt_id = $1 AND status = $2
	`, attemptID, from, to)
	if execErr != nil {
		return false, execErr
	}
	rows, rowsErr := res.RowsAffected()
	if rowsErr != nil {
		return false, rowsErr
	}
	return rows == 1, nil
}
