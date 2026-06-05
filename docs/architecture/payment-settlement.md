# Payment Settlement - Stripe sync & async money-safety

How Purser turns a hosted-checkout payment into a provisioning side-effect (invoice paid,
prepaid credited, subscription active, cluster access granted) without ever granting value
before the money has actually settled.

## Core invariant

**No provisioning side-effect happens before payment is verified.** Card/wallet payments
settle synchronously (Stripe sends `checkout.session.completed` only after capture), but EU
methods — SEPA Direct Debit, iDEAL, Bancontact — settle hours-to-days later. For those,
`checkout.session.completed` arrives with `payment_status != "paid"`; the confirming event
comes later. Granting value on `completed` alone would credit money that never arrives.

## Architecture

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

## Settlement state machine

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

## API version

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

## Key Files

- `api_billing/internal/handlers/checkout.go` - dispatch, gating, activation/staging helpers
- `api_billing/internal/handlers/webhooks.go` - event switch, subscription/invoice/charge handlers
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
