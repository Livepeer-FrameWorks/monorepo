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
unused share settled once by `settleUnusedBaseFeeTx`: an unpaid invoice is
reduced to its used share; unused paid fees are forfeited with an invoice audit
record and never become spendable prepaid credit. The shipped
`unused_share_returned` ledger key prevents applying the same share again.
A payment still pending refuses the switch with `FailedPrecondition`.
The invoice is collected like any postpaid invoice: email, `invoice_created`,
and after commit the off-session charge through the subscription's provider,
or payment by transfer (`AdminRecordInvoicePayment`). Charges the collection
minimum deferred are charged to the prepaid balance (`collection_carry`),
since no later postpaid invoice would collect them. A subscription, tier,
provider setup, operator grant (including one that expired) or count of
received usage that changed since the rating returns `ErrPostpaidPhaseChanged`;
`AdminAssignTier` rates again, up to three times, then returns `Aborted`.
Unresolved cluster pricing refuses the boundary. A grant ending can also restore
a fallback tier and change the billing model, such as an enterprise trial returning to PAYG.
Model switches keep the original cycle for monthly cluster fees too. Cumulative
active-time positions are rounded at each phase boundary, so the split charges
add up to the same cents as one unsplit invoice, including cancellation and
reactivation inside the cycle. A paid upgrade establishes its fresh monthly
cycle before subsequent fees are rated.

Advance-billed provider activation closes the old billing period and starts a
new month. Its closing invoice is not marked `closes_postpaid_phase`; otherwise
the new base fee would be prorated against the old period again. The unused
share of the prior advance invoice still nets against the new base-fee invoice.
Grant and model switches retain the original period and its phase chain.

Self-service paid upgrades follow the same fresh-month rule. `ChangeBillingTier`
records one durable upgrade under the subscription lock. Checkout, grants and
other pricing changes wait until its provider outcome and usage boundary are
known. Stripe replaces its existing subscription item, resets renewal and
immediately charges the new month with its retained paid-fee proration credit. Mollie changes the
existing recurring amount and next payment date; Purser invoices the new month
less the unused paid share of the previous fee. Mollie's date-only renewal date
is the new period's end. Calendar arithmetic caps a missing month-end day at
February 28/29 (or the target month's last day); matching recorded provider
ends retain the precise local upgrade start. Advance collection uses that same invoice ledger and
the current ECB rate, locked onto the replacement invoice when it is issued.

The closing usage invoice, new local period and any Purser-issued replacement
base fee and credit commit together. The unused fee comes from the paid invoice
or provider contract, rather than the tier's current list price or a grant-split
usage phase. It is a billing credit, not a cash refund. An in-flight payment of the
previous fee refuses the upgrade before provider pricing changes. Stripe also
requires its previous subscription invoice to be paid before issuing a proration
credit. A grandfathered Stripe price uses the monthly EUR base line actually
paid for that native subscription and item, rather than today's catalog price.
Missing or ambiguous payment evidence refuses the upgrade.
An ambiguous provider update or incomplete metering keeps the upgrade
pending. Retrying the same tier
uses the original upgrade identity and boundary; an already-applied tier is a
no-op. Provider metadata locates a price change whose response was lost, and
callbacks preceding a committed Stripe upgrade cannot restore its old tier or
status. The provider inbox owns retry leases, backoff and alerts; committed
pricing is not rolled back by cache delivery failures. Permanent account closure
cancels unapplied upgrades. A confirmed provider purchase before the closure
cutoff applies pricing up to that cutoff; an already-issued replacement invoice
is still collected. A Stripe purchase starting at or after the cutoff is
compensated using only its replacement invoice. Closure never refunds unused
time on an activated month and prevents callbacks from reopening billing.

Upgrades refuse scheduled downgrades and cancelled or pending-cancellation
subscriptions; the customer must explicitly cancel the pending change or resume
first. Mollie upgrades wait for local rollover when the previous period has
ended, even if Mollie already collected the next renewal. Refunded or disputed
provider fees cannot fund a new credit. Metering is checked at the accepted
boundary before provider mutation; retries retain that boundary.

An operator can inspect `admin billing recovery list --tenant-id <uuid>` and run
`admin billing recovery upgrade --tenant-id <uuid> --upgrade-id <uuid> --reason
"Reconcile purchase"`. Recovery previews and confirms first. It only locates
existing provider work, cancels an unapplied request or finishes an applied
purchase; it never initiates another purchase. The purchase retains the first
recovery operator and reason. Provider outages retain the pricing fence until
that existing purchase can be resolved.

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

An operator grant is the current arrangement for one subscription, set with
`frameworks admin billing grant set` (`AdminSetBillingGrant`).

- `base_price` overrides the subscription tier's base fee. Without an override,
  the current subscription tier supplies the price, including after an operator tier change.
  `LoadSubscriptionEffectiveTierAt` selects the grant and tier for the invoiced phase.
  Provider subscriptions must be cancelled before overriding their base fee.
- `waive_usage` rates metered usage at zero while preserving quantities and would-have-cost amounts.
- Assigning a trial tier remembers the original tier as fallback. `--fallback-tier`
  chooses another active fallback. Renewal preserves it; an explicit `set-tier`
  change clears it. Ending the grant restores it only if the assigned tier is still current.
- Pricing changes close the preceding billing phase transactionally. Changing only
  a deadline or reason leaves the period open; an identical set is a no-op.
- A minute sweep ends expired grants at their deadline, archives their arrangement,
  and restores the fallback. A failed boundary is retried with five-minute backoff,
  alerted, and holds invoicing for that tenant while other tenants continue.
- `grant list --include-expired` reads current and archived grants; `grant revoke`
  ends the current arrangement immediately through the same billing-boundary path.

The subscription lock fences grant changes, historical pricing and billing documents.
`billing.subscription_updated` records changed arrangements. Durable refresh work
reconciles tenant entitlements and invalidates caches after commit. Signed media
permission ends at a future grant deadline, including during control-plane outages.

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

Purser grants each eligible cluster through Quartermaster's service-token
`BootstrapClusterAccess` RPC. The cluster with the highest `required_tier_level` is set
as primary (most capable cluster the tier grants access to).

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
- `AdminAssignTier` (`set-tier`) changes tier and billing model while preserving
  an active grant. A tier change clears its fallback. A paid postpaid tier
  without collection or a grant is refused rated work.
- `PromoteToPaid` honors an explicit active, postpaid-eligible `tier_id`; an
  empty value selects the default postpaid tier.

Provider tier and billing-model transitions use one subscription-row-locked
policy. Stripe checkout/activation, Mollie subscription activation and advance
billing after Stripe setup or Mollie mandate completion refuse an outstanding
operator grant. A renewal of the same established Stripe subscription updates
only status and period dates while keeping its grant. Cancellation retains the
arrangement for recoverable provider states. Revoke or expiry on a cancelled
subscription archives it at the cleanup time without a tier change or closing
document; new checkout can proceed after that cleanup. Bootstrap defers a
cancelled subscription with an actionable recovery error while reconciling the
other tenants, and reports a non-zero result.

All ordinary pricing changes use the subscription row lock before reading its
operator grant. Self-service changes, promotions and scheduled downgrades refuse
an outstanding grant. Bootstrap keeps the granted tier and model when the
billing declaration has no live grant change; its cluster entitlement operations
use that retained tier. An operator's explicit grant/tier lifecycle closes the
required billing boundary before replacing the arrangement.

A definitive Mollie activation refusal queues immediate compensation for paid,
unused first-payment funding. The subscription lock excludes concurrent
activation; activated funding and established native subscriptions are never
refunded by that refusal. Compensation is limited to tier eligibility, account
closure, an outstanding grant or a provider-confirmed terminally invalid mandate.
Invalid-mandate recovery covers unused funding on that mandate across tiers;
other mandates keep their funding.

Repairable conditions, including incomplete billing details, unpaid invoices,
pending checkout or fallback, pending mandates, changed request credentials and
cancelled setup retain funding for **retry activation**. They do not require a
second first payment. Reserving another Mollie first payment checks unused paid
funding under the subscription lock and refuses until it is activated or refunded.
The locked activation path cancels only the unactivated native subscription;
after cleanup, retrying activation links and consumes the original payment once.
The receipt check verifies the paid purchase's tier, customer and currency both
before creating a native subscription and at locked activation. Paid service
cannot use a zero-amount mandate setup or funding for another tier. A later
renewal-price change on the same tier does not rewrite the original first fee.

The first-payment fence owns receipts with a recorded mandate ID. A historical
paid receipt without that binding is not treated as unused just because its
activation date is NULL; its delivery history stays with historical recovery.
A returning customer can purchase a new month without an operator backfill.
An old native receipt still bound to a provider subscription is resolved before
it is replaced. If Mollie confirms that subscription was cancelled or completed,
the old native receipt is retired and the same request reserves a new checkout.
An active subscription or failed provider read keeps the fence. The ended
subscription never consumes the new payment.
Activation links only succeeded, unactivated funding with no recovery in flight
for the same tenant, customer, mandate, tier and currency. If no unused payment
remains, the customer is asked to complete a new first payment. If paid funding
is retained for a different setup, the customer gets retry or billing-help guidance.

Replacing a tenant's Mollie customer does not transfer unused funding to the new
customer. That funding keeps blocking another first payment until it is used
under its original binding or an operator confirms recovery of the original
payment. Reconcile any linked native checkout first, then the funding receipt;
after compensation completes, the customer can start a new first payment.

Retained funding has no automatic age-based refund. If the customer abandons
activation, account closure is the only automatic refund unless a definitive
refusal is subsequently observed. An operator can request earlier compensation
through confirmed `admin billing recovery checkout`; native cleanup must finish
before its funding can be refunded.

Mollie's [mandate schema](https://docs.mollie.com/reference/create-mandate)
exposes `valid`, `pending` and `invalid`. Its
[revocation endpoint](https://docs.mollie.com/reference/revoke-mandate) cancels
linked subscriptions. Its [lookup endpoint](https://docs.mollie.com/reference/get-mandate)
also returns 404 for missing entities, without a documented distinction for
revocation. A lookup failure or omitted mandate alone is not evidence of a
terminally invalid mandate. Purser retains that funding, reports retry guidance
and permits confirmed operator recovery if the mandate cannot be restored.

Checkout compensation rechecks the locked subscription and receipt snapshot
after provider reads, before each cancellation or refund request. A concurrent
change preserves the recovery fence for a fresh retry and does not enqueue a
refund from the stale decision. Provider I/O runs outside database transactions.

Recovery probes a cancelled Mollie collector using the customer bound to its
native checkout receipt. Missing bindings fail closed during automatic recovery.
An operator can preview and confirm `admin billing recovery checkout` to cancel
the new, unactivated purchase without inspecting or altering the previous
collector. Confirmed recovery does not attempt activation. The native receipt's
unique provider-subscription constraint prevents conflicting customer bindings;
violations of that database invariant require integrity repair rather than a
guessed customer ID.

Checkout reserves its durable provider intent under the same lock before any
external call. Grant creation refuses while Stripe checkout or Mollie first
payment/subscription setup is outstanding, including an ambiguous provider call.
Reconcile that checkout before granting a trial; the refusal identifies its
checkout deadline. Completed purchases remain reserved through payment
settlement, including after that deadline. Use
`frameworks admin billing recovery list` for pending work. Run
`frameworks admin billing recovery checkout --tenant-id <uuid>
--reservation-id <uuid> --reason "Abandoned checkout"` for a provider/state/amount
preview and confirmation; `--yes` confirms non-interactively. The outcome and
actor are retained per request, exposed by `recovery list --tenant-id <uuid>
--history`, and emitted as `billing.checkout_recovery_action` request/result
events. Unbound attempts may be closed early; completed unsettled purchases
remain fenced. Normal settlement waits back off to six hours and alert after
fourteen days. Successful Mollie
activation settles its reservation atomically; completed checkout replays cannot
activate an old tier under a later grant.

A checkout retries its existing open intent and stored provider idempotency key.
A later checkout after settlement creates a new intent and key; prior receipts
remain available to identify stale callbacks. A callback waits until its exact
session/payment reference is bound to the local intent. Storing a delayed
provider response preserves a terminal outcome. Confirmed session expiry and
compensating subscription cancellation close the reservation, allowing a fresh
attempt without reusing the cancelled provider resource.

Checkout completion is persisted independently from settlement under the
subscription row lock. Direct events and recovery use that same completion
fact. A receipt stays outstanding until activation or confirmed expiry,
cancellation or failed settlement; an elapsed deadline alone cannot release it.
Compensation is marked under the lock before provider I/O, preventing concurrent
activation, and the receipt closes only after reconciliation finishes. Recovery
never cancels the provider subscription already installed locally.

Compensation is scoped to the checkout's first payment or initial invoice;
recurring service invoices are excluded. Refund acceptance enqueues separate
bank-settlement inspection with immutable native payment and currency evidence.
Only confirmed refunds or unreversed chargebacks count as returned funds. A
failed or missing return remains actionable inbox work; polling never creates
a second refund. An unactivated cluster purchase voids its initial operator
credit under the same subscription lock, whether the accrual or refund liability
arrives first. It does not reverse producer credit for recurring service.

The reservation records `prepared` before commercial provider I/O. Persisting
the exact request and its credential digest changes its submission state to
`submitted` under the subscription lock. A prepared attempt can therefore close
without a provider call, including after a local preflight failure. Requests
without an original journal are located rather than reconstructed and replayed.
Idempotent replays use the stored body/key only within a 23-hour Stripe or
45-minute Mollie window; Mollie's keys are retained for one hour and scoped to
the API credential. Credential rotation cannot turn a retry into a second create.
Late provider responses invalidate an older recovery lease and wake the durable
work again, so its acknowledgement cannot erase the new binding.

### First payments across an upgrade

After upgrading, compare Mollie's first payments paid just before rollout with
local activation/history. A paid receipt created before mandate ownership was
recorded neither fences a new payment nor links to new activation. Succeeded
historical receipts are not picked up by the automatic checkout sweep or the
ordinary recovery inventory. Check them before asking a customer to pay again.

For each affected tenant (identified by the provider's tenant metadata), this
read-only query finds the local reservation IDs. Bind `tenant_id`, `paid_since`
and `rollout_at` to the tenant and the pre-rollout payment window under review:

```sql
SELECT id AS reservation_id, provider_payment_id, succeeded_at
FROM purser.payment_provider_intents
WHERE tenant_id = :'tenant_id'::uuid
  AND provider = 'mollie' AND purpose = 'mollie_first_payment'
  AND status = 'succeeded' AND provider_mandate_id IS NULL
  AND activated_at IS NULL AND provider_payment_id IS NOT NULL
  AND succeeded_at >= :'paid_since'::timestamptz
  AND created_at < :'rollout_at'::timestamptz
ORDER BY succeeded_at, id;
```

Preview `frameworks admin billing recovery checkout --tenant-id TENANT
--reservation-id RESERVATION --reason "Unactivated payment across rollout"`.
Confirm compensation only for a purchase that never activated. A receipt that
funded delivered service must be retained; ambiguous delivery requires the
confirmed `--retain-delivered` procedure, not a guessed refund. No manual data
update or direct provider refund is part of this check.
