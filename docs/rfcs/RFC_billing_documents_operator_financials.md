# RFC: Reconciled financial closes and journal exports

## Status and scope

Proposed. Billing tax, invoice issuance, original provider documents, prepaid
funding and the read-only evidence archive are implemented and described in
[Billing tax and financial evidence](../architecture/billing-tax.md) and the
[operator guide](../../website_docs/src/content/docs/operators/billing-financials.mdx).
This RFC retains only the unimplemented accounting close and journal proposal.

## Problem

An evidence archive records what the services and provider APIs returned. It is
not a reproducible accounting close: rows can arrive late, statuses change,
provider fees and payouts need reconciliation, and bank receipt is external.
DDVTech needs a neutral bookkeeping handoff without replacing its general ledger.

## Proposed close bundle

A close would retain an immutable scope, cutoff, source versions, hashes,
exceptions, reviewer identity and ledger mapping. It would link original sales
and credit documents to payments, provider movements, payout membership and bank
matches, separating currencies and live/test provider accounts.

Supporting schedules would include sales/credits, receivables, prepaid liability,
subscription revenue periods, VAT, provider fees, refunds/disputes, FX, transfers
in transit and applicable operator payables. Missing evidence must stay explicit.
Provider fees require their real supplier documents; a fee movement is not an
invented purchase invoice. Marketplace accruals need an agreed principal/agent
and supplier-invoice or self-billing treatment.

Balanced journal entries would use stable posting identities and a neutral
account mapping. `journal.csv` would only be available after all required control
equations pass and an authorized reviewer acknowledges the exact bundle hash.
An incomplete bundle would retain exceptions and omit the journal. Repeating an
export must reproduce its accepted close rather than silently reading new facts.

No close, sync, journal, bank-match or payout-readiness command described here
exists merely because it is proposed. The implemented command is
`frameworks admin billing finance audit`; it always reports posting-ready false.

## Accounting controls

For a EUR 100 earned service, EUR 21 VAT and a hypothetical EUR 3 provider fee:

| Event            | Debit                    | Credit                              |
| ---------------- | ------------------------ | ----------------------------------- |
| Invoice          | Receivables 121          | Service revenue 100; VAT payable 21 |
| Payment          | Provider clearing 121    | Receivables 121                     |
| Provider fee     | Fees 3                   | Provider clearing 3                 |
| Payout initiated | Transfers in transit 118 | Provider clearing 118               |
| Bank receipt     | Bank 118                 | Transfers in transit 118            |

An advance credits deferred revenue until service consumption. Payment receipts
and prepaid statements must not create duplicate sales. Provider fee VAT comes
from the real supplier document. Disputes, failed payouts and provider fees are
not automatically tax credits. Payout timing does not determine revenue or VAT.

Historic corrections would retain original documents, linked deltas, reasons,
issuer-specific credit/replacement records and accountant decisions. They would
not rewrite issued facts, backdate documents or recharge previously paid
customers. Cash refunds are separate authorized actions.

## Acceptance criteria

- Sales, credits, payments and receivables reconcile by currency.
- Funding, consumption, refunds and remaining prepaid liability reconcile.
- Provider gross, fees, net, reserves and payouts reconcile to original reports.
- Payouts are matched to bank evidence; unmatched transfers remain exceptions.
- Missing supplier/marketplace evidence and historical coverage stay visible.
- Close replay reproduces its source identities, totals and hashes.
- A reviewed fixture imports into the selected bookkeeping process without
  duplicate postings; journal entries balance and retained evidence is restorable.

This proposal does not authorize payout changes, tax filings or production
corrections. Detailed implementation and bookkeeping acceptance remain separate
work from the implemented billing release.
