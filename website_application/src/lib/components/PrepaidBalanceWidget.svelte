<script module lang="ts">
  // The prepaid admission rule as Purser applies it: refusal on the available
  // balance, suspension on the settled balance. The billing and balance pages
  // both show this copy.
  export const BALANCE_RULES = [
    { when: "Funded", effect: "Streams, viewers and recordings run normally." },
    {
      when: "Payment grace",
      effect:
        "Previously funded accounts get temporary paid continuity, bounded by the displayed deadline and spending allowance. PAYG usage remains payable.",
    },
    {
      when: "Grace ends",
      effect:
        "Eligible accounts return to Free limits. Existing recordings have a recovery window; excess storage blocks new recording and uploads.",
    },
  ] as const;

  export const BALANCE_NOTICES = {
    suspended:
      "Paid service is restricted. Top up to settle outstanding usage and restore funded service.",
    spent:
      "Prepaid balance exhausted. Paid continuity is unavailable or its grace limit has been reached. Eligible accounts return to Free limits; unpaid usage is retained. If the change is temporarily unavailable, existing broadcasts continue while new paid work is blocked. The existing negative-balance suspension threshold still applies.",
    grace:
      "Payment grace is active. Broadcasts and recording continue at PAYG rates within the remaining time and spending allowance.",
    low: "Your prepaid balance is running low. Top up before it runs out to avoid restrictions.",
  } as const;

  export type PaymentGraceInfo = {
    active: boolean;
    endsAt?: string | null;
    remainingCreditCents: number;
    mediaRecoveryUntil?: string | null;
  };

  export type BalanceNotice = keyof typeof BALANCE_NOTICES;

  // suspended is Purser's subscription state; the €0 check is on the
  // available balance, which admission uses.
  export function balanceNotice(facts: {
    admittedOnBalance: boolean;
    suspended: boolean;
    availableBalanceCents: number;
    isLowBalance: boolean;
    paymentGrace?: PaymentGraceInfo | null;
    now?: number;
  }): BalanceNotice | null {
    if (!facts.admittedOnBalance) return null;
    if (facts.suspended) return "suspended";
    if (
      facts.paymentGrace?.active &&
      facts.paymentGrace.endsAt &&
      new Date(facts.paymentGrace.endsAt).getTime() > (facts.now ?? Date.now())
    )
      return "grace";
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
  let paymentGrace = $state<PaymentGraceInfo | null>(null);
  let drainRate = $state(0);
  let error = $state<string | null>(null);
  let now = $state(Date.now());
  $effect(() => {
    const timer = setInterval(() => {
      now = Date.now();
    }, 1000);
    return () => clearInterval(timer);
  });

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
        paymentGrace = result.data.prepaidBalance.paymentGrace;
        drainRate = result.data.prepaidBalance.drainRateCentsPerHour;
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
    balanceNotice({
      admittedOnBalance,
      suspended,
      availableBalanceCents,
      isLowBalance,
      paymentGrace,
      now,
    })
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
        {:else if notice === "grace"}
          <div class="rounded-md border border-warning/30 bg-warning/10 p-3 text-sm" role="status">
            <p>{BALANCE_NOTICES.grace}</p>
            <p class="mt-1">
              Until {new Date(paymentGrace!.endsAt!).toLocaleString()} or {formatCents(
                paymentGrace!.remainingCreditCents,
                currency
              )} more usage, whichever comes first.
            </p>
          </div>
        {:else if notice === "low"}
          <div
            class="flex items-center gap-2 p-2 bg-warning/10 rounded-md border border-warning/30"
          >
            <AlertIcon class="w-4 h-4 text-warning shrink-0" />
            <span class="text-sm text-warning">{BALANCE_NOTICES.low}</span>
          </div>
        {/if}
        {#if availableBalanceCents > 0 && drainRate > 0}
          <p class="text-xs text-muted-foreground">
            About {Math.max(1, Math.floor(availableBalanceCents / drainRate))} hours of prepaid balance
            at the last hour's usage rate. Usage can change.
          </p>
        {/if}
        {#if billingModel === "postpaid" && paymentGrace?.mediaRecoveryUntil}
          <p class="text-sm text-warning" role="status">
            Your previous paid media has a recovery window until {new Date(
              paymentGrace.mediaRecoveryUntil
            ).toLocaleString()}. Top up to retain paid storage, or export and remove excess media.
            Existing recording expiry dates still apply.
          </p>
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
