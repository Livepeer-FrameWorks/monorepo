-- Platform operator grant: marks a user as platform staff. Carried into the
-- session token as the RFC 9068 platform_operator role and enforced by the
-- authz PDP for /admin and Mist break-glass. Distinct from tenant role.

ALTER TABLE commodore.users
  ADD COLUMN IF NOT EXISTS platform_operator BOOLEAN NOT NULL DEFAULT false;
