-- name: UpsertFXRate :exec
INSERT INTO purser.fx_rates (currency, reference_date, units_per_eur, source, fetched_at)
VALUES (
    sqlc.arg(currency)::text, sqlc.arg(reference_date)::date,
    sqlc.arg(units_per_eur)::text::numeric, 'ecb', sqlc.arg(fetched_at)::timestamptz
)
ON CONFLICT (currency, reference_date) DO UPDATE
SET units_per_eur = EXCLUDED.units_per_eur,
    fetched_at = EXCLUDED.fetched_at;

-- name: GetFXRateOnOrBefore :one
SELECT currency::text AS currency,
       reference_date::date AS reference_date,
       units_per_eur::text AS units_per_eur,
       source::text AS source,
       fetched_at::timestamptz AS fetched_at
FROM purser.fx_rates
WHERE currency = sqlc.arg(currency)::text
  AND reference_date <= sqlc.arg(on_date)::date
ORDER BY reference_date DESC
LIMIT 1;

-- name: ListLatestFXRateReferenceDates :many
SELECT currency::text AS currency,
       MAX(reference_date)::date AS reference_date
FROM purser.fx_rates
GROUP BY currency
ORDER BY currency;
