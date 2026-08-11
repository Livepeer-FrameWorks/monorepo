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

## x402 settlement reconciler

x402 USDC top-ups settle on-chain, so the row in `purser.x402_nonces` walks an
explicit state machine driven by the settle handler (`x402.go`) and a 30-second
reconciler loop (`x402_reconciler.go`):

```
settle request ──> submitting ──broadcast ok──> pending ──receipt + depth──> confirmed
                       │                           │                            │
                       │ auth consumed w/o tx      │ timeout (2m) / revert      │ reorg watch (1h,
                       │ or validBefore expired    ▼                            ▼  50-block depth)
                       └─────────────────────────> failed <─────────────────────┘
                                                     │
                                                     └─ timeout/reorg failures: late recovery
                                                        within 168h ──> confirmed (re-credit)
```

- **submitting** — durable pre-submit intent: the row (with the full
  authorization payload as JSONB) is written _before_ any chain interaction.
  The `(network, payer, nonce)` unique key doubles as the replay guard: a
  conflicting tenant/amount/payload for the same nonce is rejected as a
  settlement conflict.
- **submitting → pending** — after `eth_call` simulation and broadcast of
  `transferWithAuthorization`, the tx hash is recorded and the tenant's prepaid
  balance is credited **optimistically** in its own transaction (a credit failure
  leaves the row pending; the reconciler credits at confirmation instead). This
  deliberately differs from the fiat core invariant — value is granted at
  broadcast and clawed back on failure, with every credit/debit recorded in
  `balance_transactions` by reference type.
- **Stuck submitting** (>30s): the USDC contract's `authorizationState` is the
  oracle. Consumed on-chain without a recorded tx hash → `failed` flagged for
  manual reconciliation (the bool cannot recover the tx hash) + accounting
  anomaly event. `validBefore` expired → `failed` safely (nothing was credited).
  Otherwise the reconciler re-broadcasts from the stored payload under a
  2-minute claim lease (`last_submit_attempt_at`), then promotes and credits.
- **pending** (>15s): fetch the receipt. Success → wait for the network's
  required confirmation depth (block/gas recorded while waiting), ensure the
  credit exists, then `confirmed`. No receipt after 2 minutes → `failed`
  (timeout) + balance debit. Reverted → `failed` + debit.
- **failed, recoverable reasons** (timeout / reorg) within
  `X402_RECOVERY_WINDOW_HOURS` (default 168): if a receipt now shows a confirmed
  success, re-credit **only when the original timeout reversal exists** (missing
  reversal → anomaly event, no credit — prevents double-credit) and promote to
  `confirmed` with a late-recovery event.
- **confirmed** (watched for 1 hour): reorg check. Receipt missing beyond
  `X402_REORG_DEPTH_BLOCKS` (default 50) or now reverted → `failed` + debit,
  guarded by "only debit if the original credit exists".

## Key Files

- `api_billing/internal/handlers/checkout.go` - dispatch, gating, activation/staging helpers
- `api_billing/internal/handlers/webhooks.go` - event switch, subscription/invoice/charge handlers (Stripe + Mollie, incl. observation park/drain)
- `api_billing/internal/handlers/jobs.go` - invoice generation, inline drain call, 10-minute observation-drain backstop
- `api_billing/internal/handlers/x402.go` - settle handler: intent record, broadcast, optimistic credit
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
