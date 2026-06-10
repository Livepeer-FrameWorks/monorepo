-- Track the successor token issued by each rotation so after-grace reuse can
-- distinguish a lost rotation response (successor never used -> recover the
-- session) from genuine token theft (successor in use -> revoke the family).

ALTER TABLE commodore.refresh_tokens
  ADD COLUMN IF NOT EXISTS replaced_by UUID;
