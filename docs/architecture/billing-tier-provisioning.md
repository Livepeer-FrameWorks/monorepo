# Billing Tier Provisioning - Account Creation & Cluster Access

Billing tiers drive cluster access. When an account is created or promoted, the billing tier determines which platform clusters the tenant can access. Purser orchestrates both billing subscription creation and cluster provisioning via Quartermaster.

## Architecture

```
                    ┌─────────────┐
                    │  Commodore  │
                    │  (Register) │
                    └──────┬──────┘
                           │ InitializePostpaidAccount
                           ▼
                    ┌─────────────┐       ┌────────────────┐
                    │   Purser    │──────▶│  Quartermaster  │
                    │             │       │                 │
                    │ 1. Resolve  │       │ BootstrapClusterAccess
                    │    tier     │       │ UpdateTenant     │
                    │ 2. Create   │       │  (primary_cluster)│
                    │    sub      │       └────────────────┘
                    │ 3. Cluster  │
                    │    access   │
                    └─────────────┘
```

## Service Responsibilities

| Service       | Role                                                     | Data                                                       |
| ------------- | -------------------------------------------------------- | ---------------------------------------------------------- |
| Commodore     | Triggers billing init during Register                    | Calls Purser after user creation                           |
| Purser        | Resolves tier, creates subscription, provisions clusters | `billing_tiers`, `tenant_subscriptions`, `cluster_pricing` |
| Quartermaster | Materializes tier-derived grants and primary routing     | `tenant_cluster_access`, `tenants.primary_cluster_id`      |

## Data Flows

### New Email Account (Postpaid)

```
1. Commodore.Register creates tenant via Quartermaster
2. Commodore calls Purser.InitializePostpaidAccount(tenant_id)
3. Purser resolves tier WHERE is_default_postpaid = true
4. Purser creates subscription (billing_model=postpaid)
5. Purser.ensureTierClusterAccess:
   a. Queries cluster_pricing for eligible platform clusters
   b. Materializes `platform_tier` access via Quartermaster.BootstrapClusterAccess
   c. Sets highest-tier-level cluster as primary via Quartermaster.UpdateTenant
```

### New Wallet Account (Prepaid)

```
1. Commodore.GetOrCreateWalletUser creates tenant via Quartermaster
2. Commodore calls Purser.InitializePrepaidAccount(tenant_id, currency)
3. Purser resolves tier WHERE is_default_prepaid = true
4. Purser creates subscription (billing_model=prepaid) + prepaid balance
5. Purser.ensureTierClusterAccess provisions clusters (same as above)
```

### Prepaid → Postpaid Promotion

```
1. Gateway calls `Purser.PromoteToPaid(tenant_id, optional tier_id)`.
2. Purser starts a transaction and locks the tenant subscription `FOR UPDATE`.
3. An explicit active postpaid-eligible tier is honored; otherwise Purser
   resolves `is_default_postpaid = true`.
4. Free needs verified email but no postal profile/provider. Paid postpaid tiers
   require complete billing identity and confirmed Stripe/Mollie collection.
5. In the same transaction Purser closes the prepaid phase (see Prepaid
   statements below) and commits prepaid → postpaid while retaining prepaid
   credit.
6. Purser rereads the canonical committed tier, then reconciles clusters and
   invalidates caches. A same-target retry is idempotent.
```

`AdminAssignTier` (`frameworks admin billing set-tier`) closes the prepaid
phase the same way when it moves a tenant from prepaid to postpaid, and
closes the postpaid phase with a finalized invoice when it moves a tenant
from postpaid to prepaid (see Switch to prepaid mid-period below).

## Prepaid Statements

A prepaid tenant pays usage from its EUR prepaid balance as each usage report
arrives: `processPrepaidUsage` rates the billing period's cumulative usage and
records the marginal amount in `prepaid_usage_settlements` with a `usage`
balance transaction. Its period therefore never ends in an invoice. The
month-end job (`finalizeSubscriptionPeriods`) closes a prepaid tenant's period
with a **prepaid statement** instead: a `billing_invoices` row with
`document_kind = 'prepaid_statement'`, number `STM-…`, status `paid`, amount
0 and no invoice credit (a check constraint holds a statement to both).

- It itemizes the period's usage at the rated prices the settlements used, as
  invoice line items.
- `usage_details.statement` states what the prepaid settlements took from the
  balance for the period (the authority for what the usage cost), the rated
  total, top-ups and usage deductions posted in the period, the balance at the
  period's start and end, and any monthly fees. A rated total that differs from
  what was paid by more than a cent is logged as a warning; late corrections and
  reports settled after a price change are the usual cause.
- The statement settles what the phase's usage still owes the balance: it
  rates the phase's cumulative usage the way the settlements do and posts the
  difference to what they took as one more settlement (report id
  `statement-…`), under the subscription row lock. Usage no settlement paid,
  such as a report processed under the model the tenant was leaving or after
  the last settlement, is paid here; a settlement that took usage of another
  phase is returned. `usage_details.statement.settled_with_statement_cents`
  states it.
- Any invoice credit an earlier postpaid draft of the period still held returns
  to the balance, and such a draft becomes the statement.
- Monthly fees Purser bills itself (the tier base fee when it is not collected
  by a provider subscription, for the phase's share of a split period, and
  Purser-invoiced monthly cluster fees from `cluster_subscriptions`, prorated
  like on invoices) are charged to the prepaid
  balance with the statement, as one `usage` balance transaction with
  `reference_type = 'prepaid_statement_fees'`. The balance may go negative; the
  prepaid thresholds suspend the tenant then, as for usage.
- Marketplace operator credits accrue from the statement's lines, since the
  balance paid them.
- A `prepaid_statement` email (`invoice_email_outbox`) states that nothing is
  due. Statements are listed with the billing documents (kind
  `prepaid_statement`), not with invoices, and cannot be paid.
- The subscription advances to its next period in the same transaction.

**Phases and receipt time.** Switches between prepaid and postpaid split a
billing period into phases, each closed by its own document. A usage record or
usage correction belongs to the phase in force when it reached Purser
(`created_at`, written by the database clock), and switch times come from the
same clock. A phase's document rates the rows of its window plus rows of
earlier phases of the split period that reached Purser after the phase began
(late reports and corrections); a row that reached Purser before the phase
began belonged to the phase before. `CollectPhaseUsage` holds the rule for
every document: postpaid drafts and invoices, prepaid settlements and
reservations, statements, and which corrections a document marks applied.

The boundary is a lock, not a clock race. A usage report is written in one
transaction (`receiveUsageSummary`) that holds the subscription row
`FOR SHARE` (`LockSubscriptionForUsage`) and reads the billing model there;
a switch holds the row `FOR UPDATE`. A report that holds the row when a switch
reaches it commits first, and the switch's count of received usage
(`CountPhaseUsageReceivedBefore`) sees it and rates again. A report that takes
the row after a switch committed is dated no earlier than the latest switch
(`created_at` is at least the end of the latest closing document), so it
belongs to the phase that followed, and the model it read is that phase's.
Prepaid settlement (`processPrepaidUsage`) takes the same row lock and settles
nothing once the tenant has left prepaid; the closing statement settled that
phase. Each row is therefore in exactly one phase and paid exactly once.

**Base fee of a split period.** Each phase of a split period pays its plan's
base fee for its own time: the share runs from the phase's start to its end
in whole seconds of the split period, each end rounded to the cent, so the
shares of the phases add up to exactly one fee (`usagePhase.baseFeeShare`).
This applies to closing invoices and statements, to the month-end document of
the rest of the period, to drafts, and to the advance base-fee invoice of a
period a switch from prepaid started.

**When invoice credit is taken.** A postpaid tenant's prepaid balance stays
on the balance while its period runs: the draft (`updateInvoiceDraft`) states
the gross amount, holds no credit, and returns any credit its key still holds.
The invoice takes its credit once, in the transaction that finalizes it
(`writePostpaidInvoiceTx`, at month end, an early close, or the switch to
prepaid): the gross amount, bounded by the balance then. Manual-review holds,
statements, and switches hold none.

**Invoice credit keys.** Each billing document holds its prepaid invoice
credit under its own key (`documentInvoiceCreditKey`): the first document
starting in a month under the month's key (`Invoice credit: YYYY-MM`, the key
every document had before), a later document of the same month, after an
early close or a switch, under its exact period start. No document's
reconciliation takes or returns another's credit.

**Switch to postpaid mid-period.** The prepaid phase is closed at the switch:
the switch path rates the closing statement for [period start, switch) before
its transaction and writes it inside it (flagged `closes_prepaid_phase`), and
the postpaid period starts at the switch and ends where the prepaid period
would have. Usage of the phase that reached Purser while the statement was
rated returns `ErrPrepaidPhaseChanged`, and the switch rates again. The
postpaid rest of the period pays the base fee for its share of the period.
Invoices record `prepaid_settled_usage_excluded` in `usage_details`: they rate
only their own phase.

**Switch to prepaid mid-period.** A postpaid plan's entitlement ends at the
switch, so what it used is owed then. `AdminAssignTier` (the only path that
moves a tenant from postpaid to prepaid; `purser bootstrap` refuses a billing
model change on an existing tenant and points at `set-tier`) closes the
postpaid phase with an invoice: `PreparePostpaidPhaseClose` rates
[period start, switch) before the switch transaction, at the postpaid tier's
prices and under the operator grant in force, with the base fee for the
phase's share of the period and Purser-invoiced monthly cluster fees for
their active time (a base fee a provider subscription or an advance base-fee
invoice collects is not charged again). The period is the one month-end
finalization and drafts use, Mollie-anchored when Mollie collects the
subscription. `CommitTx` writes it inside the transaction under the
subscription row lock: the open draft of the period becomes the finalized
invoice (flagged `closes_postpaid_phase`), also a draft that started elsewhere
than the phase (its credit returns first, under its own key), so no draft
stays open; the prepaid period starts at the switch and ends where the
postpaid period would have. An advance base-fee invoice of the period has the
unused share from the switch returned once (`returnUnusedBaseFeeTx`, the
tier-change rule): a paid one credits it to the prepaid balance
(`reference_type = 'base_fee_unused_share'`), an unpaid one is reduced to its
used share; the invoice records `unused_share_returned`, so no later base-fee
invoice of the period credits it again. A payment of it still pending refuses
the switch with `FailedPrecondition`.
The invoice is collected like any postpaid invoice: email, `invoice_created`,
and after commit the off-session charge through the subscription's provider,
or payment by transfer (`AdminRecordInvoicePayment`). Charges the collection
minimum deferred are charged to the prepaid balance (`collection_carry`),
since no later postpaid invoice would collect them. A subscription, tier,
provider setup, operator grant (including one that expired) or count of
received usage that changed since the rating returns `ErrPostpaidPhaseChanged`;
`AdminAssignTier` rates again, up to three times, then returns `Aborted`.
Unresolved cluster pricing refuses the switch. Operator grants never change
the billing model, so a grant expiring is not a switch.

**Period after a split.** Whichever document closes the last part of a split
period, the next period is as long as the whole period the switches split,
not as its last part: `nextBillingPeriodEnd` follows the chain of documents
that closed a phase (`GetSplitPeriodStart`: statements flagged
`closes_prepaid_phase` and invoices flagged `closes_postpaid_phase`, one per
switch) back to the period's start. A period of whole calendar months is
followed by as many calendar months (September is followed by October, not by
30 days); any other period by one calendar month from its end.

**Earlier double charges.** Before statements, month-end finalization invoiced
prepaid tenants and rated again the usage their balance had paid, taking the
total as invoice credit from the same balance or charging it to the card; a
tenant promoted mid-period was invoiced for the prepaid part as well.
`frameworks admin billing prepaid-double-charges` (Purser
`AdminListPrepaidDoubleCharges`) lists those invoices read-only with the amount
charged twice: the invoice's usage up to what the settlements took for the
period. Refunds are an operator decision.

## Collection Readiness and Operator Grants

A postpaid paid tier admits rated work only while its charges can be collected.
`postpaidCollectionReadiness` (`api_billing/internal/grpc/collection_readiness.go`)
is the one rule, used by tenant admission, billing status and the self-serve tier
change:

1. A Stripe or Mollie setup that can charge off-session: a provider subscription,
   or the saved Stripe card or valid Mollie mandate that advance-billed tenants
   run on. Provider `stripe` or `mollie`.
2. Otherwise an operator grant in force (`purser.subscription_operator_grants`)
   with `collection = 'invoice'`: the operator collects invoices by hand and
   records payments (`AdminRecordInvoicePayment`). Provider `operator`.
3. Otherwise a grant under which the tenant owes nothing: base fee 0 and usage
   waived. Provider `operator`.

An operator grant is one row per subscription, set with
`frameworks admin billing grant set` (`AdminSetBillingGrant`):

- `base_price` replaces the tier's base fee in `LoadEffectiveTier`, so every
  invoice and base-fee path sees it. A grant that sets it is refused while a
  Stripe or Mollie subscription exists, because that subscription bills the
  tier's price on the provider's side; while it applies, Stripe Checkout runs in
  setup mode (card only) and Mollie subscriptions are refused.
- `waive_usage` rates usage at zero for that tenant, the per-tenant form of
  `WAIVE_USAGE_CHARGES`. Quantities and would-have-cost amounts stay on the
  invoice.
- `expires_at` is evaluated at read time: an expired grant stops applying
  everywhere at once, with no job, and stays on record.

Grant changes record `billing.subscription_updated` with `changed_fields =
["operator_grant"]` and the operator's reason. Metering and rating are unchanged
by a grant.

## Default Tier Configuration

Default tiers are configured via boolean flags on `purser.billing_tiers`:

| Flag                  | Meaning                                 | Current default  |
| --------------------- | --------------------------------------- | ---------------- |
| `is_default_prepaid`  | Assigned to wallet/x402 accounts        | `payg` (level 0) |
| `is_default_postpaid` | Assigned to email registration accounts | `free` (level 1) |

Exactly one tier should have each flag set to `true`.

## Cluster Eligibility

`ensureTierClusterAccess` first asks Quartermaster for platform-official cluster IDs, then filters `purser.cluster_pricing` for clusters the tier can access:

```sql
WHERE cluster_id = ANY(<quartermaster_official_cluster_ids>)
  AND required_tier_level <= <tier_level>
  AND (allow_free_tier = true OR <tier_level> > 0)
```

Purser grants each eligible cluster through Quartermaster's service-token `BootstrapClusterAccess` RPC. The cluster with the highest `required_tier_level` is set as primary (most capable cluster the tier grants access to).

## Key Files

- `api_billing/internal/grpc` - `ensureTierClusterAccess`, `InitializePrepaidAccount`, `InitializePostpaidAccount`, `PromoteToPaid`
- `api_control/internal/grpc` - `Register` calls `InitializePostpaidAccount`
- `pkg/proto` - RPC definitions and response messages
- `pkg/database/sql/schema` - `billing_tiers` (with default flags), `cluster_pricing`
- `pkg/database/sql/seeds/static` - Default flag assignments

## Gotchas

- Quartermaster's `CreateTenant` still auto-subscribes to the single `is_default_cluster=true` cluster as a safety net and sets `official_cluster_id`. Purser's later cluster provisioning is idempotent, so overlap is harmless.
- Tier-specific paid collection must be completed before selecting a non-Free
  postpaid tier, unless an operator grant stands in for it. Free activation
  remains provider-free.
- `AdminAssignTier` (`set-tier`) changes the tier only. A paid postpaid tier
  assigned that way without collection or a grant is refused rated work.
- `PromoteToPaid` honors an explicit active, postpaid-eligible `tier_id`; an
  empty value selects the default postpaid tier.
