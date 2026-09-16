-- Merge prepaid balance rows keyed by a non-uppercase currency (Stripe card
-- top-ups credited "eur") into the uppercase row that admission, usage burn,
-- and invoice credit read. balance_remainder_micro is in 10^-6 currency units
-- (10,000 per cent), so merged remainders carry whole cents into balance_cents
-- and keep the remainder in [0, 10000). Once no non-uppercase rows remain,
-- every statement matches nothing.

INSERT INTO purser.prepaid_balances (
    tenant_id, balance_cents, balance_remainder_micro, currency,
    low_balance_threshold_cents, created_at, updated_at
)
SELECT tenant_id, 0, 0, UPPER(currency), MAX(low_balance_threshold_cents), NOW(), NOW()
FROM purser.prepaid_balances
WHERE currency <> UPPER(currency)
GROUP BY tenant_id, UPPER(currency)
ON CONFLICT (tenant_id, currency) DO NOTHING;

UPDATE purser.prepaid_balances target
SET balance_cents = target.balance_cents + source.balance_cents
        + (target.balance_remainder_micro + source.remainder_micro
           - (((target.balance_remainder_micro + source.remainder_micro) % 10000) + 10000) % 10000) / 10000,
    balance_remainder_micro =
        (((target.balance_remainder_micro + source.remainder_micro) % 10000) + 10000) % 10000,
    updated_at = NOW()
FROM (
    SELECT tenant_id, UPPER(currency) AS currency,
           SUM(balance_cents)::bigint AS balance_cents,
           SUM(balance_remainder_micro)::bigint AS remainder_micro
    FROM purser.prepaid_balances
    WHERE currency <> UPPER(currency)
    GROUP BY tenant_id, UPPER(currency)
) source
WHERE target.tenant_id = source.tenant_id
  AND target.currency = source.currency;

DELETE FROM purser.prepaid_balances
WHERE currency <> UPPER(currency);
