-- Record the physical backend each freeze-garbage object was written to on the durable cleanup + publication-ledger
-- rows, so the cleanup worker deletes ONLY when the recorded backend_id exactly matches the cell's current store and
-- fails closed otherwise (an empty/legacy id, or a mismatch after a forbidden repoint → the row is retained, never a
-- guessed-store delete). Fresh rows attribute it at enqueue; legacy rows are adopted once at boot.
ALTER TABLE foghorn.staging_cleanup_queue
    ADD COLUMN IF NOT EXISTS backend_id TEXT;

ALTER TABLE foghorn.freeze_publication_ledger
    ADD COLUMN IF NOT EXISTS backend_id TEXT;
