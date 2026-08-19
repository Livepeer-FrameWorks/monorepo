-- The v0.3 writer identifies Stripe meter events by permanent invoice line.
-- Remove the v0.2 period-level key after the writer transition so distinct
-- dimension buckets of one meter are not collapsed.

DROP INDEX IF EXISTS purser.uq_stripe_meter_events_outbox_period;
