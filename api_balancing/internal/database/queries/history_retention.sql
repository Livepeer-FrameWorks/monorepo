-- name: PurgeEndedIngestSessions :execrows
-- An ended generation stays while a recording still references it (the artifacts FK) or its
-- admission obligation is still pending; every trigger path that resolves a generation by id
-- runs within seconds of its end, far inside the retention horizon.
WITH old_ended AS (
    SELECT s.id
    FROM foghorn.ingest_sessions s
    WHERE s.ended_at IS NOT NULL
      AND s.ended_at < NOW() - sqlc.arg(retention_seconds)::bigint * INTERVAL '1 second'
      AND NOT EXISTS (
          SELECT 1 FROM foghorn.artifacts a
          WHERE a.ingest_generation = s.id
      )
      AND NOT EXISTS (
          SELECT 1 FROM foghorn.ingest_admission_effects e
          WHERE e.source_generation = s.id
            AND e.state IN ('pending', 'pending_v2')
      )
    ORDER BY s.ended_at
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE OF s SKIP LOCKED
)
DELETE FROM foghorn.ingest_sessions s
USING old_ended o
WHERE s.id = o.id;

-- name: PurgeTerminalProcessingJobs :execrows
-- A terminal job stays while another job names it as parent, and a clip's job stays while the
-- clip's creation outcome is unconsumed: the Commodore convergence sweep reads the fulfilled range
-- from its source params until then.
WITH old_terminal AS (
    SELECT j.job_id
    FROM foghorn.processing_jobs j
    WHERE j.status IN ('completed', 'failed')
      AND j.updated_at < NOW() - sqlc.arg(retention_seconds)::bigint * INTERVAL '1 second'
      AND NOT EXISTS (
          SELECT 1 FROM foghorn.processing_jobs child
          WHERE child.parent_job_id = j.job_id
      )
      AND NOT EXISTS (
          SELECT 1 FROM foghorn.artifact_creation_commands c
          WHERE c.artifact_hash = j.artifact_hash
            AND c.consumed_at IS NULL
      )
    ORDER BY j.updated_at
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE OF j SKIP LOCKED
)
DELETE FROM foghorn.processing_jobs j
USING old_terminal o
WHERE j.job_id = o.job_id;

-- name: PurgeCompletedArtifactEvents :execrows
WITH old_completed AS (
    SELECT e.id
    FROM foghorn.artifact_event_outbox e
    WHERE e.completed_at IS NOT NULL
      AND e.completed_at < NOW() - sqlc.arg(retention_seconds)::bigint * INTERVAL '1 second'
    ORDER BY e.completed_at
    LIMIT sqlc.arg(batch_limit)
    FOR UPDATE OF e SKIP LOCKED
)
DELETE FROM foghorn.artifact_event_outbox e
USING old_completed o
WHERE e.id = o.id;
