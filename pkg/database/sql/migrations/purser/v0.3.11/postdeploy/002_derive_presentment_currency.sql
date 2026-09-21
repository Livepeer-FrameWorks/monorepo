-- Presentment currency follows the billing country: EU member states,
-- Iceland, Liechtenstein, and Norway present in EUR, the United Kingdom in
-- GBP, and every other or unknown country in USD. Tenants with a provider
-- subscription or an open invoice keep the currency they were billed in.
-- pkg/billing PresentmentCurrencyForCountry holds the same country lists.
UPDATE purser.tenant_subscriptions AS subscription
SET presentment_currency = derived.currency
FROM (
    SELECT tenant_id,
           CASE
               WHEN UPPER(COALESCE(billing_address->>'country', '')) IN (
                   'AT', 'BE', 'BG', 'CY', 'CZ', 'DE', 'DK', 'EE', 'ES', 'FI',
                   'FR', 'GR', 'HR', 'HU', 'IE', 'IT', 'LT', 'LU', 'LV', 'MT',
                   'NL', 'PL', 'PT', 'RO', 'SE', 'SI', 'SK',
                   'IS', 'LI', 'NO'
               ) THEN 'EUR'
               WHEN UPPER(COALESCE(billing_address->>'country', '')) = 'GB' THEN 'GBP'
               ELSE 'USD'
           END AS currency
    FROM purser.tenant_subscriptions
) AS derived
WHERE subscription.tenant_id = derived.tenant_id
  AND subscription.presentment_currency <> derived.currency
  AND NOT purser.tenant_presentment_currency_locked(subscription.tenant_id);
