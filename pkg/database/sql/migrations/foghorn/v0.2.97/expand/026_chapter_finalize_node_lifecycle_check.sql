-- Encode the chapter finalize-node assignment lifecycle as an invariant: finalize_node_id may be non-null ONLY
-- while state='finalizing'. Every transition out of finalizing (finalized/closed/failed) clears it in code, so
-- a retired node can never authorize a later transition. Added NOT VALID (non-blocking); v0.2.97/postdeploy/004
-- nulls stale assignments and VALIDATEs. IDEMPOTENT CONDITIONAL ADD — never drops the constraint, so a re-run
-- or rolling deploy leaves no window where the invariant is unenforced.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'chk_foghorn_dvr_chapters_finalize_node'
          AND conrelid = 'foghorn.dvr_chapters'::regclass
    ) THEN
        ALTER TABLE foghorn.dvr_chapters
            ADD CONSTRAINT chk_foghorn_dvr_chapters_finalize_node
                CHECK (finalize_node_id IS NULL OR state = 'finalizing') NOT VALID;
    END IF;
END $$;
