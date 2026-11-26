<script lang="ts">
  import { onMount } from "svelte";
  import {
    PlatformAdjustBalanceStore,
    PlatformRefundInvoiceStore,
    GetPlatformAutomaticTopupStore,
    PlatformDisableAutomaticTopupStore,
    PlatformAssignBillingTierWithReviewStore,
    PlatformPreviewBillingTierStore,
    PlatformRecoverCheckoutStore,
    PlatformSetEntitlementsStore,
  } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { Textarea } from "$lib/components/ui/textarea";
  import { formatCents } from "$lib/utils/formatters";
  import { billingAdjustmentCents, entitlementEntries } from "./billing-repair";

  let {
    tenantId,
    entitlements = [],
    automaticPrepaidUsage = null,
    onChanged,
  }: {
    tenantId: string;
    entitlements?: readonly { key: string; value: string }[];
    automaticPrepaidUsage?: boolean | null;
    onChanged?: () => Promise<void> | void;
  } = $props();
  type Action = "credit" | "entitlements" | "tier" | "checkout" | "refund" | "disable_auto";
  type Review = {
    action: Action;
    tenantId: string;
    reason: string;
    referenceId: string;
    amountCents: number;
    tierName: string;
    reservationId: string;
    invoiceId: string;
    refund?: {
      fingerprint: string;
      amountMinor: number;
      currency: string;
      status: string;
      detail: string;
    };
    quote?: { quotedAt: string; expiresAt: string; fingerprint: string; effectsJson: string };
    entries: { key: string; value: string }[];
    expectedEntries: { key: string; value: string }[];
  };
  const credit = new PlatformAdjustBalanceStore();
  const refund = new PlatformRefundInvoiceStore();
  const automaticQuery = new GetPlatformAutomaticTopupStore();
  const automaticDisable = new PlatformDisableAutomaticTopupStore();
  let automaticSummary = $state("");
  let invoiceId = $state("");
  const assign = new PlatformAssignBillingTierWithReviewStore();
  const previewAssignment = new PlatformPreviewBillingTierStore();
  const recover = new PlatformRecoverCheckoutStore();
  const terms = new PlatformSetEntitlementsStore();
  let action = $state<Action>("credit");
  let reason = $state("");
  let amount = $state("");
  let tierName = $state("");
  let reservationId = $state("");
  let overrides = $state("");
  let review = $state<Review | null>(null);
  let checkoutPreview = $state("");
  let busy = $state(false);
  let submitted = $state(false);
  let error = $state("");
  let receipt = $state("");
  let initializedTenant = $state("");
  const storageKey = (targetTenant = tenantId) => `frameworks:operator-credit:${targetTenant}`;

  onMount(() => {
    const pending = sessionStorage.getItem(storageKey());
    if (pending) {
      try {
        const saved: Review = JSON.parse(pending);
        if (
          (saved.action === "credit" || saved.action === "refund") &&
          saved.tenantId === tenantId
        ) {
          review = saved;
          submitted = true;
        }
      } catch {
        error =
          "Could not restore the pending credit. Inspect its ledger before creating another adjustment.";
      }
    }
  });
  $effect(() => {
    if (initializedTenant !== tenantId) {
      initializedTenant = tenantId;
      overrides = JSON.stringify(
        Object.fromEntries(entitlements.map((entry) => [entry.key, JSON.parse(entry.value)])),
        null,
        2
      );
    }
  });

  async function prepare() {
    error = "";
    receipt = "";
    if (!reason.trim()) {
      error = "Enter an audit reason.";
      return;
    }
    try {
      const candidate: Review = {
        action,
        tenantId,
        reason: reason.trim(),
        referenceId: crypto.randomUUID(),
        amountCents: action === "credit" ? billingAdjustmentCents(amount) : 0,
        tierName: tierName.trim(),
        reservationId: reservationId.trim(),
        invoiceId: invoiceId.trim(),
        entries: action === "entitlements" ? entitlementEntries(overrides) : [],
        expectedEntries: entitlements.map((entry) => ({ ...entry })),
      };
      if (action === "tier" && !candidate.tierName) throw new Error("Enter a catalogue tier name.");
      if (action === "tier") {
        busy = true;
        const response = await previewAssignment.mutate({
          tenantId: candidate.tenantId,
          tierName: candidate.tierName,
          reason: candidate.reason,
        });
        const quote = response.data?.platformPreviewBillingTier;
        if (!quote || response.errors?.length)
          throw new Error(response.errors?.[0]?.message ?? "Could not price the assignment.");
        candidate.quote = {
          ...quote,
          quotedAt: String(quote.quotedAt),
          expiresAt: String(quote.expiresAt),
        };
      }
      if (action === "refund") {
        if (!candidate.invoiceId) throw new Error("Enter the paid invoice UUID.");
        busy = true;
        const response = await refund.mutate({
          tenantId: candidate.tenantId,
          invoiceId: candidate.invoiceId,
          requestId: candidate.referenceId,
          reason: candidate.reason,
          confirmed: false,
          fingerprint: "",
        });
        const result = response.data?.platformRefundInvoice;
        if (!result || response.errors?.length)
          throw new Error(response.errors?.[0]?.message ?? "Refund preview unavailable.");
        candidate.refund = result;
      }
      if (action === "disable_auto") {
        busy = true;
        const response = await automaticQuery.fetch({
          policy: "NetworkOnly",
          variables: { tenantId: candidate.tenantId },
        });
        const result = response.data?.platformAutomaticTopup;
        if (!result || response.errors?.length)
          throw new Error(
            response.errors?.[0]?.message ?? "Automatic payment settings unavailable."
          );
        automaticSummary = `${result.enabled ? "Enabled" : "Disabled"}; ${formatCents(result.monthlyReservedOrSpentMinor, result.currency)} reserved or spent this UTC month. ${result.pausedReason}`;
      }
      if (action === "checkout") {
        if (!candidate.reservationId) throw new Error("Enter the checkout reservation ID.");
        busy = true;
        const response = await recover.mutate({
          tenantId,
          reservationId: candidate.reservationId,
          reason: candidate.reason,
          confirmed: false,
        });
        const result = response.data?.platformRecoverCheckout;
        if (!result || response.errors?.length)
          throw new Error(response.errors?.[0]?.message ?? "Checkout preview failed.");
        checkoutPreview = `${result.provider} · ${result.status} · ${result.amountKnown ? formatCents(result.amountCents, result.currency) : "Amount requires provider inspection"} · ${result.outcome}: ${result.detail}`;
      }
      if (candidate.tenantId !== tenantId) return;
      review = candidate;
      submitted = false;
    } catch (failure) {
      error = failure instanceof Error ? failure.message : "Could not prepare billing change.";
    } finally {
      busy = false;
    }
  }

  async function apply() {
    const request = review;
    if (!request || busy || request.tenantId !== tenantId) return;
    let operationReceipt = "";
    busy = true;
    error = "";
    try {
      // Persist the immutable request before contacting Purser so a reload can replay its receipt.
      if (request.action === "credit" || request.action === "refund")
        sessionStorage.setItem(storageKey(request.tenantId), JSON.stringify(request));
      submitted = true;
      if (request.action === "credit") {
        const response = await credit.mutate({
          tenantId: request.tenantId,
          amountCents: request.amountCents,
          reason: request.reason,
          referenceId: request.referenceId,
        });
        const result = response.data?.platformAdjustBalance;
        if (!result || response.errors?.length)
          throw new Error(
            response.errors?.[0]?.message ?? "Credit outcome is unknown. Retry this same reference."
          );
        operationReceipt = `Ledger receipt ${result.id}: ${formatCents(result.amountCents, "EUR")}; balance after this operation ${formatCents(result.balanceAfterCents, "EUR")}.`;
        sessionStorage.removeItem(storageKey(request.tenantId));
      } else if (request.action === "refund") {
        const response = await refund.mutate({
          tenantId: request.tenantId,
          invoiceId: request.invoiceId,
          requestId: request.referenceId,
          reason: request.reason,
          confirmed: true,
          fingerprint: request.refund!.fingerprint,
        });
        const result = response.data?.platformRefundInvoice;
        if (!result || response.errors?.length)
          throw new Error(
            response.errors?.[0]?.message ??
              "Refund outcome unknown. Retry this same reference; do not create another refund."
          );
        operationReceipt = `Refund ${result.requestId}: ${result.status} · ${formatCents(result.amountMinor, result.currency)}. ${result.detail}`;
        if (result.status !== "succeeded") {
          if (request.tenantId === tenantId) {
            receipt = operationReceipt;
            await onChanged?.();
          }
          return;
        }
        sessionStorage.removeItem(storageKey(request.tenantId));
      } else if (request.action === "disable_auto") {
        const response = await automaticDisable.mutate({
          tenantId: request.tenantId,
          reason: request.reason,
        });
        if (!response.data?.platformDisableAutomaticTopup || response.errors?.length)
          throw new Error(response.errors?.[0]?.message ?? "Could not disable automatic payments.");
        operationReceipt =
          "Future automatic top-ups disabled. A charge already in progress may still settle.";
      } else if (request.action === "tier") {
        const response = await assign.mutate({
          tenantId: request.tenantId,
          tierName: request.tierName,
          reason: request.reason,
          quotedAt: request.quote!.quotedAt,
          fingerprint: request.quote!.fingerprint,
        });
        const result = response.data?.platformAssignBillingTierWithReview;
        if (!result || response.errors?.length)
          throw new Error(
            response.errors?.[0]?.message ??
              "Assignment outcome is unknown. Reload billing before another assignment."
          );
        operationReceipt = `Subscription ${result.subscriptionId}: ${result.tierName}, ${result.billingModel}.`;
      } else if (request.action === "entitlements") {
        const response = await terms.mutate({
          tenantId: request.tenantId,
          input: {
            reason: request.reason,
            entitlementOverrides: request.entries,
            expectedEntitlementOverrides: request.expectedEntries,
          },
        });
        const result = response.data?.updateSubscriptionCustomTerms;
        if (!result || response.errors?.length)
          throw new Error(response.errors?.[0]?.message ?? "Entitlement update failed.");
        operationReceipt = `Saved entitlement overrides for subscription ${result.id}.`;
      } else {
        const response = await recover.mutate({
          tenantId: request.tenantId,
          reservationId: request.reservationId,
          reason: request.reason,
          confirmed: true,
        });
        const result = response.data?.platformRecoverCheckout;
        if (!result || response.errors?.length)
          throw new Error(
            response.errors?.[0]?.message ??
              "Recovery outcome is unknown. Inspect recovery before retrying."
          );
        operationReceipt = `${result.outcome}: ${result.detail} · Audit ${result.auditId || "pending"}`;
      }
      if (request.tenantId !== tenantId) return;
      receipt = operationReceipt;
      review = null;
      submitted = false;
      await onChanged?.();
    } catch (failure) {
      if (request.tenantId === tenantId)
        error = failure instanceof Error ? failure.message : "Billing change failed.";
    } finally {
      busy = false;
    }
  }
</script>

<section class="slab mb-4" aria-label="Operator billing actions">
  <div class="slab-header"><h3>Operator billing actions</h3></div>
  <div class="slab-body--padded space-y-3">
    <p class="text-sm text-muted-foreground">
      Target tenant: <span class="font-mono">{tenantId}</span>. Every change records a reason.
    </p>
    {#if automaticPrepaidUsage !== null}
      <p class="text-sm text-muted-foreground">
        Funding preference on Free: {automaticPrepaidUsage
          ? "activate PAYG after settled funding"
          : "keep Free and retain wallet credit"}.
      </p>
    {/if}
    {#if review}
      <div role="status" class="space-y-2 text-sm">
        <p>
          Review <strong>{review.action}</strong> for
          <span class="font-mono">{review.tenantId}</span>: {review.reason}
        </p>
        {#if review.action === "credit"}
          <p>
            Wallet adjustment: {formatCents(review.amountCents, "EUR")}. This is account credit or
            debit; it does not refund a card payment.
          </p>
          <p class="break-all">Retry reference: {review.referenceId}</p>
        {:else if review.action === "refund"}
          <p>
            Return {review.refund
              ? formatCents(review.refund.amountMinor, review.refund.currency)
              : "the full original payment"} for invoice
            <span class="font-mono">{review.invoiceId}</span> to the original payment method.
          </p>
          <p>{review.refund?.detail}</p>
          <p>
            Retry reference: {review.referenceId}. Do not also grant goodwill credit for the same
            reimbursement.
          </p>
        {:else if review.action === "disable_auto"}
          <p>{automaticSummary}</p>
          <p>
            Disable future automatic top-ups. A charge already in progress may still settle. This
            does not cancel the subscription.
          </p>
        {:else if review.action === "tier"}
          <p>
            Apply <strong>{review.tierName}</strong> now. Purser closes the current billing phase. This
            does not issue a cash refund or reimburse the unused paid month.
          </p>
          {#if review.quote}
            <p>
              Priced through {new Date(review.quote.quotedAt).toLocaleString()}; review expires {new Date(
                review.quote.expiresAt
              ).toLocaleTimeString()}.
            </p>
            {@const effects = JSON.parse(review.quote.effectsJson)}
            <dl class="grid grid-cols-2 gap-2">
              <dt>Wallet before → after</dt>
              <dd>
                {formatCents(effects.wallet_before_eur_cents, "EUR")} → {formatCents(
                  effects.wallet_after_eur_cents,
                  "EUR"
                )}
              </dd>
              <dt>Closing invoice due</dt>
              <dd>
                {formatCents(
                  effects.postpaid.invoice_due_eur_cents,
                  "EUR"
                )}{#if effects.postpaid.invoice_presentment_currency}
                  / {formatCents(
                    effects.postpaid.invoice_presentment_minor,
                    effects.postpaid.invoice_presentment_currency
                  )}{/if}
              </dd>
              <dt>Wallet credit used on invoice</dt>
              <dd>{formatCents(effects.postpaid.invoice_wallet_credit_eur_cents, "EUR")}</dd>
              <dt>Deferred collection charged to wallet</dt>
              <dd>{formatCents(effects.postpaid.collection_carry_eur_cents, "EUR")}</dd>
              <dt>Unused unpaid monthly fee removed</dt>
              <dd>{formatCents(effects.postpaid.unpaid_advance_reduction_eur_cents, "EUR")}</dd>
              <dt>Unused paid fee retained</dt>
              <dd>{formatCents(effects.postpaid.paid_advance_forfeiture_eur_cents, "EUR")}</dd>
              <dt>Closing prepaid usage and fees</dt>
              <dd>
                {formatCents(
                  effects.prepaid.statement_settlement_eur_cents +
                    effects.prepaid.statement_fees_eur_cents,
                  "EUR"
                )}
              </dd>
              <dt>Cash refund from this change</dt>
              <dd>{formatCents(0, "EUR")}</dd>
            </dl>
            <p>
              Positive forfeited fees remain paid. A cash refund or goodwill credit requires its own
              reviewed action.
            </p>
          {/if}
        {:else if review.action === "entitlements"}
          <p>Replace all subscription entitlement overrides with:</p>
          <pre class="overflow-auto">{JSON.stringify(
              Object.fromEntries(
                review.entries.map((entry) => [entry.key, JSON.parse(entry.value)])
              ),
              null,
              2
            )}</pre>
        {:else}<p>{checkoutPreview}</p>
          <p>
            Reconcile the checkout with its provider. Depending on provider state, this can cancel
            or refund the attempt. Recovery may remain pending.
          </p>{/if}
      </div>
    {:else}
      <form
        id="operator-billing-change"
        class="space-y-3"
        onsubmit={(event) => {
          event.preventDefault();
          void prepare();
        }}
      >
        <Label for="billing-action">Action</Label>
        <select
          id="billing-action"
          class="w-full border border-border bg-background p-2"
          bind:value={action}
          disabled={busy}
        >
          <option value="credit">Credit or debit EUR wallet</option>
          <option value="entitlements">Edit entitlement overrides</option>
          <option value="tier">Assign billing arrangement now</option>
          <option value="checkout">Inspect and recover checkout</option>
          <option value="refund">Refund a completed invoice payment</option>
          <option value="disable_auto">Inspect and disable automatic top-ups</option>
        </select>
        {#if action === "credit"}<Label for="billing-amount"
            >EUR amount (negative removes credit)</Label
          ><Input id="billing-amount" bind:value={amount} placeholder="79.00" disabled={busy} />
        {:else if action === "tier"}<Label for="billing-tier">Catalogue tier name</Label><Input
            id="billing-tier"
            bind:value={tierName}
            placeholder="payg"
            disabled={busy}
          />
        {:else if action === "entitlements"}<Label for="billing-entitlements"
            >Complete override map (JSON)</Label
          ><Textarea id="billing-entitlements" bind:value={overrides} disabled={busy} />
          <p class="text-xs text-muted-foreground">
            An empty object removes subscription overrides and uses the tier. A retention cap of 0
            permits unlimited retention; the account or asset retention policy still controls
            deletion.
          </p>
        {:else if action === "refund"}<Label for="billing-refund-invoice">Paid invoice UUID</Label
          ><Input id="billing-refund-invoice" bind:value={invoiceId} disabled={busy} />
          <p class="text-xs text-muted-foreground">
            Full original card payment only. Earlier credits or reversals require manual
            reconciliation. The subscription stays active until changed separately.
          </p>
        {:else if action === "disable_auto"}<p class="text-sm">
            Review current automation and disable future automatic payments.
          </p>
        {:else}<Label for="billing-reservation">Checkout reservation ID</Label><Input
            id="billing-reservation"
            bind:value={reservationId}
            disabled={busy}
          />{/if}
        <Label for="billing-reason">Audit reason</Label><Textarea
          id="billing-reason"
          bind:value={reason}
          disabled={busy}
        />
      </form>
    {/if}
    {#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
    {#if receipt}<p role="status" class="text-sm text-success">{receipt}</p>{/if}
  </div>
  <div class="slab-actions flex border-t border-border">
    {#if review}
      <Button
        class="rounded-none"
        disabled={busy || (submitted && review.action === "tier")}
        onclick={() => void apply()}
        >{busy ? "Applying…" : submitted ? "Retry same operation" : "Apply reviewed change"}</Button
      >
      {#if submitted && review.action === "tier"}
        <Button
          class="rounded-none"
          variant="outline"
          disabled={busy}
          onclick={async () => {
            await onChanged?.();
            review = null;
            submitted = false;
          }}>Reload billing before another assignment</Button
        >
      {/if}
      {#if !submitted}<Button
          class="rounded-none"
          variant="outline"
          disabled={busy}
          onclick={() => {
            review = null;
          }}>Edit request</Button
        >{/if}
    {:else}<Button class="rounded-none" type="submit" form="operator-billing-change" disabled={busy}
        >{busy ? "Inspecting…" : "Review change"}</Button
      >{/if}
  </div>
</section>
