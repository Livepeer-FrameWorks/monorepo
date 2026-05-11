ALTER TABLE foghorn.node_update_state
    DROP CONSTRAINT IF EXISTS chk_foghorn_node_update_phase,
    DROP CONSTRAINT IF EXISTS chk_foghorn_node_update_expected_components_object,
    ADD CONSTRAINT chk_foghorn_node_update_phase CHECK (phase IN (
        'idle', 'cordoning', 'draining', 'drained', 'updating', 'updating_restore', 'warming', 'warming_restore', 'failed'
    )),
    ADD CONSTRAINT chk_foghorn_node_update_expected_components_object CHECK (jsonb_typeof(expected_components) = 'object');
