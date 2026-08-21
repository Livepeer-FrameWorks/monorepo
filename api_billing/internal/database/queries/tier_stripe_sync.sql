-- name: ListActivePaidBillingTiersForStripe :many
SELECT id, tier_name, display_name, COALESCE(description, '') AS description,
       base_price::text AS base_price, currency,
       stripe_product_id, stripe_price_id_monthly
FROM purser.billing_tiers
WHERE base_price > 0 AND COALESCE(is_active, true) = true
ORDER BY tier_name;

-- name: UpdateBillingTierStripeIDs :execrows
UPDATE purser.billing_tiers
SET stripe_product_id = sqlc.arg(stripe_product_id),
    stripe_price_id_monthly = sqlc.arg(stripe_price_id_monthly),
    updated_at = NOW()
WHERE id = sqlc.arg(id);
