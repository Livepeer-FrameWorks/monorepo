CREATE TABLE IF NOT EXISTS commodore.media_authority_compile_fences (
    scope_key VARCHAR(320) PRIMARY KEY,
    generation BIGINT NOT NULL CHECK (generation > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
