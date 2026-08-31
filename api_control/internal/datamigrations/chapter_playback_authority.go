package datamigrations

import (
	"context"
	"fmt"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

const ChapterPlaybackAuthorityID = "commodore_chapter_playback_authority_v0_3_0"

// ChapterPlaybackAuthorityBatchSQL is exported so the real-engine migration
// contract executes the exact statement used by the background worker.
const ChapterPlaybackAuthorityBatchSQL = `
WITH candidates AS (
    SELECT chapter_asset.id,
		   CASE WHEN parent_dvr.id IS NOT NULL AND parent_dvr.playback_authority_ready
		        THEN parent_dvr.requires_auth ELSE COALESCE(parent_stream.requires_auth, TRUE) END AS requires_auth,
		   CASE WHEN parent_dvr.id IS NOT NULL AND parent_dvr.playback_authority_ready THEN parent_dvr.playback_policy
		        WHEN parent_stream.id IS NOT NULL THEN parent_stream.playback_policy ELSE NULL END AS playback_policy,
		   CASE WHEN parent_dvr.id IS NOT NULL AND parent_dvr.playback_authority_ready THEN parent_dvr.playback_webhook_secret_enc
		        WHEN parent_stream.id IS NOT NULL THEN parent_stream.playback_webhook_secret_enc ELSE NULL END AS playback_webhook_secret_enc
    FROM commodore.vod_assets AS chapter_asset
	LEFT JOIN commodore.dvr_chapter_playback AS chapter_identity
	  ON chapter_identity.tenant_id = chapter_asset.tenant_id
	 AND chapter_identity.artifact_hash = chapter_asset.vod_hash
	LEFT JOIN commodore.dvr_recordings AS parent_dvr
	  ON parent_dvr.tenant_id = chapter_asset.tenant_id
	 AND parent_dvr.dvr_hash = chapter_identity.dvr_hash
	LEFT JOIN commodore.streams AS parent_stream
	  ON parent_stream.tenant_id = chapter_asset.tenant_id
	 AND parent_stream.id = chapter_asset.stream_id
    WHERE chapter_asset.origin_type = 'dvr_chapter'
	  AND NOT EXISTS (
	      SELECT 1 FROM commodore.artifact_catalog_tombstones AS tombstone
	      WHERE tombstone.tenant_id = chapter_asset.tenant_id
	        AND tombstone.kind = 'vod'
	        AND tombstone.artifact_hash = chapter_asset.vod_hash
	  )
), batch AS (
	SELECT chapter_asset.id, candidates.requires_auth, candidates.playback_policy,
	       candidates.playback_webhook_secret_enc,
	       chapter_asset.requires_auth AS original_requires_auth,
	       chapter_asset.playback_policy AS original_playback_policy,
	       chapter_asset.playback_webhook_secret_enc AS original_webhook_secret
	FROM candidates
	JOIN commodore.vod_assets AS chapter_asset ON chapter_asset.id = candidates.id
	WHERE chapter_asset.requires_auth IS DISTINCT FROM candidates.requires_auth
	   OR chapter_asset.playback_policy IS DISTINCT FROM candidates.playback_policy
	   OR chapter_asset.playback_webhook_secret_enc IS DISTINCT FROM candidates.playback_webhook_secret_enc
    ORDER BY chapter_asset.id
    LIMIT $1
    FOR UPDATE OF chapter_asset SKIP LOCKED
), updated AS (
    UPDATE commodore.vod_assets AS chapter_asset
    SET requires_auth = batch.requires_auth,
        playback_policy = batch.playback_policy,
        playback_webhook_secret_enc = batch.playback_webhook_secret_enc
    FROM batch
    WHERE chapter_asset.id = batch.id
      AND chapter_asset.requires_auth IS NOT DISTINCT FROM batch.original_requires_auth
      AND chapter_asset.playback_policy IS NOT DISTINCT FROM batch.original_playback_policy
      AND chapter_asset.playback_webhook_secret_enc IS NOT DISTINCT FROM batch.original_webhook_secret
    RETURNING chapter_asset.id
)
SELECT (SELECT COUNT(*) FROM batch),
       (SELECT COUNT(*) FROM updated),
	   0::bigint`

func registerChapterPlaybackAuthority() {
	datamigrate.Register(datamigrate.Migration{
		ID: ChapterPlaybackAuthorityID, Service: "commodore", IntroducedIn: "v0.3.0",
		RequiredBeforePhase: "postdeploy",
		Description:         "snapshot parent DVR playback authority onto existing chapter VOD records",
		DependsOn:           []string{DVRPlaybackAuthorityID},
		Run:                 runChapterPlaybackAuthority,
		Verify:              verifyChapterPlaybackAuthority,
	})
}

func runChapterPlaybackAuthority(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 1000
	}
	var scanned, changed, skipped int64
	if err := db.QueryRowContext(ctx, ChapterPlaybackAuthorityBatchSQL, batchSize).Scan(&scanned, &changed, &skipped); err != nil {
		return datamigrate.Progress{}, fmt.Errorf("backfill chapter playback authority: %w", err)
	}
	return datamigrate.Progress{Scanned: scanned, Changed: changed, Skipped: skipped, Done: scanned < int64(batchSize)}, nil
}

func verifyChapterPlaybackAuthority(ctx context.Context, db datamigrate.DB) error {
	var remaining int64
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM commodore.vod_assets AS chapter_asset
LEFT JOIN commodore.dvr_chapter_playback AS chapter_identity
	ON chapter_identity.tenant_id = chapter_asset.tenant_id
 AND chapter_identity.artifact_hash = chapter_asset.vod_hash
LEFT JOIN commodore.dvr_recordings AS parent_dvr
	ON parent_dvr.tenant_id = chapter_asset.tenant_id
 AND parent_dvr.dvr_hash = chapter_identity.dvr_hash
LEFT JOIN commodore.streams AS parent_stream
	ON parent_stream.tenant_id = chapter_asset.tenant_id
 AND parent_stream.id = chapter_asset.stream_id
WHERE chapter_asset.origin_type = 'dvr_chapter'
  AND NOT EXISTS (
      SELECT 1 FROM commodore.artifact_catalog_tombstones AS tombstone
      WHERE tombstone.tenant_id = chapter_asset.tenant_id
        AND tombstone.kind = 'vod'
        AND tombstone.artifact_hash = chapter_asset.vod_hash
  )
  AND (chapter_asset.requires_auth IS DISTINCT FROM
      CASE WHEN parent_dvr.id IS NOT NULL AND parent_dvr.playback_authority_ready
	       THEN parent_dvr.requires_auth ELSE COALESCE(parent_stream.requires_auth, TRUE) END
   OR chapter_asset.playback_policy IS DISTINCT FROM
      CASE WHEN parent_dvr.id IS NOT NULL AND parent_dvr.playback_authority_ready THEN parent_dvr.playback_policy
		   WHEN parent_stream.id IS NOT NULL THEN parent_stream.playback_policy ELSE NULL END
   OR chapter_asset.playback_webhook_secret_enc IS DISTINCT FROM
      CASE WHEN parent_dvr.id IS NOT NULL AND parent_dvr.playback_authority_ready THEN parent_dvr.playback_webhook_secret_enc
		   WHEN parent_stream.id IS NOT NULL THEN parent_stream.playback_webhook_secret_enc ELSE NULL END
  )`).Scan(&remaining); err != nil {
		return fmt.Errorf("verify chapter playback authority: %w", err)
	}
	if remaining != 0 {
		return fmt.Errorf("chapter playback authority has %d rows remaining", remaining)
	}
	return nil
}
