ALTER TABLE commodore.media_cell_placement_capabilities
    ADD COLUMN IF NOT EXISTS activation_schema_version INTEGER NOT NULL DEFAULT 0;
