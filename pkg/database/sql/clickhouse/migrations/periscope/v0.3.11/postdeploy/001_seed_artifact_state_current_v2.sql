-- Seeds artifact_state_current_v2 with the latest artifact_state_current row
-- of every artifact the domain projection has not written yet. The seeded
-- version, (updated_at_ms << 12), is on the scale of an unstamped domain
-- event's UUIDv7 order, so any domain event written after the transition the
-- seed row describes outranks it. Artifacts already present in v2 keep their
-- domain rows. content_type (clip, dvr, vod) and stage keep their v1 values:
-- the domain projection writes the same vocabulary.
INSERT INTO periscope.artifact_state_current_v2
    (tenant_id, artifact_id, content_type, stream_id, stage, event_type, failure_reason,
     filename, size_bytes, duration_ms, updated_at, event_id, aggregate_version, version)
SELECT
    tenant_id,
    request_id AS artifact_id,
    content_type,
    stream_id,
    stage,
    '' AS event_type,
    '' AS failure_reason,
    filename,
    size_bytes,
    CAST(NULL AS Nullable(Int64)) AS duration_ms,
    updated_at,
    '' AS event_id,
    toUInt64(0) AS aggregate_version,
    bitShiftLeft(toUInt64(toUnixTimestamp64Milli(updated_at)), 12) AS version
FROM periscope.artifact_state_current FINAL
WHERE request_id != ''
  AND (tenant_id, request_id) NOT IN (
      SELECT tenant_id, artifact_id FROM periscope.artifact_state_current_v2
  );
