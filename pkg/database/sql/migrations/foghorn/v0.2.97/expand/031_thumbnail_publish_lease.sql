-- Publication lease: a single-flight fence a completion acquires BEFORE it HEAD-verifies + promotes an attempt's
-- staging objects to their immutable version keys. Without it two problems exist: (1) two concurrent completions
-- of the SAME attempt both copy to the same version key (CopySourceIfMatch fences only the source ETag, not the
-- destination), so a late copy can mutate an already-published version; and (2) the recovery reconciler can expire
-- + fail + sweep an attempt while a slow completion is mid-promote, orphaning the just-promoted object. The lease
-- makes the HEAD/promote single-flight per attempt, and the recovery fail-sweep HONORS it (it will not fail an
-- attempt whose publication lease is still live), so a promotion in flight is never swept out from under. The
-- lease auto-expires so a crashed completion's attempt is reclaimed. Schema source of truth: foghorn.sql.
ALTER TABLE foghorn.thumbnail_task_assignment
    ADD COLUMN IF NOT EXISTS publish_leased_until TIMESTAMPTZ;
