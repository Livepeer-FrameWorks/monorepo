-- v0.3.11: the time a Purser-invoiced monthly cluster subscription was last
-- activated. Its monthly fee is prorated over the time it was active in each
-- invoice period, and a reactivated subscription is active again only from its
-- reactivation. Rows without it were activated when created.

ALTER TABLE purser.cluster_subscriptions
    ADD COLUMN IF NOT EXISTS activated_at TIMESTAMPTZ;
