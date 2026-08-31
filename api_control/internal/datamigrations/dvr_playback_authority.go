package datamigrations

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

const DVRPlaybackAuthorityID = "commodore_dvr_playback_authority_v0_3_0"

const dvrPlaybackAuthorityBatchSQL = `
WITH batch AS (
    SELECT dvr.id, dvr.stream_id,
           dvr.requires_auth AS original_requires_auth,
           dvr.playback_policy AS original_playback_policy,
           dvr.playback_webhook_secret_enc AS original_webhook_secret,
           dvr.playback_authority_ready AS original_authority_ready
    FROM commodore.dvr_recordings AS dvr
    LEFT JOIN commodore.streams AS stream ON stream.id = dvr.stream_id
    WHERE NOT dvr.playback_authority_ready
       OR (stream.id IS NOT NULL AND (
              dvr.requires_auth IS DISTINCT FROM stream.requires_auth
           OR dvr.playback_policy IS DISTINCT FROM stream.playback_policy
           OR dvr.playback_webhook_secret_enc IS DISTINCT FROM stream.playback_webhook_secret_enc
          ))
       OR (stream.id IS NULL AND (
              dvr.requires_auth IS DISTINCT FROM TRUE
           OR dvr.playback_policy IS NOT NULL
           OR dvr.playback_webhook_secret_enc IS NOT NULL
          ))
    ORDER BY dvr.id
    LIMIT $1
    FOR UPDATE OF dvr SKIP LOCKED
), updated AS (
    UPDATE commodore.dvr_recordings AS dvr
    SET requires_auth = COALESCE(stream.requires_auth, TRUE),
        playback_policy = CASE WHEN stream.id IS NULL THEN NULL ELSE stream.playback_policy END,
        playback_webhook_secret_enc = CASE WHEN stream.id IS NULL THEN NULL ELSE stream.playback_webhook_secret_enc END,
        playback_authority_ready = TRUE
    FROM batch
    LEFT JOIN commodore.streams AS stream ON stream.id = batch.stream_id
    WHERE dvr.id = batch.id
      AND dvr.requires_auth IS NOT DISTINCT FROM batch.original_requires_auth
      AND dvr.playback_policy IS NOT DISTINCT FROM batch.original_playback_policy
      AND dvr.playback_webhook_secret_enc IS NOT DISTINCT FROM batch.original_webhook_secret
      AND dvr.playback_authority_ready IS NOT DISTINCT FROM batch.original_authority_ready
    RETURNING dvr.id
)
SELECT (SELECT COUNT(*) FROM batch), (SELECT COUNT(*) FROM updated)`

func Register() {
	datamigrate.Register(datamigrate.Migration{
		ID: DVRPlaybackAuthorityID, Service: "commodore", IntroducedIn: "v0.3.0",
		RequiredBeforePhase: "postdeploy",
		Description:         "snapshot parent stream playback authority onto existing DVR records",
		Run:                 runDVRPlaybackAuthority,
		Verify:              verifyDVRPlaybackAuthority,
	})
	registerChapterPlaybackAuthority()
}

func runDVRPlaybackAuthority(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	var scanned, changed int64
	if err := db.QueryRowContext(ctx, dvrPlaybackAuthorityBatchSQL, batchSize).Scan(&scanned, &changed); err != nil {
		return datamigrate.Progress{}, fmt.Errorf("backfill DVR playback authority: %w", err)
	}
	return datamigrate.Progress{Scanned: scanned, Changed: changed, Done: scanned < int64(batchSize)}, nil
}

func verifyDVRPlaybackAuthority(ctx context.Context, db datamigrate.DB) error {
	var remaining int64
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM commodore.dvr_recordings AS dvr
LEFT JOIN commodore.streams AS stream ON stream.id = dvr.stream_id
WHERE NOT dvr.playback_authority_ready
   OR (stream.id IS NOT NULL AND (
          dvr.requires_auth IS DISTINCT FROM stream.requires_auth
       OR dvr.playback_policy IS DISTINCT FROM stream.playback_policy
       OR dvr.playback_webhook_secret_enc IS DISTINCT FROM stream.playback_webhook_secret_enc
      ))
   OR (stream.id IS NULL AND (
          dvr.requires_auth IS DISTINCT FROM TRUE
       OR dvr.playback_policy IS NOT NULL
       OR dvr.playback_webhook_secret_enc IS NOT NULL
      ))`).Scan(&remaining); err != nil {
		return fmt.Errorf("verify DVR playback authority: %w", err)
	}
	if remaining != 0 {
		return fmt.Errorf("DVR playback authority has %d rows remaining", remaining)
	}
	return nil
}
