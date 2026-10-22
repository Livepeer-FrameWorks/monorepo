-- name: ListActivePaidBillingTiersForStripe :many
SELECT id, tier_name, display_name, COALESCE(description, '') AS description,
       base_price::text AS base_price, currency,
       stripe_product_id, stripe_price_id_monthly
FROM purser.billing_tiers
WHERE base_price > 0 AND COALESCE(is_active, true) = true
ORDER BY tier_name;

-- Records the tier's Stripe IDs only while the row still holds the IDs the
-- sync read, so a replica whose sync another replica already recorded changes
-- nothing and affects no row.
-- name: UpdateBillingTierStripeIDs :execrows
UPDATE purser.billing_tiers
SET stripe_product_id = sqlc.arg(stripe_product_id),
    stripe_price_id_monthly = sqlc.arg(stripe_price_id_monthly),
    updated_at = NOW()
WHERE id = sqlc.arg(id)
  AND stripe_product_id IS NOT DISTINCT FROM sqlc.narg(read_stripe_product_id)
  AND stripe_price_id_monthly IS NOT DISTINCT FROM sqlc.narg(read_stripe_price_id_monthly);

-- Whether any tier or cluster price records the Stripe price, so a sync
-- only deactivates a price nothing records.
-- name: IsStripePriceRecorded :one
SELECT EXISTS (
    SELECT 1 FROM purser.billing_tiers
    WHERE stripe_price_id_monthly = sqlc.arg(price_id)::text
       OR stripe_price_id_yearly = sqlc.arg(price_id)::text
) OR EXISTS (
    SELECT 1 FROM purser.cluster_pricing
    WHERE stripe_price_id_monthly = sqlc.arg(price_id)::text
) AS recorded;
