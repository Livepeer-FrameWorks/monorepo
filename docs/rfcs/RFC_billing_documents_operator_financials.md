# RFC: Billing documents and operator financial exports

## Status

Approved for staged implementation — 2026-11-05, including consumers. No production deployment or payout is performed by this work.
Local execution log: `PLAN_BILLING_FINANCIALS_2026-11-04.md` (root PLAN files are
ignored by repository convention; this RFC contains the durable proposal).

First delivery implements personal/business profiles, a shared tax decision
engine with crypto payment safeguards, and the read-only
`frameworks admin billing finance audit` evidence archive. Its operator contract
and remaining limitations are documented in
`website_docs/src/content/docs/operators/billing-financials.mdx`. The archive is
explicitly not posting-ready. Regular invoice tax finalization, Stripe recurring
tax integration, prepaid migration, reconciliation and journal exports below
remain outstanding.

DDVTech reports predominantly B2B support sales before launching this product.
That does not establish a consumer VAT exemption, verified cross-border annual
totals, or an OSS election. No exemption or registration is activated from that
statement. Unconfigured foreign consumer tax treatment is held before new crypto
payment instructions; enabling the threshold exception requires verified inputs.

## TL;DR

- DDVTech B.V. is the seller for its hosted FrameWorks business. Purser issues
  customer documents on that seller's behalf. It does not invoice DDVTech for
  DDVTech's own sales. Independently operated installations use their own seller
  configuration; tenant identity is not seller identity.
- Keep Stripe subscription collection. Stripe issues its base-fee/proration tax
  invoices; Purser imports the original documents and issues invoices for the
  supplies it collects itself. Purser provides the consolidated financial record.
  There is no subscription-engine migration in this implementation.
- Give operators an accounting close bundle: sales and corrections, VAT/ICP,
  payments, provider balance movements, fees, payouts, receivables, prepaid
  liabilities, and applicable marketplace payables, linked to original evidence.
- Establish DDVTech's first reconciled close now, using provider CSV exports if
  necessary. A Stripe-to-bank payout transfers an asset; it is not sales revenue.

## Current state

These are source observations, not a production audit or compliance certificate.

| Area                          | Observed implementation                                                                                                                                                                                                  | Consequence                                                                                                                                                                                        |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Postpaid                      | `api_billing/internal/handlers/postpaid_invoice.go` rates usage, applies prepaid credit and collection minimums; `purser.billing_invoices` has base/metered/amount fields without a corresponding explicit tax breakdown | Taxable supply, tax, invoice total and amount collected need separate, stable meanings                                                                                                             |
| PDFs                          | `api_billing/internal/grpc/billing_documents.go:invoiceTotals` displays subtotal, credit, carried amounts and amount due                                                                                                 | Current regular invoice totals do not provide the required tax breakdown                                                                                                                           |
| Stripe subscriptions          | `api_billing/internal/stripe/client.go:CreateCheckoutSession` uses subscription mode; `handlers/webhooks.go:handleStripeInvoicePaid` explicitly leaves the provider-owned base charge outside the local usage invoice    | A provider base-fee invoice and a Purser usage invoice can represent different supplies; two documents alone do not prove duplicate billing. The base fee still needs a complete tax/document path |
| Stripe tax setup              | The inspected checkout code does not set automatic tax, tax ID collection or explicit tax rates                                                                                                                          | Code does not establish tax correctness; inspect actual account/customer/price settings and issued invoices before determining historical liabilities                                              |
| Crypto                        | `handlers/x402.go` stores VAT decisions, VIES evidence and a `vat-at-confirmed-topup-v1` policy; schema includes simplified/full crypto documents                                                                        | Reuse useful primitives after reviewing policy. Presence of fields does not establish correct place of supply, prepaid tax timing or legal evidence                                                |
| Identity                      | `handlers/document_supplier.go:DocumentSupplierSnapshot` returns empty when configuration is incomplete and allows rendering with current supplier details                                                               | Legacy documents may not reproduce their historical seller identity; finalization should require an immutable complete identity                                                                    |
| Numbering/retention           | Schema uses `nextval` for invoice numbers and retention/delete guards; supplier/customer snapshots also exist                                                                                                            | Sequence allocation is not proof of gaplessness or full immutability. Preserve issued facts and explain numbering gaps; assess retention by record class                                           |
| Existing financial primitives | Prepaid balances/statements, credit notes, collection balances, provider settlements/presentment FX, and `operator_credit_ledger` already exist                                                                          | Extend the owning records and reuse their identities; do not introduce competing balance systems                                                                                                   |
| Marketplace                   | `docs/architecture/cross-cluster-billing.md` accrues paid third-party work to operator credits                                                                                                                           | Customer sales, third-party operator payables and platform margin require separate accounting; a credit ledger is not itself a supplier tax invoice                                                |

No claim is made that actual Stripe settings, all top-up paths, historical
invoices, or company books were audited. Read those in step 0.

## Problem / Motivation

DDVTech needs evidence for its company accounts before moving funds out of
Stripe: what was sold, what tax is owed, what customers still owe, what prepaid
credit is unconsumed, what Stripe deducted, and how the remaining balance reaches
the bank. A customer PDF list or a net payout report alone cannot answer this.

Crypto compliance, sequence continuity, and overlapping supplies need explicit
verification against source evidence and actual issued documents.

## Goals

- Complete tax documents and correctly taxed collections for every enabled path.
- A reproducible trail from supply to invoice, payment, provider balance, payout
  and bank receipt, including refunds, disputes, fees and FX.
- A practical DDVTech accounting handoff that also works for other operators.
- Historical repair without silently rewriting issued invoices or charging
  customers again.

## Non-Goals

- A company general ledger, corporate tax return, or automatic VAT/ICP filing.
- Automatic bank withdrawals, changes to live payout schedules, or automatic
  historical tax collections as part of implementing this proposal.
- Treating a platform admin credit as cash received, or treating a provider
  settlement record as a supplier invoice.

## Proposal

### Implementation contract

The following is the proposed implementation, including names of new files,
fields, commands and tests. None of these additions exists merely because it
appears in this RFC. Existing code observations are listed separately above.

#### A. Eight changes, with concrete code ownership

| Change                        | Existing code to change                                                                                                                                                                                                         | New code / output                                                                                                                      | Exit criterion                                                                                                                               |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------- |
| 1. Read-only historical audit | `internal/stripe/client.go`, `internal/grpc/`, `cli/cmd/admin.go`                                                                                                                                                               | `internal/finance/audit.go`, `internal/stripe/finance_reader.go`, `internal/grpc/admin_finance.go`, `cli/cmd/admin_billing_finance.go` | Paginated live/test-separated inventory from inception; every charge has a tax-document link or an actionable exception                      |
| 2. Shared tax decisions       | `handlers/x402.go:getVATDecisionForProfile`, `handlers/vies.go`, `grpc/server.go:UpdateBillingDetails`                                                                                                                          | `internal/tax/decision.go`, `internal/tax/profile.go`, tax-aware quote/decision payload                                                | Same input/date yields the same tax decision across collection providers; unresolved reverse-charge evidence cannot silently become zero tax |
| 3. Purser invoices            | `handlers/postpaid_invoice.go:writePostpaidInvoiceTx`, `handlers/tier_upgrade_invoice.go`, `handlers/jobs.go:chargeStripeOverage`, `grpc/billing_documents.go`, `grpc/billing_document_pdf.go`                                  | `handlers/invoice_tax.go`, `handlers/document_finalization.go`; tax columns and immutable document files                               | Frozen tax totals and original PDF; exact gross outstanding collected once                                                                   |
| 4. Stripe recurring invoices  | `stripe/client.go:CreateCheckoutSession`, `stripe/tier_upgrade.go`, `stripe/subscription_end.go`, `handlers/webhooks.go:handleStripeInvoicePaid`                                                                                | `stripe/tax.go`, `handlers/stripe_invoice_documents.go`; imported provider invoices/credit notes                                       | First Checkout, renewal, failed payment and upgrade have one authoritative supply document and correct tax                                   |
| 5. Prepaid                    | `grpc/server.go` top-up entry points, `handlers/checkout.go:handlePrepaidCheckoutCompleted`, `handlers/x402.go:generateCryptoTopupInvoice`, `handlers/prepaid_statement.go`, `handlers/webhooks.go:applyPrepaidTopupReversalTx` | `handlers/prepaid_tax.go`; funding lots and tax-aware quotes                                                                           | Cash, net credit, VAT and remaining liability reconcile across card, Mollie and crypto                                                       |
| 6. Provider balance           | `handlers/provider_settlements.go`, `handlers/refund_settlement.go`, Stripe/Mollie clients                                                                                                                                      | `internal/finance/reconcile.go`, `handlers/provider_finance_sync.go`; movement/payout imports                                          | Full balance proof includes fees, refunds, reserves, FX and reversals rather than successful charges alone                                   |
| 7. Bookkeeping bundle         | `internal/grpc/`, `cli/cmd/`, billing document queries                                                                                                                                                                          | `internal/finance/export.go`, `internal/finance/journal.go`, `internal/finance/controls.go`; frozen close snapshots and ZIP download   | Balanced import feed with complete supporting schedules, stable posting IDs and reproducible hashes                                          |
| 8. Corrections and rollout    | Original issuer adapters, document download/history APIs, existing stack fixtures                                                                                                                                               | Correction manifest/apply workflow; operator runbook; stack financial fixture                                                          | Historical exceptions resolved, reviewed import acknowledged, full verification and pilot close recorded                                     |

Paths in this table are relative to `api_billing/` except explicitly prefixed
`cli/` paths. PR 1 is first. PR 3/4 depend on PR 2; PR 5 depends on PR 2/3; PR 6 can start after
PR 1; PR 7 joins PR 3–6; PR 8 provides activation gates. Each bug fix records its
red assertion before the green result. No broad resolver or generated-file edits.

#### B. Money fields and schema

Extend the canonical baseline `pkg/database/sql/schema/purser.sql` and matching
pending-release expand/backfill/contract migrations. Register every new table
in `pkg/database/sql/layout/purser.yaml`. Use nullable fields for historical
unknowns; never default missing historic VAT to zero. Migration sequence numbers
are assigned at implementation time after checking other pending migrations.

| Record                                                | Proposed fields / invariant                                                                                                                                                                                                                                                                                                                                                                                            |
| ----------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `billing_invoices`                                    | `net_eur_cents`, `vat_eur_cents`, `gross_eur_cents`; `document_net_minor`, `document_vat_minor`, `document_gross_minor` in existing presentment currency; `tax_status` (`ready`, `review`, `legacy_unknown`), `tax_policy_version`, `tax_decision` JSONB, `tax_summary` JSONB, `tax_point_at`. Existing `amount` stays the legacy collection projection; all new document/accounting consumers use frozen gross totals |
| `invoice_line_items`                                  | Net/VAT/gross EUR cents, `tax_treatment`, `vat_rate_bps`, `tax_point_at`; keep quantity/unit/net unit price and cluster attribution. `amount` retains its rated-net meaning. Persist document-currency line/tax totals in the issued rendering payload; never recompute with a later FX rate                                                                                                                           |
| `credit_notes`                                        | Original tax-policy reference, net/VAT/gross and tax groups in original currency/EUR; credit amounts remain positive in storage, exported as negative sales. Cumulative credited gross cannot exceed the source supply                                                                                                                                                                                                 |
| `billing_document_files` (new)                        | `tenant_id`, source kind/ID, original filename, PDF bytes, SHA-256, rendering version, frozen rendering JSON, issued/retention dates. Unique `(tenant_id, source_kind, source_id)`; issued content cannot be replaced                                                                                                                                                                                                  |
| `provider_billing_documents` (new)                    | Customer `tenant_id`, provider/account/mode, provider invoice or credit-note ID, original number, source invoice link for credits, dates/service period, currency, net/VAT/gross, tax groups, supplier/customer snapshots, raw provider facts. Unique provider/account/mode/type/ID; points to the original PDF in `billing_document_files`; no local `INV-` number                                                    |
| `prepaid_funding_lots` (new)                          | Customer `tenant_id`, funding reference, source tax document, policy version, cash gross, net service credit, VAT, original-currency/FX basis, remaining net credit and legacy classification. Usage/refund allocations reference the lot in existing balance/settlement metadata; no second spending balance                                                                                                          |
| `provider_balance_movements` (new)                    | Finance-owner `tenant_id`, provider/account/mode, unique balance transaction ID, signed gross/fee/net, currency, type/category, source object, creation/availability dates, actual payout link, mapped customer tenant/object where known, source payload/hash and import watermark                                                                                                                                    |
| `provider_payouts` (new)                              | Finance-owner `tenant_id`, provider/account/mode, payout ID, currency/amount, automatic/manual/instant, provider status, initiation/arrival dates, bank reference/match, reversal movement, original facts. A failed payout cannot remain a completed bank transfer                                                                                                                                                    |
| `financial_close_runs` + `financial_close_rows` (new) | Finance-owner `tenant_id`, run UUID, seller identity, period/timezone, schema and mapping versions, per-source watermarks/completeness, immutable dataset rows, controls/exception snapshot and hashes, export and import acknowledgment. Corrections create a new revision/run; previous export content remains retrievable                                                                                           |

All supported fiat currencies currently use two minor digits; reject a currency
outside the supported set rather than assuming every world currency has cents.
Use integer minor units at the document/export boundary and decimal arithmetic
when rating. Per-group VAT uses decimal half-up rounding to minor units; the sum
of allocated line VAT must equal the persisted group VAT. Allocate any rounding
remainder deterministically by fractional remainder then stable line key.
For imported Stripe invoices retain Stripe's actual line/group amounts; an
unexplained difference from the approved decision becomes an exception.

Persist payment/collection allocations to the original invoice in the existing
collection-entry metadata/queries. One collection can settle several invoices;
its carry is not a new sale. Keep marketplace revenue sharing on original net
service revenue, excluding VAT; add a regression before changing this path.

Finance-owner tenant scope is a dedicated operator accounting scope configured
once for the seller, with no paid subscription required. It does not mean
DDVTech buys its own service. Every new SQL query filters `tenant_id`. Provider
account rows use the finance owner; customer document reads use the customer
tenant. Only a platform finance role can orchestrate both scopes, and every
cross-scope lookup is authorized and audited. Tenant enumeration uses the owning
service's client, never another service's database.

#### C. Tax/price contract and examples

Proposed `tax.Decision` takes supplier profile, versioned customer profile,
supply classification, tax point and net line amounts. It returns treatment,
rate/tax groups, net/VAT/gross, EUR valuation, required wording, evidence IDs,
policy version and `ready`/`review`. It has no Stripe/Mollie/crypto-specific rule.

Consumer support is required alongside business support. The decision engine
includes domestic VAT, EU B2B reverse charge, EU consumer destination and
conditional origin-threshold treatments, plus reviewed non-EU business/consumer
jurisdiction rules. Each enabled jurisdiction needs its explicit rule/evidence
fixtures and real registration state; a foreign consumer is never assumed exempt. A missing/invalid VAT claim produces a review action,
not an invented consumer classification. Outside-EU status needs business/place
evidence and a reviewed destination treatment, not a country-only bypass.

| Scenario                                                       | Required result                                                                                        |
| -------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| New NL invoice, €100 net service                               | Net 10000, VAT 2100, gross 12100 cents; complete parties and dates                                     |
| Same invoice with €30 gross prepaid/payment allocation         | Document stays 10000/2100/12100; remaining receivable/collection is 9100 cents                         |
| Qualifying cross-border EU B2B €100                            | Net 10000, VAT 0, gross 10000; both IDs, evidence and `btw verlegd`; qualifying ICP row                |
| New NL advance buying €100 net credit                          | Gross payment 12100, VAT 2100, spendable net credit 10000; funding liability 10000                     |
| Consume €25 of that advance                                    | Remaining liability/credit 7500; recognize 2500 service revenue; no new VAT or receivable              |
| Receive €121 gross crypto equivalent under that advance policy | Locked FX gives 12100 EUR gross, 2100 VAT and 10000 net credit; crypto amount and chain proof retained |
| Historic €100 charge with unproven price/tax basis             | `legacy_unknown`, amount 10000 retained; no guessed VAT or silent balance reduction                    |

The proposed prepaid contract is an advance for identified services with a known
customer and tax treatment. Confirm that legal classification once before
activation. If a credit product is instead a multipurpose voucher/deposit, it
must use a separately approved redemption policy; do not ship advance taxation
for that product. Existing balances stay intact until each lot is classified;
never shrink them by extracting newly assumed VAT. Legacy profile/rate changes,
expired grants and refunds require explicit allocation, with oldest eligible
funding lot consumed first and sub-cent usage preserved by existing micro ledgers.

#### C1. Customer experience: automatic tax, one saved billing profile

Make this an explicit deliverable of PR 2–5, not a later dashboard enhancement.
Reuse the existing settings billing form and checkout entry points in
`website_application/src/routes/settings/+page.svelte`,
`routes/account/billing/+page.svelte` and `routes/account/balance/+page.svelte`.
Use a shared billing-details/quote component, with the existing
`GetBillingDetails.gql` and `UpdateBillingDetails.gql` operations extended through
their schema source and normal codegen. The user does not select a tax rate or
toggle VAT exemption.

1. Ask once for legal business name, billing address/country and applicable tax
   identifier. Prefill existing values; IP geolocation may suggest an unconfirmed
   country. Explain which additional field is needed in the same form.
2. Validate the profile and VAT claim server-side and store validation status,
   evidence timestamp and profile version. A valid foreign B2B case receives the
   applicable no-Dutch-VAT treatment automatically. A normal complete profile
   requires no support ticket or operator approval per purchase.
3. Add a server-side `GetBillingTaxQuote` contract shared by tier checkout,
   upgrades and top-ups. Return priced lines, net/tax/gross in payment currency,
   treatment, plain-language explanation, profile version, decision ID and
   expiry. A Dutch €100 net service shows `Subtotal €100; VAT 21% €21; Total €121`.
   An eligible EU B2B quote shows `VAT reverse charged; Total €100`.
4. Bind the accepted quote to the tenant, purchase, profile version, tax point,
   source prices and FX quote. Revalidate on the server before creating payment;
   a client-supplied country/rate/total cannot override it. If the profile or
   payable total changes, show a new quote before payment authorization.
5. Reuse the saved profile for subsequent purchases and renewals. Refresh evidence
   automatically when due. A usable existing validation can survive a transient
   validation-service outage under the approved validity policy; a new unresolved
   claim cannot silently gain reverse charge. Show checking/retry/field-specific
   errors only when resolution is actually required.
6. Tax follows the relevant customer business establishment and supply rule.
   A user travelling, switching VPN, changing wallet, selecting another edge or
   streaming to viewers abroad does not itself change that billing profile.
   Apply location-conflict review only where required by the applicable evidence
   rules; do not hold every valid B2B purchase because an IP differs.
7. The stored quote/decision controls Stripe or Mollie collection, crypto funding,
   customer invoice, and DDVTech export. Profile changes apply to future tax points
   and explicit corrections; previously issued documents remain unchanged.

Expose tax status as `ready`, `checking`, `needs_information` or `retryable_error`
with specific fields/actions. Keep internal treatments and evidence identifiers
out of the ordinary checkout copy. Unresolved classification blocks that new
purchase, not unrelated access already paid for. For legitimate purchases that
do not qualify for reverse charge, use the supported taxable rule and preview
its amount rather than promising all foreign customers zero VAT.

#### D. Exact Stripe behavior

Keep Stripe Billing for base fees, cluster subscriptions and its native prorations.
Purser does not create a second legal invoice for those supplies. It stores the
original Stripe invoice number/PDF, plus normalized facts for the unified history
and accounting export. Purser's existing included-subscription usage line stays
informational and references the relevant provider document(s).

Implement `stripe/tax.go` to sync legal customer name/address, supported tax IDs,
and the decision's reverse-charge/exemption representation. Map the approved
rate to a Stripe Tax Rate object with matching percentage and exclusive/inclusive
behavior; persist provider/account/rate identity. New net-price subscriptions use
exclusive rates. Use Checkout line tax rates and subscription default/item tax
rates as supported by pinned stripe-go/v85. Test all emitted parameters; do not
infer support solely from the latest website API version. Keep automatic tax off
for this manual-rate integration to prevent a second independent calculation.

Before first Checkout, require a ready decision and synchronize the subscription
tax configuration. On profile/rate changes, mark tax synchronization pending and
hold affected collection until provider confirmation. Revalidate before renewal;
keep a due-work query keyed to the recorded evidence/rate expiry. For a known
unresolved profile, put collection on a provider-side `keep_as_draft` hold and
handle already-open invoices explicitly. The hold must preserve subscription
access according to existing entitlement policy, not grant a new paid period.
Use `invoice.created` to verify/update draft facts and disable advancement for a
discrepancy; a delayed webhook must not be the sole protection against stale tax
configuration. Poll/backfill held drafts because paused subscriptions can suppress
upcoming-invoice webhooks. Releasing a hold is an explicit audited workflow.

Import `invoice.finalized`, `invoice.paid`, failed/void/uncollectible states and
provider credit notes using the webhook inbox plus an idempotent backfill. On
finalization: fetch all invoice lines, freeze source facts, download and retain
the original PDF. If paid arrives first, fetch/import the finalized invoice
before recording its payment. Missing PDFs remain retryable exceptions, not
locally regenerated replacements with another number. Invoice and payment
events must not both produce sales postings.

Configure DDVTech's legal account name, address, VAT ID and KVK disclosure on
Stripe documents, then inspect actual test PDFs. Preserve Stripe tax invoice
emails/portal access for Stripe-owned supplies; Purser notifications link the
same original document. Purser owns emails/PDFs for its supplies. Payment receipts
and usage statements remain supporting documents. Audit the Stripe account for
existing automatic-tax or conflicting customer overrides before activating rates.

Keep upgrade validation against the net base line. Add fixtures where base net,
gross paid, credited unused net/tax and replacement gross all differ; retain
original proration tax bases. Treat this as required regression coverage, not a
claim that the current net-line comparison is itself defective.

#### E. Finance commands, APIs, and output files

Add an `admin billing finance` Cobra group, backed by new Purser RPCs in
`pkg/proto/purser.proto`: `AdminAuditFinancials`, `AdminSyncProviderFinancials`,
`AdminCreateFinancialClose`, `AdminGetFinancialClose`,
`AdminDownloadFinancialClose`, `AdminPreviewBillingCorrection`,
`AdminApplyBillingCorrection`, `AdminAcknowledgeFinancialClose`,
`AdminCheckPayoutReadiness`, `AdminRecordPayoutBankMatch`. Use request IDs for mutations and paginated/streamed
responses for financial datasets; no 1000-document truncation inherited from
`ListBillingDocuments`. Generate clients with repository targets.

Proposed operator workflow (commands do not exist yet):

```text
frameworks admin billing finance audit --provider stripe --from inception --through now
frameworks admin billing finance sync --provider stripe --from inception --through now
frameworks admin billing finance export --from inception --through now --output ./ddvtech-opening.zip
frameworks admin billing finance corrections preview --run <run-id> --output ./corrections.json
frameworks admin billing finance corrections apply --manifest ./corrections.json --reason <reason>
frameworks admin billing finance export --period 2026-10 --timezone Europe/Amsterdam --output ./ddvtech-2026-10.zip
frameworks admin billing finance acknowledge --run <run-id> --import-reference <bookkeeping-reference> --mapping ./accounts.json
frameworks admin billing finance readiness --run <run-id>
frameworks admin billing finance match-payout --provider stripe --payout-id <id> --bank-reference <ref> --received-date YYYY-MM-DD --received-minor <amount> --currency EUR
```

`now` resolves once to a persisted UTC cutoff. A month means local first-of-month
midnight inclusive to next-month midnight exclusive, stored as UTC instants.
Audit/backfill `--through` is an ingestion cutoff; the opening packet lists any
later-dated supply/payment activity separately rather than losing its source.
An export command returns its run ID and download checksum. Acknowledgment binds
that checksum, mapping version and actual import reference. A new revision needs
its own acknowledgment and an explicit delta; never reimport all prior postings.

Produce these exact ZIP datasets; fields below are the minimum schema v1:

| File                              | Required columns / content                                                                                                                                                                                                        |
| --------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `manifest.json`                   | Run/revision, supplier snapshot, period/timezone, source account IDs/mode, cutoff/watermarks/completeness, schema/mapping versions, file SHA-256s, row counts                                                                     |
| `sales.csv`                       | `document_id,issuer,document_kind,document_number,tenant_id,issued_at,tax_point_at,service_from,service_to,currency,net_minor,vat_minor,gross_minor,net_eur_cents,vat_eur_cents,gross_eur_cents,tax_treatment,source_document_id` |
| `sales_lines.csv`                 | Document/line IDs, description, quantity/unit/net unit price, taxable net, rate, VAT, gross, service period, cluster attribution                                                                                                  |
| `payments.csv`                    | Stable allocation ID, payment/provider object IDs, tenant, source document, cash/refund/allocation dates, currencies/FX and allocated gross                                                                                       |
| `journal.csv`                     | `posting_id,event_id,posting_date,account_key,debit_eur_cents,credit_eur_cents,source_type,source_id,document_number,currency,original_minor,run_id`; each event balances, posting IDs survive re-export                          |
| `vat.csv`                         | Reviewed return classification, treatment/rate, tax period, signed taxable net and VAT EUR, source document references; unknowns go to exceptions                                                                                 |
| `icp.csv`                         | Period, customer country/VAT ID, signed qualifying service net EUR and source documents                                                                                                                                           |
| `prepaid.csv`                     | Tenant/lot, opening, funding, consumption, refund, adjustment, closing net EUR liability, source tax document and legacy status                                                                                                   |
| `receivables.csv`                 | Tenant/document, original gross, credits, cash/prepaid allocations, open gross, due date and age bucket                                                                                                                           |
| `operator_payables.csv`           | Owner/cluster, source accrual/document, net payable, tax-evidence state, paid/open; flag missing supplier invoices/self-billing evidence                                                                                          |
| `provider_movements.csv`          | Account/mode, balance ID/category/source object, signed gross/fee/net, currency, dates, actual payout membership, mapped customer source                                                                                          |
| `payouts.csv`                     | Account/payout, mode, currency/amount, status, initiated/arrival, automatic batch proof if available, bank reference/match and reversal                                                                                           |
| `controls.json`, `exceptions.csv` | Equations and component totals by currency; stable exception code, source IDs, affected amount, severity and required action                                                                                                      |
| `documents/`, `evidence/`         | Original PDFs/credit notes; raw report/import evidence; provider fee documents or expected-availability register                                                                                                                  |

An incomplete export is marked `posting_ready=false`, returns exit code 2 and
omits `journal.csv`; its diagnostic schedules retain unknown values as null.
Only a complete, approved export includes `journal.csv` as a journal import feed; other CSVs support reconciliation.
`accounts.json` maps semantic keys (`receivables`, `revenue_streaming`,
`vat_payable_nl`, `stripe_clearing`, `mollie_clearing`, `prepaid_deferred`,
`subscription_deferred`, `provider_fees`, `transfers_in_transit`,
`operator_payables`, `fx_gain_loss`) to DDVTech ledger codes. Confirm package
column mapping once using a fixture import; this is the only package-specific
step in v1. Cross-period subscription revenue uses the agreed service-period
schedule, rather than recognizing revenue when Stripe pays out.

Collect provider fee PDFs from the provider's supplied documents/report access;
where no supported API exists, accept an authenticated admin attachment with
account/period/hash metadata. An unavailable monthly fee invoice gets a due date
and owner; never fabricate one from transaction fees.

#### F. Exact readiness checks and test fixtures

`finance readiness` returns exit code 0 only when all critical checks pass;
otherwise exit code 2 with machine-readable reasons. This is accounting
readiness, not a provider payout permission. It never submits a withdrawal.

- Coverage: zero `MISSING_TAX_DOCUMENT`, `UNKNOWN_TAX_TREATMENT`,
  `DUPLICATE_SUPPLY`, `UNMAPPED_CUSTOMER_CASH`, `DOCUMENT_COLLECTION_MISMATCH`.
- Funding: zero `PREPAID_BASIS_UNKNOWN` or liability-control differences.
- Provider: every required page/source complete; opening plus signed movements
  equals closing per account/currency. Automatic batches tie exactly; manual
  payouts use the balance equation. Available/pending/reserves remain separate.
- Accounts: every journal event and full run balance; all source-document and
  payment control totals tie; current revision has an import acknowledgment.
- Historical corrections: no pending correction approval or unissued required
  correction. Pending legitimate receivables/funds are reported, not forced to zero.
- After payout, separately verify bank receipt and clear transfers in transit.

Add named tests with these exact expected assertions:

| Proposed test                              | Expected assertion                                                                                                                      |
| ------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------- |
| `TestInvoiceNLNet100Collects121`           | Net=10000, VAT=2100, gross/outstanding=12100, one collection=12100                                                                      |
| `TestPaymentDoesNotReduceTaxableBase`      | €30 gross allocation leaves net/VAT/gross unchanged and outstanding=9100                                                                |
| `TestReverseChargeRequiresEvidence`        | Qualifying evidence yields zero VAT plus wording/IDs; timeout on unresolved claim yields review and zero new collection attempts        |
| `TestIssuedDocumentSurvivesProfileChange`  | Changing current supplier/customer data leaves PDF SHA-256 and tax totals identical                                                     |
| `TestStripeRecurringDocumentImportedOnce`  | Finalized/paid/replayed/out-of-order events create one sales document, original number/PDF and independent payment allocation           |
| `TestStripeFirstRenewalAndUpgradeTax`      | Checkout/subscription rate params match the frozen decision; renewal and net/tax proration totals equal provider source facts           |
| `TestTaxHoldSurvivesMissingInvoiceWebhook` | An unresolved profile has a confirmed provider-side collection hold; a due draft recovered by polling cannot be charged before review   |
| `TestNLAdvance121Funds100`                 | Cash=12100, VAT=2100, new net balance/liability=10000; 2500 consumption leaves 7500 and emits no second VAT                             |
| `TestLegacyPrepaidNeverSilentlyRevalued`   | Unknown lot remains at original balance, export is blocked with `PREPAID_BASIS_UNKNOWN`, no guessed net/VAT                             |
| `TestCollectionCarryRecognizesSupplyOnce`  | Collection allocation to an earlier invoice cannot create a second sales/tax journal event                                              |
| `TestPartialRefundUsesOriginalTax`         | Refunding half of 12100 credits net=5000/VAT=1050/gross=6050, even after current rate/profile changes; cumulative overcredit rejected   |
| `TestProviderBalanceAndPayout118`          | 12100 charge less 300 fee less 11800 payout leaves zero; reversal restores clearing and creates no revenue                              |
| `TestFinanceImportCompleteness`            | Missing next page/watermark rejects close; replay adds no movement/posting duplicates; one-cent unexplained difference blocks readiness |
| `TestFinanceExportImportIdentity`          | Re-export retains posting IDs; acknowledged content remains immutable; late activity produces explicit delta IDs                        |
| `TestFinanceExportAuthorization`           | Customer and non-finance admin cannot read another tenant's documents or operator account movements                                     |

Add frontend/API acceptance scenarios for:

- One profile save followed by two purchases: reuse the profile and validated
  status without asking the customer to complete billing details again.
- IP country changes with a stable eligible B2B profile: keep the proper
  treatment and do not invalidate it solely due to travel/VPN.
- VAT/profile changes after quote creation: reject the stale quote before any
  new charge and show the revised payable total for acceptance.
- Eligible reverse charge: checkout, provider parameters, original invoice and
  finance export all carry the same zero-Dutch-VAT treatment and wording.
- Validation outage: reuse usable evidence under its validity policy; show an
  inline retry for a new unresolved claim, with no charge submitted.

Extend the existing scenario 27 billing fixture for a taxed Purser invoice and
prepaid switch, and add a provider-finance fixture with saved signed provider
responses for subscription/import/reconciliation behavior. Fixtures must not
hit live Stripe or initiate payouts. Validate an actual Stripe test-mode invoice
PDF separately before production enablement; contract fixtures alone do not
certify provider settings or legal invoice layout.

Use `make sqlc`, `make proto`, and `make graphql-all` for changed contracts and
their consumers; coordinate generation with concurrent editors per AGENTS.md.
Focused `make test-purser`/`make test-cli` runs precede full required checks.
Record red/green evidence in the working log. No staging smoke without a request.

### Historical correction and accounting rules

The opening close covers the first live transaction through the chosen cutoff,
including provider activity with no local match and any payouts already made.
Preserve source data first. For each historic charge determine the original
issuer, supply, agreed net/inclusive price, tax status and evidence available at
the time. Current billing-profile data is not proof of historical validation.

Correction preview emits a manifest row containing source issuer/document,
reason, proposed net/VAT/gross delta, replacement/credit action, evidence,
accountant decision and manifest hash. Applying that exact manifest uses a
stable idempotency key and the original issuer's correction mechanism. Stripe
supplies use Stripe credit/replacement documents; Purser supplies use Purser
corrections. Draft preview does not allocate legal numbers. Application never
silently backdates documents, rewrites originals or initiates a customer recharge.
Subsequent refunds are separate cash actions and need their own authorization.

For historical €100 payments, the correction cannot assume that €21 is still
collectible or that €100 was net. DDVTech's terms and accountant determine whether
the original amount includes VAT or a further amount is owed. Record any required
VAT/ICP return amendment and its filing reference. Corrections update the close
through explicit linked deltas, not deletion of prior exported rows.

Keep invoice gross, credit notes, payment allocations, receivable balance and
amount submitted to a provider as separate values. Discounts reduce taxable
consideration when applicable; payment/prepaid allocations settle the claim.
Collection minimums carry outstanding claims without recognizing revenue/VAT
again. Prepaid statements and payment receipts produce no extra sales posting.
Disputes, provider fees and failed payouts are not automatically tax credits.

For a €100 earned Dutch service, €21 VAT and hypothetical €3 provider fee:

| Event                | Debit                    | Credit                              |
| -------------------- | ------------------------ | ----------------------------------- |
| Invoice              | Receivables 121          | Service revenue 100; VAT payable 21 |
| Customer payment     | Stripe clearing 121      | Receivables 121                     |
| Fee movement         | Provider fees 3          | Stripe clearing 3                   |
| Payout initiated     | Transfers in transit 118 | Stripe clearing 118                 |
| Bank receipt matched | Bank 118                 | Transfers in transit 118            |

For the proposed €100 net prepaid advance, invoice/funding credits deferred
prepaid revenue 100 and VAT payable 21. Consuming 25 debits deferred prepaid
revenue and credits service revenue by 25. Provider fee VAT uses the real supplier
document's treatment; the example does not assume every fee includes deductible
VAT. Monthly fee statements must not duplicate fees already posted from movements.
Ordinary hosting/Livepeer/supplier bills remain purchase invoices in DDVTech's
bookkeeping; Purser supplies its usage attribution and payable evidence.

Confirm marketplace principal-versus-agent treatment and supplier-invoice or
self-billing agreements before classifying operator accruals as tax-complete
payables. Export missing evidence explicitly. Customer VAT never becomes
third-party service revenue merely because the customer paid gross.

Encode issuance deadlines and tax points for regular supplies, continuous
services and advances; do not wait for provider payout to issue a due invoice.
The €10,000 EU consumer threshold has establishment/current/prior-year conditions;
it is not a universal rule. Keep legal invoice sequence uniqueness/continuity
and explain gaps; `nextval` alone does not promise gaplessness. Assess retention
by record class: ordinary Dutch invoice retention is generally seven years,
while OSS can require ten. Preserve existing protected retention until a reviewed
policy changes it, and test document/evidence restore.

## Impact / Dependencies

- Purser schema, SQL query sources and generated outputs via repository codegen;
  tax/document service, PDF/email templates, collections and provider imports.
- Purser protobuf/API and administrator CLI first; GraphQL/frontend follow where
  customers need tax/profile/amount visibility. Update all consumers through
  `make proto` / `make graphql-all` as applicable, including demo generators.
- Schema baseline, Yugabyte placement layout and migrations under the actual
  pending release in `cli/internal/releases/catalog.yaml` (currently its highest
  declared entry is `v0.3.14`; verify reachable tags/state before implementation).
  Do not invent a later version or edit shipped migrations.
- DDVTech bookkeeper: approve account/tax mappings, sample import, historical
  corrections, prepaid policy and marketplace treatment. Accounting package
  choice only blocks its specific adapter, not the portable export.

## Alternatives Considered

- All invoices and recurring collection under Purser: would unify issuance but
  also migrates subscription retries, prorations, saved methods and entitlement
  boundaries. Defer it; fix the present issuer paths and consolidate their records.
- Stripe Tax for recurring supplies: possible later; v1 uses explicitly mapped
  tax rates from the shared decision to avoid two independent tax policies.
- Accounting package as invoice authority: requires migrating issuance and every
  collection path; importing Purser PDFs and reissuing invoices risks duplication.
- Payout totals as sales: loses gross sales, VAT, fees, funding liabilities and
  cutoff differences, and cannot reconcile money still held at the provider.

## Risks & Mitigations

- Wrong historical tax assumptions: inventory first; no automatic repricing,
  fabricated validation, backdating or customer recollection.
- Collection/subscription regression: prove representative provider fixtures,
  retain existing idempotency/recovery behavior, pilot before migration.
- Double taxation/revenue through advances, statements or carried amounts:
  explicit document kind and allocation model with accounting invariants.
- False payout confidence: surface incomplete imports and unresolved variances;
  distinguish a manual payout's balance proof from automatic batch proof.

## Migration / Rollout

1. **Opening close:** inventory history and produce the first accountant packet.
   Correct historical documents through the agreed workflow. Reconcile the
   current provider balance before the first requested payout; match bank receipt
   afterward. Record any current payout settings; do not silently change them.
2. **Shared contract:** agree tax/pricing/prepaid rules and schema; implement
   focused failing tests first, then shared decisions and issued snapshots.
3. **Collection coverage:** pilot taxed Purser collections and Stripe's retained
   recurring issuer path; activate corrected amounts at explicit boundaries
   without overlapping invoice/charge ownership.
4. **Permanent close:** provider import/backfill, reconciliation, portable exports
   and import acknowledgements. Historical unknowns stay visible as exceptions.
5. **Verification and handoff:** reviewed sample import, historical/current close,
   canonical billing/operations documentation and full checks. After approval
   and implementation, merge this design into canonical docs and remove the RFC.

Before changed collection goes live, compare shadow tax totals against approved
fixtures and actual historical examples. Shadow mode must not submit payments,
issue legal invoices, or overwrite existing documents. Rollback can disable new
collection/issuance while preserving recorded liabilities and issued evidence;
it must not revert issued numbers or financial events.

### Required verification

The named red/green fixtures in Implementation contract F are the acceptance
suite. Run `make test`, `make verify-prepush`, and `make verify-stack` on an
isolated remotedev slot for implementation changes, plus
`make validate-migrations` and `make verify-schema-migrations` for schema changes.
Run required lint/generation targets; no hand-edited generated files. Full
verification certifies the exact implementation commit, not this planning draft.
No nightly jobs or staging smoke without a maintainer request.

## Open Questions

Resolve from existing records and DDVTech's bookkeeper during step 0; do not
block the portable design on answers that affect only one adapter:

- Which accounting package/import format and chart of accounts does DDVTech use?
- What do current customer terms say about tax, and which supplies have already
  been invoiced/paid? What historic corrections and filing adjustments are needed?
- What is the approved advance/voucher/deposit classification for spendable
  credit, and how are subscription periods recognized for financial reporting?
- What business evidence and jurisdictions are supported at launch? Are consumer
  transactions admitted, and are any destination registrations required?
- Are third-party marketplace sales active, and what principal/agent and
  invoicing/self-billing arrangements apply?
- Review the proposed prepaid advance classification before activation. Stripe
  recurring ownership is decided above; tax synchronization and document coverage
  are implementation acceptance criteria, not an undecided architecture branch.

## References, Sources & Evidence

Primary online documentation checked 2026-11-04. This design interprets those
sources for the architecture; DDVTech's actual filing treatment requires review.

- [Source] [Belastingdienst invoice requirements](https://www.belastingdienst.nl/wps/wcm/connect/bldcontentnl/belastingdienst/zakelijk/btw/administratie_bijhouden/facturen_maken/factuureisen/factuureisen): required identities, dates, sequential unique numbers and tax breakdown.
- [Source] [Belastingdienst invoice accounting](https://www.belastingdienst.nl/wps/wcm/connect/bldcontentnl/belastingdienst/zakelijk/btw/btw_aangifte_doen_en_betalen/bereken_het_bedrag/hoe_berekent_u_het_btw_bedrag/factuurstelsel): tax-period rules are not determined by provider payout timing.
- [Source] [Belastingdienst digital services](https://www.belastingdienst.nl/wps/wcm/connect/bldcontentnl/belastingdienst/zakelijk/btw/zakendoen_met_het_buitenland/goederen_en_diensten_naar_andere_eu_landen/btw_berekenen_bij_diensten/wijziging_in_digitale_diensten_vanaf_2015/): consumer place-of-supply rules and conditional threshold.
- [Source] [Government guidance on services abroad](https://ondernemersplein.overheid.nl/internationaal-ondernemen/douane-en-belastingen/btw-bij-diensten-in-het-buitenland/): general cross-border B2B treatment, reverse charge and ICP.
- [Source] [Belastingdienst invoice retention](https://www.belastingdienst.nl/wps/wcm/connect/bldcontentnl/belastingdienst/zakelijk/btw/administratie_bijhouden/facturen_maken/uw_facturen_bewaren) and [government administration guidance](https://ondernemersplein.overheid.nl/wetten-en-regels/administratie-bijhouden-en-bewaren/): ordinary retention and special ten-year classes.
- [Source] [Stripe subscription lifecycle](https://docs.stripe.com/billing/subscriptions/overview): subscription collection generates provider invoices.
- [Source] [Stripe tax rates](https://docs.stripe.com/tax/tax-rates) and [recurring tax-rate configuration](https://docs.stripe.com/billing/taxes/collect-taxes?tax-calculation=tax-rates): explicit manual rates, Checkout/subscription configuration and credit-proration references.
- [Source] [Stripe collection holds](https://docs.stripe.com/billing/subscriptions/pause-payment): keeping future invoices in draft, already-open invoice caveats and draft recovery.
- [Source] [Stripe balance report](https://docs.stripe.com/reports/balance) and [payout reconciliation report](https://docs.stripe.com/reports/payout-reconciliation): full balance evidence and automatic versus manual/instant payout limits.
- [Evidence] Repository paths and symbols in Current state; also `pkg/database/sql/schema/purser.sql`, `api_billing/internal/database/queries/`, and `api_billing/internal/operator/credit.go`.
- [Reference] [Cross-cluster billing](../architecture/cross-cluster-billing.md), [schema migration standard](../standards/schema-migrations.md), [remote verification](../development/remote-development.md). Local context also includes the ignored working logs `PLAN_PURSER_ASSURANCE.md` and `PLAN_BILLING_METERING_V030.md`.
