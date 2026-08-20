# Crypto payment incident runbook

This runbook covers new direct-deposit and x402 incidents. Reconciliation,
customer document access, and sweep reconciliation stay online when creation is
disabled. Never delete or manually rewrite a settlement, deposit event, nonce,
ledger entry, or sweep row.

Start every incident with:

```bash
frameworks crypto readiness --output json
```

Capture the command output, Purser logs, relevant Prometheus series, network,
transaction/quote IDs, UTC timestamps, and the configuration revision in the
incident record.

## Emergency disablement

- Stop new direct deposit addresses with `CRYPTO_DEPOSITS_ENABLED=false`.
- Stop new x402 quotes and settlements with `X402_PAYMENTS_ENABLED=false`.
- Restart/redeploy Purser and Bridge through the normal deployment workflow.
- Do not stop the scanner, x402 reconciler, invoice access, or sweep reconcile.
- Re-enable only when `frameworks crypto readiness` is green and the incident
  evidence explains the failure and remediation.

## Facilitator outage

1. Confirm the official v2 facilitator readiness/support call is failing; do
   not switch production to self-settlement.
2. Disable new x402 operations. Direct deposits may remain enabled if their
   independent readiness checks are green.
3. Leave `submitting`, `settling`, and `unknown` records for reconciliation.
   A timeout is not proof of failure and must never cause a second submission.
4. When the facilitator recovers, verify provider transaction IDs and canonical
   finalized receipts before re-enabling.

## RPC lag or missing finalized head

1. Inspect `purser_crypto_scanner_block`, scanner error count, and cursor lag.
2. Verify the configured endpoint can return the chain's `finalized` block and
   the recorded block hash. Do not substitute `latest` or a guessed depth.
3. Move to an approved RPC endpoint through configuration management.
4. Deposits remain pending while finality is unavailable. Never credit them
   manually merely to clear customer support pressure.

## Consumed authorization with no transaction hash

1. Locate the open `x402_consumed_authorization_missing_transaction` row in
   `purser.crypto_accounting_anomalies`.
2. Search the facilitator and chain by payer, USDC contract, authorization
   nonce, recipient, exact amount, and authorization validity window.
3. If a unique canonical transaction is found, attach its provider/RPC evidence
   and use an audited operator reconciliation procedure. Do not resubmit: USDC
   reports the authorization as consumed.
4. If no unique transaction can be proven, keep the item open and do not credit.

## Reorg or post-finality canonical mismatch

The reconciler reverses the prepaid credit (the balance may become negative),
reopens/claws back invoice allocation, creates a linked credit note where a
customer document exists, and blocks ordinary prepaid admission.

1. Confirm old and canonical block hashes from independent RPC evidence.
2. Confirm the reversal ledger reference and credit note exist exactly once.
3. Contact the customer with the affected transaction and document references.
4. Do not waive the negative balance by editing it; an approved compensating
   credit must use the normal audited operator-credit path.

## Late or unsupported deposit

Expired addresses remain watched indefinitely. Late prepaid USDC is valued at
actual stablecoin value; late ETH uses receipt-time price/FX evidence. Late
invoice deposits, partial invoice deposits, unsupported assets, internal native
transfers, and conflicting observations stay in operator review.

Resolve only after destination ownership, canonical receipt, valuation source,
tenant, invoice (if any), and allocation decision are recorded.

## Sweep stuck, failed, or interrupted

Follow [the sweep ceremony](sweep-ceremony.md). Re-running broadcast with the
same bundle replays the persisted raw transaction/relayer intent. Never create a
new bundle merely because the first broadcast timed out. Reconcile individual
items; a partial batch is not rolled back as a unit.

For an expired signed ETH item, do not release the source for a new nonce until
the signed raw transaction is proven absent/nonviable under the approved
recovery procedure. ETH raw transactions have no protocol expiry.

## Insufficient USDC relayer gas

1. Disable new x402 settlement if the same relayer is affected; direct USDC
   deposits may continue, but do not broadcast new USDC sweeps.
2. Fund only the configured dedicated relayer address with native gas under the
   treasury approval process.
3. Verify current nonce, balance, fee ceiling, token domain, and authorization
   state before replaying the persisted relay transaction.

## VIES or tax-evidence outage

1. Do not grant reverse charge from VAT number format alone.
2. Valid, unexpired cached VIES evidence may be used under the configured cache
   policy. Otherwise the transaction/document remains non-validated or enters
   tax review.
3. Missing or conflicting billing-country/IP signals create an open
   `tax_location_evidence_review` anomaly. Blockchain network is never customer
   location evidence.
4. Keep production money acceptance disabled if supplier identity/country
   configuration or the effective VAT catalog is incomplete.

## Closure evidence

An incident closes only with canonical transaction/receipt evidence, affected
tenant and ledger/document references, anomaly resolution, configuration and
deployment revisions, readiness output, and a statement that duplicate credit,
duplicate mutation execution, and unswept custody were checked.
