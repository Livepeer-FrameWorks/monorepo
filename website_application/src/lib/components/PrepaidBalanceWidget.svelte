<script module lang="ts">
  // The prepaid admission rule as Purser applies it: refusal on the available
  // balance, suspension on the settled balance. The billing and balance pages
  // both show this copy.
  export const BALANCE_RULES = [
    { when: "Above €0", effect: "Streams, viewers and recordings run normally." },
    {
      when: "€0 or below",
      effect:
        "Running streams and their viewers continue. New streams and new viewers are refused, and a viewer or encoder that disconnects cannot reconnect.",
    },
    {
      when: "Below -€10 settled",
      effect: "The account is suspended and running streams stop until a top-up.",
    },
  ] as const;

  export const BALANCE_NOTICES = {
    suspended:
      "Your account is suspended because its settled balance fell below -€10, so running streams stopped. Top up to stream again.",
    spent:
      "Balance spent. Running streams and their viewers continue, but new streams and new viewers are refused, and a viewer or encoder that disconnects cannot reconnect. Below -€10 settled, running streams stop.",
    low: "Low balance. At €0 new streams and new viewers are refused; top up before then.",
  } as const;

  export type BalanceNotice = keyof typeof BALANCE_NOTICES;

  // suspended is Purser's subscription state; the €0 check is on the
  // available balance, which admission uses.
  export function balanceNotice(facts: {
    admittedOnBalance: boolean;
    suspended: boolean;
    availableBalanceCents: number;
    isLowBalance: boolean;
  }): BalanceNotice | null {
    if (!facts.admittedOnBalance) return null;
    if (facts.suspended) return "suspended";
    if (facts.availableBalanceCents <= 0) return "spent";
    if (facts.isLowBalance) return "low";
    return null;
  }
</script>

<script lang="ts">
  import { resolve } from "$app/paths";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { GetPrepaidBalanceStore } from "$houdini";
  import { formatCents } from "$lib/utils/formatters";

  const WalletIcon = getIconComponent("Wallet");
  const AlertIcon = getIconComponent("AlertTriangle");
  const ArrowRightIcon = getIconComponent("ArrowRight");

  // Only a prepaid account is admitted on its balance. A postpaid account's
  // balance is credit taken by its next invoice, so the spent-balance rule
  // does not apply to it.
  let { billingModel, suspended = false }: { billingModel?: string | null; suspended?: boolean } =
    $props();

  const balanceQuery = new GetPrepaidBalanceStore();

  let loading = $state(true);
  let balanceCents = $state(0);
  let reservedBalanceCents = $state(0);
  let availableBalanceCents = $state(0);
  // The prepaid ledger is EUR; the response confirms it.
  let currency = $state("EUR");
  let isLowBalance = $state(false);
  let error = $state<string | null>(null);

  $effect(() => {
    loadBalance();
  });

  async function loadBalance() {
    loading = true;
    error = null;
    try {
      const result = await balanceQuery.fetch();
      if (result.data?.prepaidBalance) {
        balanceCents = result.data.prepaidBalance.balanceCents;
        reservedBalanceCents = result.data.prepaidBalance.reservedBalanceCents;
        availableBalanceCents = result.data.prepaidBalance.availableBalanceCents;
        currency = result.data.prepaidBalance.currency;
        isLowBalance = result.data.prepaidBalance.isLowBalance;
      }
    } catch (e) {
      error = e instanceof Error ? e.message : "Failed to load balance";
    } finally {
      loading = false;
    }
  }

  const balanceDisplay = $derived(formatCents(availableBalanceCents, currency));
  const admittedOnBalance = $derived(billingModel !== "postpaid");
  const notice = $derived(
    balanceNotice({ admittedOnBalance, suspended, availableBalanceCents, isLowBalance })
  );
  const isSuspended = $derived(notice === "suspended");
  const isSpent = $derived(admittedOnBalance && availableBalanceCents <= 0);
</script>

<div class="slab">
  <div class="slab-header">
    <div class="flex items-center gap-2">
      <WalletIcon class="w-4 h-4 text-primary" />
      <h3>Prepaid Balance</h3>
    </div>
  </div>

  <div class="slab-body--padded">
    {#if loading}
      <div class="flex items-center justify-center py-4">
        <div class="loading-spinner w-6 h-6"></div>
      </div>
    {:else if error}
      <div class="text-destructive text-sm">{error}</div>
    {:else}
      <div class="space-y-3">
        <div class="flex items-baseline gap-2">
          <span
            class="text-3xl font-bold tabular-nums"
            class:text-destructive={isSpent || isSuspended}
            class:text-foreground={!isSpent && !isSuspended}
          >
            {balanceDisplay}
          </span>
          <span class="text-sm text-muted-foreground">{currency}</span>
        </div>
        <div class="text-xs text-muted-foreground">
          {formatCents(balanceCents, currency)} settled ·
          {formatCents(reservedBalanceCents, currency)} reserved in active usage
        </div>

        {#if notice === "suspended" || notice === "spent"}
          <div
            class="flex items-start gap-2 p-2 bg-destructive/10 rounded-md border border-destructive/30"
          >
            <AlertIcon class="w-4 h-4 text-destructive shrink-0 mt-0.5" />
            <span class="text-sm text-destructive">{BALANCE_NOTICES[notice]}</span>
          </div>
        {:else if notice === "low"}
          <div
            class="flex items-center gap-2 p-2 bg-warning/10 rounded-md border border-warning/30"
          >
            <AlertIcon class="w-4 h-4 text-warning shrink-0" />
            <span class="text-sm text-warning">{BALANCE_NOTICES.low}</span>
          </div>
        {/if}
      </div>
    {/if}
  </div>

  <div class="slab-actions">
    <Button href={resolve("/account/balance")} variant="ghost" class="gap-2">
      Top Up Balance
      <ArrowRightIcon class="w-4 h-4" />
    </Button>
  </div>
</div>
