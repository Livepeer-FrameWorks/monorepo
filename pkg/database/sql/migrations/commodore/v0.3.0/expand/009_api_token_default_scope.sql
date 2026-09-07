-- Keep direct/bootstrap inserts aligned with Commodore's exact API-token
-- permission model. Existing token grants are intentionally not widened or
-- rewritten; operators must revoke and reissue coarse-scoped tokens.
ALTER TABLE commodore.api_tokens
    ALTER COLUMN permissions SET DEFAULT ARRAY['streams:read']::text[];
