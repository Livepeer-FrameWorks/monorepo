-- name: LoadClusterPricingHistory :one
SELECT version_id, pricing_model, currency, base_price::text, metered_rates::text
FROM purser.cluster_pricing_history
WHERE cluster_id = $1
  AND effective_from <= $2
  AND (effective_to IS NULL OR effective_to > $2)
ORDER BY effective_from DESC
LIMIT 1;
