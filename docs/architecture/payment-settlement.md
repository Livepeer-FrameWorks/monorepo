# Payment Settlement - sync & async money-safety

How Purser turns a payment into a provisioning side-effect (invoice paid, prepaid
credited, subscription active, cluster access granted) without ever granting value
before the money has actually settled. Covers the Stripe hosted-checkout state
machine, the Mollie subscription-installment observation drain, and the x402
on-chain settlement reconciler.

## Core invariant

**No provisioning side-effect happens before payment is verified.** Card/wallet payments
settle synchronously (Stripe sends `checkout.session.completed` only after capture), but EU
methods — SEPA Direct Debit, iDEAL, Bancontact — settle hours-to-days later. For those,
`checkout.session.completed` arrives with `payment_status != "paid"`; the confirming event
comes later. Granting value on `completed` alone would credit money that never arrives.

For invoice collection, the second invariant is **never collect more than the
current balance**. Purser locks the invoice and derives
`amount_due = max(invoice_total - confirmed_payments + reversals, 0)` before it
creates or resumes a payment. A zero balance is not payable. One active
invoice-wide reservation blocks another card/crypto method; stale and terminal
attempts release the reservation. Billing status and reminder queries use the
same net-settlement definition instead of presenting the original total as due.

Provider transport retries reuse the original provider idempotency key. The
bounded API-call count lives on `payment_provider_intents`; it is deliberately
separate from the logical payment-attempt number. Declines, revoked mandates,
and SCA/action-required results are never retried blindly. Off-session SCA marks
the automatic reservation terminal and directs the customer to the exact
invoice for a new on-session hosted Checkout.

Provider callbacks have a separate durable ingress boundary. Stripe signatures
and payloads are checked, and Mollie form payloads are validated, before an
inbox row commits. The gateway receives 2xx at that point. A background worker
then leases and reconciles the callback with exponential backoff; the existing
provider-event claims keep reconciliation idempotent.

Overdue email follows the same durability rule. A daily/startup pass marks due
invoices overdue and stages at most one reminder for the latest eligible
1-, 7-, 14-, or 30-day stage. The email outbox retries SMTP delivery, and its
dispatcher recomputes amount due immediately before sending so a settled invoice
never receives a stale payment request.

## Stripe architecture

```
hosted Checkout ──> Stripe ──> Gateway /webhooks/billing/stripe ──> Purser WebhookService
                                                                       │
   checkout.session.completed (payment_status?) ──> DispatchStripeCheckoutCompleted
        paid / no_payment_required ──> settle / activate now
        unpaid / processing        ──> stage linkage only, wait
   checkout.session.async_payment_succeeded ──> DispatchStripeCheckoutCompleted (now paid) ──> settle
   checkout.session.async_payment_failed    ──> mark pending payment/top-up failed
   checkout.session.expired                 ──> expire intent + clear staged subscription state
   customer.subscription.updated (active)   ──> activate tenant subscription (apply tier)
   invoice.paid                             ──> activate cluster subscription (grant access)
```

## Stripe settlement state machine

| Purpose              | Stage on unpaid `completed`                                                | Settles / activates on                                                                                             |
| -------------------- | -------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| invoice              | attach payment_intent to pending `billing_payments`                        | `completed(paid)` or `async_payment_succeeded` → `updateInvoicePaymentStatus`                                      |
| prepaid              | attach provider id to `pending_topups`, commit, no credit                  | `completed(paid)` or `async_payment_succeeded` → credit balance                                                    |
| subscription         | persist customer/subscription ids, `stripe_subscription_status=incomplete` | `completed(paid)` or **`customer.subscription.updated(active)`** → `activateTenantSubscriptionFromStripe`          |
| cluster_subscription | upsert row `status=pending_payment`, no grant                              | `completed(paid)`, **`invoice.paid`**, or `subscription.updated(active)` → `activateClusterSubscriptionFromStripe` |

Activation authorities are idempotent and convergent: whichever event lands first activates;
the rest are no-ops. Cluster activation is reachable from both `invoice.paid` and
`customer.subscription.updated(active)` and must produce the same single grant in either order.

## Idempotency

- Invoice: `updateInvoicePaymentStatus` is partial-payment-aware — keyed on `tx_id`, the
  invoice flips paid only when summed confirmed payments cover the amount.
- Prepaid: `pending_topups` status guard (`FOR UPDATE`) + `balance_transactions` idempotency
  unique index prevent double-credit on webhook replay.
- Subscription/cluster: activation `UPDATE`/`UPSERT` is keyed on the tenant/subscription and
  COALESCEs known fields, so replays and out-of-order deliveries are safe.
- Cluster access: `GrantClusterAccess` runs **before** the row is marked active. A failed
  grant returns an error and leaves the row non-active, so the webhook retry re-attempts it
  (no active-without-access stranding, crash-safe); once active, duplicate events skip the
  grant so access work is not re-enqueued.

## Stripe API version

The bundled `stripe-go` SDK pins `2026-05-27.dahlia`; the Stripe webhook endpoint must be
configured with the same API version. Webhook payloads are parsed by hand-rolled structs
(minimal-field, drift-resistant), not `webhook.ConstructEvent`. Dahlia delivers the invoice
subscription id at `parent.subscription_details.subscription`; `resolveSubscriptionID()` reads
that with the legacy top-level `subscription` as fallback.

## Payment-method allowlist

Pinned in code (`PaymentMethodTypesForCurrency`), not inherited from the dashboard, so a
dashboard toggle cannot silently introduce a method the settlement code does not handle. The
list is **currency-aware**: `card` is always offered; `sepa_debit`, `ideal`, and `bancontact`
are EUR-only at Stripe, so they are added only when the checkout currency is EUR (a USD
checkout would offer card only). The same list applies to one-time and subscription Checkout —
iDEAL/Bancontact subscribers are collected as a SEPA Direct Debit mandate.

The public invoice method `card` is advertised only when one provider is fully
reconcilable. Stripe requires its secret, webhook signing secret, and public
webapp URL. Mollie requires its secret plus public gateway/webapp URLs. When
both are ready, `PAYMENT_CARD_PROVIDER` must explicitly choose `stripe` or
`mollie`; Purser does not silently prefer either provider.

## Collection and top-up minimums

Automatic Stripe/Mollie overage collection has a 500-cent economic floor. A
closed period with no billable overage produces a zero-due paid statement. A
rounded overage from 1 through 499 cents is not waived: Purser posts a visible
negative carry-forward adjustment so the current invoice has no payment due,
locks the amount in `billing_collection_balances`, and records the opening,
current, collected, and closing cents in `billing_collection_entries`. The
first later period that brings the balance to at least 500 cents adds the prior
balance as a line item and collects the combined invoice exactly once.

Account closure and the move to Free each end the last collectable invoice. Both
write off any remaining sub-floor balance, including what their final invoice
deferred, to `billing_collection_writeoffs` with reason `account_closed` or
`moved_to_free` and clear the carry in the transaction that commits that invoice
or tier change, including a Purser-billed tenant's scheduled downgrade to Free. It is never silently
discarded or left as an unreachable customer debt.

The balance update, audit entry, invoice header, line items, credit application,
and period advancement share one database transaction. Status is decided only
after rounding to the invoice currency precision, so a fractional-cent rating
cannot become a pending invoice stored as `0.00`.

Fiat prepaid top-ups are separately validated by provider and currency. Stripe
and Mollie EUR/USD top-ups currently share the 500-cent business minimum; the
policy registry also tracks provider technical minima so adding a provider or
currency cannot silently inherit an invalid amount. Crypto top-ups accept one
cent, the smallest amount the prepaid ledger can credit. Provider and network
fees are not added to the credited amount.

## Mollie subscription installments & observation drain

Mollie auto-creates one payment per subscription period and fires the payment
webhook with `payment.subscriptionId` set. Purser reconciles it by locating the
local invoice whose period contains `payment.createdAt` for that subscription's
tenant — **only `pending`/`overdue` invoices qualify**; draft/manual_review
invoices must not consume a real payment before they can be finalized. A matching
invoice gets a pending `billing_payments` row keyed by the Mollie payment id and
flows through the same partial-payment-aware `updateInvoicePaymentStatus` helper
as Stripe.

The out-of-order case is the point of the drain: Mollie can fire the installment
webhook **before** the local invoice for that period has been finalized. The
webhook handler then parks the payment in `purser.mollie_payment_observations`
(upserted by `mollie_payment_id`, raw payload retained, `attempt_count`
incremented on redelivery) — neither a silent no-op nor an error that would make
Mollie retry forever.

Parked observations are drained on two paths:

1. **Inline:** invoice finalization calls
   `drainMolliePaymentObservationsForInvoice` immediately after the invoice
   commit, so a newly-finalized invoice consumes observations parked earlier.
2. **Backstop:** a 10-minute loop (`runMollieObservationDrain` in the job
   manager) scans invoices that still have unresolved observations — this covers
   a crash between invoice commit and inline drain.

Drain semantics: unresolved observations matching the invoice's tenant +
`mollie_subscription_id` with `paid_at` inside the invoice period are attached —
Mollie status mapped (`paid` → confirmed, `failed`/`cancelled`/`expired` →
failed, `pending`/`open` → pending), `billing_payments` row inserted, settlement
routed through `updateInvoicePaymentStatus`, observation marked
`resolution='attached'`. A currency mismatch refuses to settle and leaves the
observation unresolved for operator review rather than silently dropping it.

## Paid-term changes

A self-service upgrade starts a fresh month. The unused retained paid old base
fee offsets only the replacement fee, capped at that fee; it creates no cash
refund or spendable balance. An operator move to prepaid forfeits any retained
unused paid share and records that amount on the old invoice. Reducing an unpaid
old fee to its used share remains part of the closing boundary.

Changing collection provider on the same paid advance tier preserves the current
period and its paid invoice. A native provider subscription must use the tier
change flow instead of creating a second checkout.

### Scheduled downgrades on a provider subscription

A downgrade leaves the paid term untouched: no proration, refund or credit. The
provider must charge the lower fee at the next renewal, so `ChangeBillingTier`
commits one `tier_downgrade` inbox step per tenant with the staged downgrade.
Reissuing `ChangeBillingTier` re-points it; selecting the current tier cancels
it. `admin billing set-tier` replaces it. Each wakes the same step, which reads
the latest desired tier: the pending target, otherwise the current tier.

The worker reads the provider outside the subscription lock and changes only
what differs. Stripe replaces the subscription item's price with
`proration_behavior=none` and writes `metadata.tier_id`, keeping the billing
anchor. Mollie patches the amount and metadata, keeping `startDate`. A repeat is
a no-op, so a lost response or a retry needs no idempotency key. The worker then
relocks and records `pending_provider_tier_id` only if the desired tier and
subscription still match; otherwise it wakes itself. A suspended Mollie subscription
is patched too; only canceled and completed ones are terminal.

The first write of a downgrade records the subscriber's own Stripe price or Mollie
amount in provider metadata, in the same update, because that may differ from the
current catalog fee. Cancelling restores exactly that value and clears it. A Stripe
price archived meanwhile is re-created inline with its product, amount and interval.
With no downgrade pending and the provider already on the tier there is no write at
all. A tier the provider cannot return to, with nothing recorded, stays a failing
inbox step: the alert fires and the unfinished step keeps metadata from moving the
local tier. An upgrade is refused with a retry message while the step is unfinished,
because it credits and replaces that same price.

Once a confirmed downgrade reaches `pending_effective_at` the provider renews at the
target fee while the local flip waits for that period's invoice. `ChangeBillingTier`
refuses a cancel or re-point in that window, so nobody gets a term at the old tier
for the lower fee. While a held invoice keeps the flip waiting the customer stays
refused, and `PurserTierDowngradeHeld` fires 26 hours after the effective time. The
exits are resolving the hold, so the daily applier flips the tier, or
`admin billing set-tier`.

Local and provider state cannot disagree silently:

- `applyPendingDowngrade` flips `tier_id` only once the provider confirmed that
  exact target. While it waits the tenant keeps the paid tier, and repeated
  failures raise `PurserProviderWebhookInboxStuck`.
- A late confirmation never shortens paid service: `pending_effective_at` moves
  forward to the provider's paid-through date when a renewal already ran.
- Stripe metadata cannot move `tier_id` while a downgrade is pending or its step
  is unfinished, so the flip stays with the applier at the effective time. After
  each confirmation `provider_tier_changed_at` fences later deliveries: an event
  created up to one minute after it, or of unknown age, keeps the local tier.
  Period and status fields still apply.
- Discovery enqueues any staged downgrade with a live provider subscription and
  no confirmation, so every such downgrade has an owner in the inbox.

A paid target with no monthly Stripe price is refused, as is a no-fee tier other
than Free.

Free has no price to change, so that downgrade ends the subscription through the
Free fallback. The subscription stays live until 72 hours before the effective
time, because a Mollie cancellation cannot be undone; a re-point or cancel in that
window changes nothing at the provider. Discovery then wakes the inbox step, which
schedules the fallback. Its sweep commits the pricing fence, ends provider
collection outside the lock with its own retries and `PurserFreeFallbackStuck`
alert, and retains the term through `pending_effective_at`. Until the sweep's
fence, a re-point or cancel withdraws the schedule and the subscription stays
live. During the stop it is refused as reconciling. Once the provider confirms,
the subscription is `pending_cancellation` and any further tier change is refused:
the customer keeps the paid term, moves to Free at its end, and may buy a paid tier
after that move. The lead leaves three days for retries; a stop still failing at
the renewal is the alert's case, not a silent charge. A payment-failure fallback
already scheduled is left alone: it keeps its grace date, reason and recovery check,
and a past-due term is not retained through the staged effective time. The Stripe
stop finds the paid term by the invoice line's subscription item, because a confirmed
tier change moves that item to another price after the term was paid.

## End of a provider subscription

A subscription that ends never becomes `cancelled` while it has paid service to
wind down, because billing and admission queries read `cancelled` as no
subscription at all.

| Trigger                                                                                                  | Status                 | Moves to Free            |
| -------------------------------------------------------------------------------------------------------- | ---------------------- | ------------------------ |
| Stripe `unpaid`, or `canceled` for any reason but a customer request                                     | `past_due`             | 14 days later            |
| Mollie mandate revoked at charge time, or a failed installment finds the subscription canceled/suspended | `past_due`             | 14 days later            |
| Stripe `canceled` from `pending_cancellation` or with reason `cancellation_requested`                    | `pending_cancellation` | At the event             |
| Mollie cancellation through `CancelMollieSubscription`                                                   | `pending_cancellation` | At billing period end    |
| Stripe `incomplete_expired`, or the subscription never activated                                         | `cancelled`            | Never; nothing activated |

Mollie sends no webhook for a subscription's own status, so a failed installment
reads it. Exhausted retries on a valid mandate keep ordinary dunning.

`free_fallback_at` holds the date; repeated events never extend it, and a
completed provider activation clears it. Neither status is in the invoice job's list, so
`runFreeFallbackSweep` issues the final invoice through
`PrepareProviderBillingBoundary` in the transaction that moves the tenant,
which waits for complete metering at the stored fallback cutoff. Retries keep
that cutoff, so a delayed worker does not extend the invoice. A boundary already
opened by an operator takes precedence over an earlier fallback date.

Moves and notices use separate workers and durable claims. Moves retry after
five minutes; failed notices back off exponentially to a twelve-hour maximum.
After five failures, delivery stops in the terminal `notice_abandoned` state.
The abandoned notice leaves the automatic stuck gauge but remains in recovery
inventory with its delivery error. Correct `billing_email`, then use
`admin billing recovery notice --tenant-id <UUID> --reason "Billing email repaired"`.
A separate operator-only RPC previews the abandoned notice and fences its
confirmed retry to the same deadline and grace/moved phase. The transaction
requeues delivery and records the operator and reason in a billing event; it
never invokes purchase recovery or changes billing. Repeated confirmation of
the same queued lifecycle does not duplicate the audit event.
Failed final invoices cannot starve later tenants, and notification failure does
not block a billing move. SMTP runs after its claim commits, outside the billing
transaction; acknowledgement is fenced against a newer move or fallback. The
`PurserFreeFallbackStuck` alert reports repeated failures or sustained inventory
read errors, using the maximum across replicas. An operator grant or a pending
tier upgrade holds the move. A platform operator can inspect its schedule and
errors with `admin billing recovery list --tenant-id <UUID>`. An audited
`admin billing set-tier` cancels a scheduled fallback before provider stopping
begins when no live native collector remains. The abort finalizes the ended provider-managed periods before atomically
restoring `active` invoicing and detaching native subscription identities.
Complete overdue months retain separate allowances; the final partial close
commits with detachment. Every old closing document excludes the native base
fee, which the provider already collected.
An ended native subscription retains its fee-exclusion identity until its paid
term ends. Tier assignment, grant set and explicit grant revoke refuse during
that term. Grant creation, renewal, expiry and revoke preserve the customer's
cancellation, including when a grant held the move past the paid deadline.
A same-tier fallback abort with an outstanding grant is refused; the operator
must end it or make an explicit assignment to a different tier. Cancellation moves
the tenant to Free at the paid-term end once the grant no longer holds it.
Automatic same-tier grant expiry still ends its waiver at the grant deadline,
preserving the native identity, ended-provider evidence and fallback schedule.
A live native collector or an in-flight stop must finish reconciliation before
assignment; Purser refuses the abort instead of discarding that obligation. On Free, `requireSettledOnFree` refuses paid
tiers while finalized invoices are unpaid.

Before moving a subscription to Free, Purser commits a pricing fence under the
subscription row lock, then ends native provider collection outside that
transaction. A provider outage keeps its native identity and retries. Grants,
checkouts and upgrades cannot replace pricing while that stop is unsettled;
account closure can still take ownership and permanently close billing.

Confirmed bank payments that are still settling keep the fence. Normal waits
are not failures; waits exceeding fourteen days alert. A recovered collector
keeps the subscription. A payment that settles during cancellation retains its
full paid term, including its DNS entitlements, without renewing or refunding
that term. Initial Mollie funding is matched through its first-payment and
native-creation receipts, excluding funding consumed by an earlier native
creation. A definitive activation refusal queues compensation for unused paid
funding even when no native subscription was created; a transient read failure
leaves it reusable. Stripe retains cash-funded terms only with retained payment evidence;
fully credit-funded terms use the paid invoice and its service period.
Pending mandates and in-flight pricing changes retain funding for retry. A
provider-confirmed terminally invalid mandate queues compensation only for its
own unused funding. Native subscriptions created before a repairable activation
refusal are cancelled without refunding the reusable first payment.

A durable protection-through timestamp keeps local monthly start, end and
collection anchors together through payment recovery. Dated dunning events
establish protection before moving any local dates. Native renewal callbacks
remain observational until actual invoice finalization closes each missed
month with its own allowance. Before moving to Free, catch-up finalizes one
complete stored month per transaction, then the final partial period. It
propagates metering or invoice errors and never charges the next term. A new
native purchase establishes a fresh period. Retry inventories prioritize untouched and least recently
attempted work, so slow provider failures cannot monopolize the first batch.

## x402 settlement reconciler

x402 USDC top-ups settle on-chain, so the row in `purser.x402_nonces` walks an
explicit state machine driven by the settle handler (`x402.go`) and a 30-second
reconciler loop (`x402_reconciler.go`):

```
settle request ──> submitting ──broadcast acknowledged──> pending
      │                  │                                  │
      │                  └─ unknown outcome: replay the      ├─ finalized exact transfer ──> confirmed + credit
      │                     same durable raw transaction     └─ canonical revert/mismatch ─> failed
      └─ validation/simulation failure ────────────────────────────────────────────────────> failed
```

- **submitting** — Purser atomically claims the tenant-bound quote and the
  `(network, payer, authorization nonce)` replay key. For the embedded
  facilitator it serializes the relayer nonce, signs once, and commits the raw
  transaction, deterministic hash, fee inputs, and relayer identity before the
  first RPC submission.
- **unknown broadcast outcome** — a timeout or malformed RPC reply does not
  become failure and never creates a replacement payment. The reconciler and
  caller retry the exact stored bytes/hash. If the authorization is consumed
  but no durable transaction hash exists, an accounting anomaly requires
  operator review because USDC exposes only a boolean authorization state.
- **pending** — broadcast is acknowledged but the customer has no spendable
  credit yet. A missing receipt or RPC outage remains pending regardless of age.
  A successful receipt must contain the exact authorized USDC `Transfer` and be
  included by the network's consensus-labelled finalized head.
- **confirmed** — settlement status, unique balance top-up, quote confirmation,
  and prepaid credit commit atomically. Only then may the gateway rerun normal
  admission and execute protected work. Tax-document and rollup obligations are
  idempotently reconciled; a missing document keeps the accounting anomaly gate
  unhealthy until repaired.
- **failed/reorg** — only an explicit canonical revert, transfer mismatch, or
  later loss of an already-confirmed receipt can fail a submitted payment. A
  post-confirmation reversal debits only when the original unique credit exists;
  any later recovery likewise requires the matching reversal before re-credit.

## Key Files

- `api_billing/internal/handlers/checkout.go` - dispatch, gating, activation/staging helpers
- `api_billing/internal/handlers/webhooks.go` - event switch, subscription/invoice/charge handlers (Stripe + Mollie, incl. observation park/drain)
- `api_billing/internal/handlers/provider_webhook_inbox.go` - durable callback ingress, leasing, and retry worker
- `api_billing/internal/handlers/invoice_email_outbox.go` - invoice and staged dunning email delivery
- `api_billing/internal/handlers/jobs.go` - invoice generation, inline drain call, 10-minute observation-drain backstop
- `api_billing/internal/handlers/x402.go` - quote claim, durable embedded-facilitator attempt, finalized confirmation, and atomic credit
- `api_billing/internal/handlers/x402_reconciler.go` - four-sweep reconciler (submitting/pending/failed/confirmed)
- `api_billing/internal/stripe/client.go` - checkout session creation, tier sync, off-session charges

## Gotchas

- `payment_intent.succeeded` does **not** settle checkout payments — Checkout-created
  PaymentIntents carry no `metadata.invoice_id`, so that handler only serves off-session
  overage charges. Checkout settlement flows through the `checkout.session.*` events.
- `invoice.paid` does not touch the tenant subscription base (provider-managed tenant
  invoices have `base_amount = 0`); it is the activation authority for **cluster**
  subscriptions and resets dunning.
- SCA notifications (`SendPaymentActionRequiredEmail`) never mark the invoice failed.
  Recurring charges (`invoice.payment_action_required`) link the hosted invoice URL. Off-session
  overage SCA cannot be completed off-session and the parked PaymentIntent is not resumable by a
  payment-method change, so the customer is directed to pay the open overage invoice on-session
  in the billing UI (hosted Checkout performs the authentication); the invoice stays
  pending/overdue and dunning covers it if they do not act.
