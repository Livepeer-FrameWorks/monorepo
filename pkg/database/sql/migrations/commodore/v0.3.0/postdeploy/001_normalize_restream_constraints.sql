WITH suppress AS MATERIALIZED (
    SELECT set_config('frameworks.suppress_media_authority_refresh', 'on', true) AS enabled
)
UPDATE commodore.push_targets
SET platform = CASE
        WHEN lower(btrim(COALESCE(platform, ''))) IN ('twitch', 'youtube', 'facebook', 'kick', 'x', 'custom')
            THEN lower(btrim(platform))
        ELSE 'custom'
    END,
    status = CASE
        WHEN lower(btrim(COALESCE(status, ''))) IN ('pending', 'pushing', 'retrying', 'stopping', 'idle', 'failed')
            THEN lower(btrim(status))
        ELSE 'idle'
    END,
    reason_code = CASE
        WHEN lower(btrim(COALESCE(reason_code, ''))) IN (
            'unspecified', 'connected', 'completed', 'destination_rejected',
            'network_error', 'process_error', 'capacity_exhausted',
            'configuration_error', 'edge_upgrade_required', 'stopped'
        ) THEN lower(btrim(reason_code))
        ELSE 'unspecified'
    END
FROM suppress
WHERE suppress.enabled = 'on'
  AND (
      platform IS DISTINCT FROM CASE
          WHEN lower(btrim(COALESCE(platform, ''))) IN ('twitch', 'youtube', 'facebook', 'kick', 'x', 'custom')
              THEN lower(btrim(platform))
          ELSE 'custom'
      END
      OR status IS DISTINCT FROM CASE
          WHEN lower(btrim(COALESCE(status, ''))) IN ('pending', 'pushing', 'retrying', 'stopping', 'idle', 'failed')
              THEN lower(btrim(status))
          ELSE 'idle'
      END
      OR reason_code IS DISTINCT FROM CASE
          WHEN lower(btrim(COALESCE(reason_code, ''))) IN (
              'unspecified', 'connected', 'completed', 'destination_rejected',
              'network_error', 'process_error', 'capacity_exhausted',
              'configuration_error', 'edge_upgrade_required', 'stopped'
          ) THEN lower(btrim(reason_code))
          ELSE 'unspecified'
      END
  );
