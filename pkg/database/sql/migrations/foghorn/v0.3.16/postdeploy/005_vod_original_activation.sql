-- Release postdeploy follows service upgrades and retirement of stale placements.
INSERT INTO foghorn.vod_original_retention_activation (singleton, activated_at)
VALUES (true, NOW())
ON CONFLICT (singleton) DO UPDATE
SET activated_at = COALESCE(foghorn.vod_original_retention_activation.activated_at, EXCLUDED.activated_at);
