<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { GetAutomaticTopupStore, SetAutomaticTopupStore } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { formatCents } from "$lib/utils/formatters";
  function cashMinor(value: string, currency: string, allowZero = false) {
    if (!/^\d+(?:\.\d{1,2})?$/.test(value.trim()))
      throw new Error(`Use a ${currency} amount with at most two decimal places.`);
    const minor = Math.round(Number(value) * 100);
    if (!Number.isSafeInteger(minor) || minor < (allowZero ? 0 : 1))
      throw new Error(`Enter a valid ${currency} amount.`);
    return minor;
  }
  const query = new GetAutomaticTopupStore();
  const mutation = new SetAutomaticTopupStore();
  type Settings = {
    enabled: boolean;
    thresholdEurCents: number;
    netAmountMinor: number;
    monthlyGrossLimitMinor: number;
    currency: string;
    pausedReason: string;
    monthlyReservedOrSpentMinor: number;
    available: boolean;
    unavailableReason: string;
  };
  let settings = $state<Settings | null>(null);
  let busy = $state(false);
  let error = $state("");
  let message = $state("");
  let threshold = $state("5.00");
  let amount = $state("20.00");
  let ceiling = $state("100.00");
  let consent = $state(false);
  onMount(() => {
    void load();
  });
  async function load() {
    try {
      const result = await query.fetch({ policy: "NetworkOnly" });
      if (!result.data?.automaticTopup || result.errors?.length)
        throw new Error(
          result.errors?.[0]?.message ?? "Automatic top-up settings are unavailable."
        );
      settings = result.data.automaticTopup;
      if (settings.enabled) {
        threshold = (settings.thresholdEurCents / 100).toFixed(2);
        amount = (settings.netAmountMinor / 100).toFixed(2);
        ceiling = (settings.monthlyGrossLimitMinor / 100).toFixed(2);
      }
    } catch (e) {
      error = e instanceof Error ? e.message : "Settings are unavailable.";
    }
  }
  async function save(enabled: boolean) {
    if (busy || !settings) return;
    if (enabled && !consent) {
      error = "Authorise the displayed automatic charges before saving.";
      return;
    }
    busy = true;
    error = "";
    message = "";
    try {
      const response = await mutation.mutate({
        input: {
          enabled,
          thresholdEurCents: enabled
            ? cashMinor(threshold, "EUR", true)
            : settings.thresholdEurCents,
          netAmountMinor: enabled ? cashMinor(amount, settings.currency) : settings.netAmountMinor,
          monthlyGrossLimitMinor: enabled
            ? cashMinor(ceiling, settings.currency)
            : settings.monthlyGrossLimitMinor,
          currency: settings.currency,
          consentVersion: enabled ? "prepaid_auto_topup_v1" : "",
        },
      });
      if (!response.data?.setAutomaticTopup || response.errors?.length)
        throw new Error(response.errors?.[0]?.message ?? "Settings could not be saved.");
      settings = response.data.setAutomaticTopup;
      consent = false;
      message = enabled
        ? "Automatic top-ups enabled with your spending limit."
        : "Automatic top-ups disabled. A payment already in progress may still settle.";
    } catch (e) {
      error = e instanceof Error ? e.message : "Settings could not be saved.";
    } finally {
      busy = false;
    }
  }
</script>

<section class="slab mt-6" aria-label="Automatic top-ups">
  <div class="slab-header"><h2>Automatic top-ups</h2></div>
  <div class="slab-body--padded space-y-4">
    <p class="text-sm text-muted-foreground">
      Optional funding from your saved Stripe card. We only credit settled payments. Tax is added to
      the top-up credit amount; your monthly cash limit includes tax and payments in progress.
      Limits reset on the first day of each UTC calendar month.
    </p>
    {#if settings}
      <p class="text-sm">
        {settings.enabled ? "Enabled" : "Disabled"} · {formatCents(
          settings.monthlyReservedOrSpentMinor,
          settings.currency
        )} reserved or spent this month.
      </p>
      {#if settings.pausedReason}<p role="alert" class="text-sm text-destructive">
          {settings.pausedReason}
        </p>{/if}
      {#if !settings.available}<p class="text-sm">
          {settings.unavailableReason}
          <a href={resolve("/account/billing")} class="underline">Review billing</a>.
        </p>{/if}
      {#if settings.available}
        <form
          class="space-y-3"
          onsubmit={(e) => {
            e.preventDefault();
            void save(true);
          }}
        >
          <div class="grid gap-3 sm:grid-cols-3">
            <div>
              <Label for="auto-topup-threshold">When EUR balance falls to</Label><Input
                id="auto-topup-threshold"
                inputmode="decimal"
                bind:value={threshold}
                disabled={busy}
              />
            </div>
            <div>
              <Label for="auto-topup-amount">Top-up credit before tax ({settings.currency})</Label
              ><Input
                id="auto-topup-amount"
                inputmode="decimal"
                bind:value={amount}
                disabled={busy}
              />
            </div>
            <div>
              <Label for="auto-topup-ceiling">Monthly cash limit ({settings.currency})</Label><Input
                id="auto-topup-ceiling"
                inputmode="decimal"
                bind:value={ceiling}
                disabled={busy}
              />
            </div>
          </div>
          <label class="flex items-start gap-2 text-sm"
            ><input type="checkbox" bind:checked={consent} disabled={busy} />I authorise recurring
            charges to my saved Stripe card for these top-ups, up to the monthly cash limit
            including tax. I can disable future automatic charges here at any time.</label
          >
          <Button type="submit" disabled={busy || !consent}
            >{busy
              ? "Saving…"
              : settings.enabled
                ? "Save and resume automatic top-ups"
                : "Enable automatic top-ups"}</Button
          >
        </form>
      {/if}
      {#if settings.enabled}<Button
          variant="outline"
          disabled={busy}
          onclick={() => void save(false)}>Disable automatic top-ups</Button
        >{/if}
    {/if}
    {#if message}<p role="status" class="text-sm">{message}</p>{/if}
    {#if error}<p role="alert" class="text-sm text-destructive">{error}</p>{/if}
  </div>
</section>
