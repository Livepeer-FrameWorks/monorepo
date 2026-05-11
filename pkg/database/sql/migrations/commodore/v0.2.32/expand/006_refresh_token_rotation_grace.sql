ALTER TABLE commodore.refresh_tokens
    ADD COLUMN IF NOT EXISTS rotated_at TIMESTAMP;
