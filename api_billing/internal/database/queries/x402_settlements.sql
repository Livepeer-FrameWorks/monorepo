-- name: LockTenantX402Address :one
SELECT x402_address_index, x402_address_xpub
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
FOR UPDATE;

-- name: AssignTenantX402Address :execrows
UPDATE purser.tenant_subscriptions
SET x402_address_index = sqlc.arg(address_index), x402_address_xpub = sqlc.arg(address_xpub), updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid;

-- name: UpsertX402CustodyAddress :exec
INSERT INTO purser.crypto_custody_addresses (
    tenant_id, source_kind, source_ref, network, asset, address, derivation_index, derivation_xpub
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, 'x402', sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(network), 'USDC', LOWER(sqlc.arg(address)), sqlc.arg(derivation_index), sqlc.arg(derivation_xpub)
)
ON CONFLICT (network, asset, address) DO UPDATE
SET tenant_id = EXCLUDED.tenant_id, derivation_index = EXCLUDED.derivation_index,
    derivation_xpub = EXCLUDED.derivation_xpub, updated_at = NOW();

-- name: CountX402NonceUses :one
SELECT COUNT(*) FROM purser.x402_nonces
WHERE network = sqlc.arg(network) AND payer_address = sqlc.arg(payer_address) AND nonce = sqlc.arg(nonce);

-- name: GetX402SettlementByIdentity :one
SELECT id::text AS id, network, tx_hash, tenant_id::text AS tenant_id,
       amount_cents, status, COALESCE(auth_payload::text, '')::text AS auth_payload,
       COALESCE(client_ip::text, '')::text AS client_ip
FROM purser.x402_nonces
WHERE network = sqlc.arg(network) AND payer_address = sqlc.arg(payer_address) AND nonce = sqlc.arg(nonce);

-- name: UpsertX402SettlementIntent :one
INSERT INTO purser.x402_nonces (
    network, payer_address, nonce, tenant_id, amount_cents,
    auth_payload, client_ip, status, settled_at, last_submit_attempt_at,
    quote_id, settlement_provider
) VALUES (
    sqlc.arg(network), sqlc.arg(payer_address), sqlc.arg(nonce), sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(amount_cents), sqlc.arg(auth_payload)::jsonb, NULLIF(sqlc.arg(client_ip), '')::inet,
    'submitting', NOW(), NOW(), NULLIF(sqlc.arg(quote_id), '')::uuid, sqlc.arg(settlement_provider)
)
ON CONFLICT (network, payer_address, nonce) DO NOTHING
RETURNING id::text AS id, tx_hash, tenant_id::text AS tenant_id, amount_cents, status,
          COALESCE(auth_payload::text, '')::text AS auth_payload,
          COALESCE(client_ip::text, '')::text AS client_ip;

-- name: MarkX402SettlementSubmitted :execrows
UPDATE purser.x402_nonces
SET tx_hash = sqlc.arg(tx_hash), submitted_at = NOW(), status = 'pending'
WHERE id = sqlc.arg(nonce_id)::text::uuid AND status = 'submitting';

-- name: MarkX402QuoteSettlingForNonce :exec
UPDATE purser.x402_payment_quotes quote
SET status = 'settling', updated_at = NOW()
FROM purser.x402_nonces nonce
WHERE nonce.id = sqlc.arg(nonce_id)::text::uuid AND quote.id = nonce.quote_id AND quote.status = 'claiming';

-- name: MarkActiveX402SettlementFailed :exec
UPDATE purser.x402_nonces
SET status = 'failed', failure_reason = sqlc.arg(reason)
WHERE id = sqlc.arg(nonce_id)::text::uuid AND status IN ('submitting', 'pending');

-- name: MarkX402QuoteFailedForNonce :exec
UPDATE purser.x402_payment_quotes quote
SET status = 'failed', failure_reason = sqlc.arg(reason), updated_at = NOW()
FROM purser.x402_nonces nonce
WHERE nonce.id = sqlc.arg(nonce_id)::text::uuid AND quote.id = nonce.quote_id
  AND quote.status IN ('claiming', 'settling', 'unknown');

-- name: MarkX402SettlementAttemptsFailed :exec
UPDATE purser.x402_settlement_attempts
SET state = sqlc.arg(attempt_state), last_error = sqlc.arg(reason), updated_at = NOW()
WHERE settlement_id = sqlc.arg(nonce_id)::text::uuid
  AND state IN ('prepared', 'broadcast_unknown', 'broadcast');

-- name: LockX402SettlementForConfirmation :one
SELECT status, tenant_id::text AS tenant_id, amount_cents, tx_hash
FROM purser.x402_nonces
WHERE id = sqlc.arg(nonce_id)::text::uuid
FOR UPDATE;

-- name: ConfirmX402Settlement :exec
UPDATE purser.x402_nonces
SET status = 'confirmed', confirmed_at = COALESCE(confirmed_at, NOW()),
    block_number = sqlc.arg(block_number), gas_used = sqlc.arg(gas_used), failure_reason = NULL
WHERE id = sqlc.arg(nonce_id)::text::uuid;

-- name: ConfirmX402SettlementAttempt :exec
UPDATE purser.x402_settlement_attempts
SET state = 'confirmed', confirmed_at = COALESCE(confirmed_at, NOW()),
    last_error = NULL, updated_at = NOW()
WHERE settlement_id = sqlc.arg(nonce_id)::text::uuid AND transaction_hash = LOWER(sqlc.arg(tx_hash));

-- name: ConfirmX402PaymentQuoteForNonce :exec
UPDATE purser.x402_payment_quotes quote
SET status = 'confirmed', provider_transaction_id = sqlc.arg(tx_hash),
    confirmed_at = COALESCE(quote.confirmed_at, NOW()), updated_at = NOW(),
    claim_expires_at = NULL, failure_reason = NULL
FROM purser.x402_nonces nonce
WHERE nonce.id = sqlc.arg(nonce_id)::text::uuid AND quote.id = nonce.quote_id;

-- name: ResetClaimingX402Quote :exec
UPDATE purser.x402_payment_quotes
SET status = 'offered', claim_token = NULL, claim_expires_at = NULL, updated_at = NOW()
WHERE id = sqlc.arg(quote_id)::text::uuid AND status = 'claiming';

-- name: MarkClaimingX402QuoteUnknown :exec
UPDATE purser.x402_payment_quotes SET status = 'unknown', updated_at = NOW()
WHERE id = sqlc.arg(quote_id)::text::uuid AND status IN ('claiming', 'settling');

-- name: MarkSettlingX402QuoteUnknown :exec
UPDATE purser.x402_payment_quotes SET status = 'unknown', updated_at = NOW()
WHERE id = sqlc.arg(quote_id)::text::uuid AND status = 'settling';

-- name: AcquireX402RelayerNonceLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(lock_key), 0));

-- name: GetFirstX402SettlementAttemptHash :one
SELECT transaction_hash FROM purser.x402_settlement_attempts
WHERE settlement_id = sqlc.arg(settlement_id)::text::uuid AND attempt_number = 1;

-- name: GetNextDurableX402RelayerNonce :one
SELECT COALESCE(MAX(relayer_nonce) + 1, 0)::bigint AS next_nonce
FROM purser.x402_settlement_attempts
WHERE network = sqlc.arg(network) AND LOWER(relayer_address) = LOWER(sqlc.arg(relayer_address));

-- name: InsertPreparedX402SettlementAttempt :exec
INSERT INTO purser.x402_settlement_attempts (
    settlement_id, attempt_number, network, chain_id, relayer_address,
    relayer_nonce, signed_raw_transaction, transaction_hash, gas_limit,
    max_fee_per_gas, max_priority_fee_per_gas, state
) VALUES (
    sqlc.arg(settlement_id)::text::uuid, 1, sqlc.arg(network), sqlc.arg(chain_id),
    LOWER(sqlc.arg(relayer_address)), sqlc.arg(relayer_nonce), sqlc.arg(signed_raw_transaction),
    LOWER(sqlc.arg(transaction_hash)), sqlc.arg(gas_limit), sqlc.arg(max_fee_per_gas),
    sqlc.arg(max_priority_fee_per_gas), 'prepared'
);

-- name: SetX402SettlementPrecomputedHash :execrows
UPDATE purser.x402_nonces SET tx_hash = LOWER(sqlc.arg(tx_hash))
WHERE id = sqlc.arg(settlement_id)::text::uuid AND status = 'submitting';

-- name: GetPreparedX402SettlementAttempt :one
SELECT signed_raw_transaction, transaction_hash
FROM purser.x402_settlement_attempts
WHERE settlement_id = sqlc.arg(settlement_id)::text::uuid
  AND state IN ('prepared', 'broadcast_unknown', 'broadcast')
ORDER BY attempt_number DESC LIMIT 1;

-- name: RecordX402EmbeddedBroadcastOutcome :exec
UPDATE purser.x402_settlement_attempts
SET state = sqlc.arg(state), broadcast_attempts = broadcast_attempts + 1,
    first_broadcast_at = COALESCE(first_broadcast_at, NOW()),
    last_broadcast_at = NOW(), last_error = sqlc.arg(detail), updated_at = NOW()
WHERE settlement_id = sqlc.arg(settlement_id)::text::uuid
  AND state IN ('prepared', 'broadcast_unknown', 'broadcast');

-- name: MarkX402EmbeddedAttemptBroadcast :exec
UPDATE purser.x402_settlement_attempts
SET state = 'broadcast', broadcast_attempts = broadcast_attempts + 1,
    first_broadcast_at = COALESCE(first_broadcast_at, NOW()),
    last_broadcast_at = NOW(), last_error = NULL, updated_at = NOW()
WHERE settlement_id = sqlc.arg(settlement_id)::text::uuid
  AND transaction_hash = LOWER(sqlc.arg(tx_hash));

-- name: MarkX402EmbeddedSettlementPending :execrows
UPDATE purser.x402_nonces
SET tx_hash = LOWER(sqlc.arg(tx_hash)), submitted_at = COALESCE(submitted_at, NOW()), status = 'pending'
WHERE id = sqlc.arg(settlement_id)::text::uuid AND status IN ('submitting', 'pending');

-- name: MarkX402EmbeddedQuoteSettling :exec
UPDATE purser.x402_payment_quotes quote
SET status = 'settling', updated_at = NOW()
FROM purser.x402_nonces nonce
WHERE nonce.id = sqlc.arg(settlement_id)::text::uuid AND quote.id = nonce.quote_id
  AND quote.status IN ('claiming', 'unknown', 'settling');

-- name: X402BalanceTransactionExists :one
SELECT EXISTS(
    SELECT 1
    FROM purser.balance_transactions
    WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND reference_id = sqlc.arg(reference_id)::text::uuid
      AND reference_type = sqlc.arg(reference_type)
      AND transaction_type = sqlc.arg(transaction_type)
);

-- name: GetX402CurrentBalance :one
SELECT balance_cents
FROM purser.prepaid_balances
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND currency = sqlc.arg(currency);

-- name: AcquireCryptoTaxDocumentLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(sqlc.arg(lock_key), 0));

-- name: GetExistingCryptoTaxDocumentNumber :one
SELECT invoice_number
FROM (
    SELECT simplified.invoice_number, simplified.issued_at
    FROM purser.simplified_invoices simplified
    WHERE simplified.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND simplified.reference_type = sqlc.arg(document_reference_type)
      AND simplified.reference_id = sqlc.arg(document_reference_id)
    UNION ALL
    SELECT crypto.invoice_number, crypto.issued_at
    FROM purser.crypto_invoices crypto
    WHERE crypto.tenant_id = sqlc.arg(tenant_id)::text::uuid
      AND crypto.reference_type = sqlc.arg(document_reference_type)
      AND crypto.reference_id = sqlc.arg(document_reference_id)
) documents
ORDER BY issued_at ASC
LIMIT 1;

-- name: NextSimplifiedInvoiceNumber :one
SELECT nextval('purser.simplified_invoice_number_seq')::bigint;

-- name: NextCryptoInvoiceNumber :one
SELECT nextval('purser.crypto_invoice_number_seq')::bigint;

-- name: InsertCryptoTopupInvoice :exec
INSERT INTO purser.crypto_invoices (
    invoice_number, tenant_id, reference_type, reference_id,
    gross_amount_cents, net_amount_cents, vat_amount_cents, vat_rate_bps,
    vat_rate_source, vat_rate_table_checked_on, vat_rate_effective_from, tax_validation_status,
    currency, amount_eur_cents, ecb_rate, fx_rate_source, fx_rate_observed_at,
    evidence_ip_country, evidence_wallet_network, evidence_billing_country,
    evidence_status, evidence_conflict, tax_policy_ref,
    supplier_name, supplier_address, supplier_vat_number, supplier_registration_number,
    service_description, service_quantity, service_date,
    customer_email, customer_name, customer_company, customer_address,
    customer_vat_number, customer_vat_validated, issued_at
) VALUES (
    sqlc.arg(invoice_number), sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(reference_type), sqlc.arg(reference_id),
    sqlc.arg(gross_amount_cents), sqlc.arg(net_amount_cents),
    sqlc.arg(vat_amount_cents), sqlc.arg(vat_rate_bps),
    sqlc.arg(vat_rate_source), sqlc.arg(vat_rate_table_checked_on)::text::date,
    sqlc.arg(vat_rate_effective_from)::text::date, sqlc.arg(tax_validation_status),
    sqlc.arg(currency), sqlc.arg(amount_eur_cents), sqlc.arg(ecb_rate),
    sqlc.arg(fx_rate_source), sqlc.arg(fx_rate_observed_at),
    sqlc.arg(evidence_ip_country), sqlc.arg(evidence_wallet_network),
    sqlc.arg(evidence_billing_country), sqlc.arg(evidence_status),
    sqlc.arg(evidence_conflict), sqlc.arg(tax_policy_ref),
    sqlc.arg(supplier_name), sqlc.arg(supplier_address), sqlc.arg(supplier_vat_number),
    sqlc.arg(supplier_registration_number), sqlc.arg(service_description),
    sqlc.arg(service_quantity), CURRENT_DATE,
    sqlc.arg(customer_email), sqlc.arg(customer_name), sqlc.narg(customer_company),
    sqlc.arg(customer_address)::jsonb, sqlc.narg(customer_vat_number),
    sqlc.arg(customer_vat_validated), NOW()
);

-- name: InsertSimplifiedCryptoTopupInvoice :exec
INSERT INTO purser.simplified_invoices (
    invoice_number, tenant_id, reference_type, reference_id,
    gross_amount_cents, net_amount_cents, vat_amount_cents, vat_rate_bps,
    vat_rate_source, vat_rate_table_checked_on, vat_rate_effective_from, tax_validation_status,
    currency, amount_eur_cents, ecb_rate, fx_rate_source, fx_rate_observed_at,
    evidence_ip_country, evidence_wallet_network, evidence_billing_country,
    evidence_status, evidence_conflict, tax_policy_ref,
    supplier_name, supplier_address, supplier_vat_number, supplier_registration_number,
    service_description, service_quantity, service_date, issued_at
) VALUES (
    sqlc.arg(invoice_number), sqlc.arg(tenant_id)::text::uuid,
    sqlc.arg(reference_type), sqlc.arg(reference_id),
    sqlc.arg(gross_amount_cents), sqlc.arg(net_amount_cents),
    sqlc.arg(vat_amount_cents), sqlc.arg(vat_rate_bps),
    sqlc.arg(vat_rate_source), sqlc.arg(vat_rate_table_checked_on)::text::date,
    sqlc.arg(vat_rate_effective_from)::text::date, sqlc.arg(tax_validation_status),
    sqlc.arg(currency), sqlc.arg(amount_eur_cents), sqlc.arg(ecb_rate),
    sqlc.arg(fx_rate_source), sqlc.arg(fx_rate_observed_at),
    sqlc.arg(evidence_ip_country), sqlc.arg(evidence_wallet_network),
    sqlc.arg(evidence_billing_country), sqlc.arg(evidence_status),
    sqlc.arg(evidence_conflict), sqlc.arg(tax_policy_ref),
    sqlc.arg(supplier_name), sqlc.arg(supplier_address), sqlc.arg(supplier_vat_number),
    sqlc.arg(supplier_registration_number), sqlc.arg(service_description),
    sqlc.arg(service_quantity), CURRENT_DATE, NOW()
);

-- name: LockX402SettlementRollup :one
SELECT tenant_id::text AS tenant_id, amount_cents, status, rollup_applied_at, rollup_reversed_at
FROM purser.x402_nonces
WHERE id = sqlc.arg(nonce_id)::text::uuid
FOR UPDATE;

-- name: AddX402TenantBalanceRollup :exec
INSERT INTO purser.tenant_balance_rollups (
    tenant_id, total_topup_cents, total_topup_eur_cents, topup_count, first_topup_at, last_topup_at
) VALUES (
    sqlc.arg(tenant_id)::text::uuid, sqlc.arg(amount_eur_cents), sqlc.arg(amount_eur_cents), 1, NOW(), NOW()
)
ON CONFLICT (tenant_id) DO UPDATE SET
    total_topup_cents = purser.tenant_balance_rollups.total_topup_cents + EXCLUDED.total_topup_cents,
    total_topup_eur_cents = purser.tenant_balance_rollups.total_topup_eur_cents + EXCLUDED.total_topup_eur_cents,
    topup_count = purser.tenant_balance_rollups.topup_count + 1,
    last_topup_at = NOW(),
    updated_at = NOW();

-- name: MarkX402RollupApplied :exec
UPDATE purser.x402_nonces
SET rollup_applied_at = NOW(), rollup_reversed_at = NULL
WHERE id = sqlc.arg(nonce_id)::text::uuid;

-- name: SubtractX402TenantBalanceRollup :execrows
UPDATE purser.tenant_balance_rollups
SET total_topup_cents = total_topup_cents - sqlc.arg(amount_eur_cents),
    total_topup_eur_cents = total_topup_eur_cents - sqlc.arg(amount_eur_cents),
    topup_count = topup_count - 1,
    updated_at = NOW()
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND total_topup_cents >= sqlc.arg(amount_eur_cents)
  AND total_topup_eur_cents >= sqlc.arg(amount_eur_cents)
  AND topup_count > 0;

-- name: MarkX402RollupReversed :exec
UPDATE purser.x402_nonces
SET rollup_reversed_at = NOW()
WHERE id = sqlc.arg(nonce_id)::text::uuid;

-- name: GetEffectiveVATRate :one
SELECT rate_bps, source, source_checked_on::text AS source_checked_on,
       effective_from::text AS effective_from
FROM purser.vat_rate_periods
WHERE country_code = sqlc.arg(country_code)
  AND effective_from <= sqlc.arg(effective_at)::date
  AND (effective_until IS NULL OR effective_until > sqlc.arg(effective_at)::date)
ORDER BY effective_from DESC
LIMIT 1;

-- name: GetActiveTenantTaxProfile :one
SELECT tax_id, COALESCE(billing_address, '{}'::jsonb) AS billing_address
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status != 'cancelled'
ORDER BY created_at DESC
LIMIT 1;

-- name: GetX402TaxSnapshot :one
SELECT quote.tax_document_kind, quote.tax_profile_snapshot,
       quote.eur_per_usd_rate::text AS eur_per_usd_rate, quote.created_at
FROM purser.x402_nonces nonce
JOIN purser.x402_payment_quotes quote ON quote.id = nonce.quote_id
WHERE nonce.tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND nonce.tx_hash = sqlc.arg(tx_hash)
ORDER BY nonce.settled_at DESC
LIMIT 1;

-- name: GetCryptoWalletTaxSnapshot :one
SELECT tax_document_kind, tax_profile_snapshot,
       quoted_usd_to_eur_rate::text AS quoted_usd_to_eur_rate, quoted_at
FROM purser.crypto_wallets
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND tx_hash = sqlc.arg(tx_hash)
  AND purpose = 'prepaid'
ORDER BY created_at DESC
LIMIT 1;

-- name: GetCryptoDocumentBillingProfile :one
SELECT billing_email, billing_name, billing_company,
       COALESCE(billing_address, '{}'::jsonb) AS billing_address, tax_id
FROM purser.tenant_subscriptions
WHERE tenant_id = sqlc.arg(tenant_id)::text::uuid
  AND status != 'cancelled'
ORDER BY created_at DESC
LIMIT 1;
