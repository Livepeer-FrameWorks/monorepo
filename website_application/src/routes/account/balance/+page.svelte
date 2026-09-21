<script lang="ts">
  import { onDestroy, onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { toast } from "$lib/stores/toast.js";
  import { getDocsSiteUrl } from "$lib/config";
  import {
    formatCents,
    formatCurrency,
    formatEcbRate,
    formatEurConversion,
    type CurrencyConversionFields,
  } from "$lib/utils/formatters";
  import { topupCurrency } from "$lib/utils/presentment-currency";
  import {
    GetPrepaidBalanceStore,
    GetBalanceTransactionsStore,
    GetBillingStatusStore,
    GetBillingDetailsStore,
    CreateCardTopupStore,
    CreateCryptoTopupStore,
    GetCryptoTopupStatusStore,
  } from "$houdini";

  // Icons
  const WalletIcon = getIconComponent("Wallet");
  const CreditCardIcon = getIconComponent("CreditCard");
  const CoinsIcon = getIconComponent("Coins");
  const HistoryIcon = getIconComponent("History");
  const RefreshIcon = getIconComponent("RefreshCw");
  const AlertIcon = getIconComponent("AlertTriangle");
  const CheckIcon = getIconComponent("Check");
  const CopyIcon = getIconComponent("Copy");

  // Stores
  const balanceQuery = new GetPrepaidBalanceStore();
  const transactionsQuery = new GetBalanceTransactionsStore();
  const billingStatusQuery = new GetBillingStatusStore();
  const billingDetailsQuery = new GetBillingDetailsStore();
  const cardTopupMutation = new CreateCardTopupStore();
  const cryptoTopupMutation = new CreateCryptoTopupStore();
  const cryptoStatusMutation = new GetCryptoTopupStatusStore();
  const docsSiteUrl = getDocsSiteUrl().replace(/\/$/, "");

  // The prepaid balance and its transactions are EUR. Top-ups are charged in
  // the tenant's presentment currency, which follows the billing country, and
  // credit EUR at the ECB rate locked when the checkout or quote is created.
  const LEDGER_CURRENCY = "EUR";

  // State
  let loading = $state(true);
  // Null until billing details report the tenant's currency; the top-up form needs it.
  let presentmentCurrency = $state<string | null>(null);
  let balance = $state<{
    balanceCents: number;
    reservedBalanceCents: number;
    availableBalanceCents: number;
    currency: string;
    isLowBalance: boolean;
  } | null>(null);
  let transactions = $state<
    Array<{
      id: string;
      amountCents: number;
      balanceAfterCents: number;
      transactionType: string;
      description: string | null;
      createdAt: string;
    }>
  >([]);
  let totalTransactions = $state(0);

  // Top-up form state. LPT is hidden until a non-Chainlink price source ships.
  let topupAmount = $state(10);
  let topupMethod = $state<"card" | "crypto">("card");
  let cardProvider = $state<"STRIPE" | "MOLLIE">("STRIPE");
  let availableCardProviders = $state<Array<"STRIPE" | "MOLLIE">>([]);
  let cryptoAsset = $state<"ETH" | "USDC">("USDC");
  let topupLoading = $state(false);
  let topupGuidance = $state<string | null>(null);
  let minimumTopupAmount = $derived(topupMethod === "card" ? 5 : 0.01);

  // Crypto deposit state — populated post-quote.
  let cryptoDeposit = $state<{
    topupId: string;
    depositAddress: string;
    asset: string;
    expiresAt: string;
    expectedAmountToken: string;
    quotedPriceUsd: string;
    quoteSource: string;
    network: string;
    conversion: CurrencyConversionFields | null;
  } | null>(null);

  // A non-EUR card checkout is shown with its EUR credit before redirecting.
  let cardCheckout = $state<{
    checkoutUrl: string;
    amountCents: number;
    currency: string;
    conversion: CurrencyConversionFields;
  } | null>(null);

  // Polling status — set from GetCryptoTopupStatus on a 15s interval.
  let cryptoStatus = $state<{
    status: string;
    txHash: string | null;
    confirmations: number;
    creditedAmountCents: number | null;
    creditedAmountCurrency: string | null;
    conversion: CurrencyConversionFields | null;
  } | null>(null);

  let pollTimer: ReturnType<typeof setInterval> | null = null;
  const POLL_INTERVAL_MS = 15_000;

  onMount(() => {
    loadData();
  });

  onDestroy(() => {
    stopPolling();
  });

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  async function pollOnce(topupId: string) {
    try {
      const result = await cryptoStatusMutation.mutate({ topupId });
      const data = result.data?.cryptoTopupStatus;
      if (!data) return;
      cryptoStatus = {
        status: data.status,
        txHash: data.txHash ?? null,
        confirmations: data.confirmations,
        creditedAmountCents: data.creditedAmountCents ?? null,
        creditedAmountCurrency: data.creditedAmountCurrency ?? null,
        conversion: data.conversion ?? null,
      };
      if (data.status === "completed") {
        stopPolling();
        toast.success("Top-up credited!");
        await loadData();
      } else if (data.status === "expired") {
        stopPolling();
      }
    } catch {
      // Silent — next tick will try again. Don't spam the user with toasts.
    }
  }

  function startPolling(topupId: string) {
    stopPolling();
    pollOnce(topupId);
    pollTimer = setInterval(() => pollOnce(topupId), POLL_INTERVAL_MS);
  }

  async function loadData() {
    loading = true;
    try {
      const [balanceResult, txResult, billingResult, detailsResult] = await Promise.all([
        balanceQuery.fetch(),
        transactionsQuery.fetch({ variables: { page: { first: 10 } } }),
        billingStatusQuery.fetch(),
        billingDetailsQuery.fetch().catch(() => null),
      ]);

      if (balanceResult.data?.prepaidBalance) {
        balance = {
          balanceCents: balanceResult.data.prepaidBalance.balanceCents,
          reservedBalanceCents: balanceResult.data.prepaidBalance.reservedBalanceCents,
          availableBalanceCents: balanceResult.data.prepaidBalance.availableBalanceCents,
          currency: balanceResult.data.prepaidBalance.currency,
          isLowBalance: balanceResult.data.prepaidBalance.isLowBalance,
        };
      }

      if (txResult.data?.balanceTransactionsConnection) {
        transactions = txResult.data.balanceTransactionsConnection.nodes.map((n) => ({
          id: n.id,
          amountCents: n.amountCents,
          balanceAfterCents: n.balanceAfterCents,
          transactionType: n.transactionType,
          description: n.description,
          createdAt: n.createdAt,
        }));
        totalTransactions = txResult.data.balanceTransactionsConnection.totalCount;
      }

      presentmentCurrency = topupCurrency(detailsResult?.data?.billingDetails);
      const configuredProviders = billingResult.data?.billingStatus?.setupProviders ?? [];
      availableCardProviders = configuredProviders
        .map((provider) => provider.toUpperCase())
        .filter(
          (provider): provider is "STRIPE" | "MOLLIE" =>
            provider === "STRIPE" || provider === "MOLLIE"
        );
      if (!availableCardProviders.includes(cardProvider) && availableCardProviders[0]) {
        cardProvider = availableCardProviders[0];
      }
    } catch {
      toast.error("Failed to load balance data");
    } finally {
      loading = false;
    }
  }

  async function handleCardTopup() {
    if (!presentmentCurrency) return;
    if (!availableCardProviders.includes(cardProvider)) {
      toast.error("No fiat payment provider is currently configured");
      return;
    }
    if (topupAmount < 5) {
      toast.error(`Fiat top-ups have a ${formatCurrency(5, presentmentCurrency)} minimum`);
      return;
    }

    topupLoading = true;
    topupGuidance = null;
    cardCheckout = null;
    try {
      const result = await cardTopupMutation.mutate({
        input: {
          amountCents: Math.round(topupAmount * 100),
          provider: cardProvider,
          successUrl: `${window.location.origin}${resolve("/account/balance")}?success=true`,
          cancelUrl: `${window.location.origin}${resolve("/account/balance")}?cancelled=true`,
        },
      });

      if (result.errors?.length) {
        throw new Error(paymentErrorMessage(result.errors[0], "Failed to create checkout session"));
      }

      const checkout = result.data?.createCardTopup;
      if (checkout?.checkoutUrl) {
        if (checkout.conversion && formatEurConversion(checkout.conversion)) {
          cardCheckout = {
            checkoutUrl: checkout.checkoutUrl,
            amountCents: checkout.amountCents,
            currency: checkout.currency,
            conversion: checkout.conversion,
          };
        } else {
          window.location.href = checkout.checkoutUrl;
        }
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "Failed to create checkout session";
      topupGuidance = message;
      toast.error(message);
    } finally {
      topupLoading = false;
    }
  }

  async function handleCryptoTopup() {
    if (!presentmentCurrency) return;
    if (topupAmount < 0.01) {
      toast.error(`Crypto top-ups have a ${formatCurrency(0.01, presentmentCurrency)} minimum`);
      return;
    }

    topupLoading = true;
    topupGuidance = null;
    cardCheckout = null;
    try {
      const result = await cryptoTopupMutation.mutate({
        input: {
          amountCents: Math.round(topupAmount * 100),
          asset: cryptoAsset,
        },
      });

      if (result.errors?.length) {
        throw new Error(paymentErrorMessage(result.errors[0], "Failed to create deposit address"));
      }

      if (result.data?.createCryptoTopup) {
        const r = result.data.createCryptoTopup;
        cryptoDeposit = {
          topupId: r.topupId,
          depositAddress: r.depositAddress,
          asset: r.assetSymbol,
          expiresAt: r.expiresAt,
          expectedAmountToken: r.expectedAmountToken,
          quotedPriceUsd: r.quotedPriceUsd,
          quoteSource: r.quoteSource,
          network: r.network,
          conversion: r.conversion ?? null,
        };
        cryptoStatus = null;
        startPolling(r.topupId);
        toast.success("Deposit address created");
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : "Failed to create deposit address";
      topupGuidance = message;
      toast.error(message);
    } finally {
      topupLoading = false;
    }
  }

  function copyAddress() {
    if (cryptoDeposit?.depositAddress) {
      navigator.clipboard.writeText(cryptoDeposit.depositAddress);
      toast.success("Address copied to clipboard");
    }
  }

  function conversionCredit(conversion: CurrencyConversionFields | null): string | null {
    if (!conversion) return null;
    return formatEurConversion(conversion) ?? formatCents(conversion.eurAmountCents, "EUR");
  }

  function conversionRate(conversion: CurrencyConversionFields): string | null {
    return formatEcbRate(
      conversion.originalCurrency,
      conversion.unitsPerEur,
      conversion.referenceDate
    );
  }

  function formatDate(dateStr: string): string {
    return new Date(dateStr).toLocaleDateString("en-US", {
      month: "short",
      day: "numeric",
      year: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    });
  }

  function getTransactionIcon(type: string) {
    switch (type) {
      case "topup":
        return CheckIcon;
      case "usage":
        return HistoryIcon;
      default:
        return CoinsIcon;
    }
  }

  function isPositiveAmount(cents: number): boolean {
    return cents >= 1;
  }

  function paymentErrorMessage(error: unknown, fallback: string): string {
    if (!error || typeof error !== "object") return fallback;
    const candidate = error as {
      message?: unknown;
      extensions?: { code?: unknown; required_fields?: unknown };
    };
    const fields = Array.isArray(candidate.extensions?.required_fields)
      ? candidate.extensions.required_fields.filter(
          (field): field is string => typeof field === "string" && field.length > 0
        )
      : [];
    if (candidate.extensions?.code === "BILLING_PROFILE_REQUIRED") {
      const suffix = fields.length > 0 ? ` Required: ${fields.join(", ")}.` : "";
      return `This payment needs a full tax invoice. Complete your billing profile before requesting a new quote.${suffix}`;
    }
    return typeof candidate.message === "string" && candidate.message.length > 0
      ? candidate.message
      : fallback;
  }
</script>

<svelte:head>
  <title>Balance - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <WalletIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Prepaid Balance</h1>
          <p class="text-sm text-muted-foreground">Manage your prepaid balance and top-ups</p>
        </div>
      </div>
      <Button variant="ghost" onclick={loadData} disabled={loading}>
        <RefreshIcon class="w-4 h-4 {loading ? 'animate-spin' : ''}" />
      </Button>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="flex items-center justify-center min-h-64">
        <div class="loading-spinner w-8 h-8"></div>
      </div>
    {:else}
      <div class="dashboard-grid">
        <!-- Current Balance Card -->
        <div class="slab">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <WalletIcon class="w-4 h-4 text-primary" />
              <h3>Current Balance</h3>
            </div>
          </div>
          <div class="slab-body--padded">
            {#if balance}
              <div
                class="text-4xl font-bold tabular-nums"
                class:text-destructive={balance.availableBalanceCents < 0}
              >
                {formatCents(balance.availableBalanceCents, balance.currency)}
              </div>
              <p class="text-xs text-muted-foreground mt-2">
                {formatCents(balance.balanceCents, balance.currency)} settled ·
                {formatCents(balance.reservedBalanceCents, balance.currency)} reserved in active usage
              </p>
              {#if balance.isLowBalance}
                <div class="flex items-center gap-2 mt-3 text-warning">
                  <AlertIcon class="w-4 h-4" />
                  <span class="text-sm">Low balance warning</span>
                </div>
              {/if}
              <p class="text-sm text-muted-foreground mt-4">
                Usage is itemized by meter; your current pricing determines which items are charged.
                <!-- eslint-disable svelte/no-navigation-without-resolve -->
                <a
                  href={`${docsSiteUrl}/builders/billing`}
                  class="text-primary hover:underline"
                  target="_blank"
                  rel="noopener">Learn more</a
                >
                <!-- eslint-enable svelte/no-navigation-without-resolve -->
              </p>
            {:else}
              <p class="text-muted-foreground">No balance data available</p>
            {/if}
          </div>
        </div>

        <!-- Top-Up Card -->
        <div class="slab">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <CoinsIcon class="w-4 h-4 text-success" />
              <h3>Top Up Balance</h3>
            </div>
          </div>
          {#if !presentmentCurrency}
            <div class="slab-body--padded">
              <div class="flex items-start gap-2 text-destructive" role="alert">
                <AlertIcon class="w-4 h-4 mt-0.5 shrink-0" />
                <p class="text-sm">
                  Your billing currency could not be loaded, so top-ups are unavailable. Reload the
                  page, or check your billing details in settings.
                </p>
              </div>
              <Button href={resolve("/settings")} variant="ghost" size="sm" class="mt-2 px-0">
                Billing details
              </Button>
            </div>
          {:else}
            <div class="slab-body--padded space-y-4">
              <!-- Amount -->
              <div>
                <span class="block text-sm font-medium text-muted-foreground mb-2"
                  >Amount ({presentmentCurrency})</span
                >
                <div class="flex gap-2">
                  {#each topupMethod === "card" ? [5, 10, 25, 50, 100] : [0.01, 1, 5, 10, 25] as amount (amount)}
                    <Button
                      variant={topupAmount === amount ? "default" : "outline"}
                      size="sm"
                      onclick={() => (topupAmount = amount)}
                    >
                      {formatCurrency(amount, presentmentCurrency)}
                    </Button>
                  {/each}
                </div>
                {#if presentmentCurrency !== LEDGER_CURRENCY}
                  <p class="mt-2 text-xs text-muted-foreground">
                    Charged in {presentmentCurrency}, the currency of your billing country. Your
                    balance is credited in EUR at the ECB reference rate locked when the checkout or
                    deposit quote is created.
                  </p>
                {/if}
                <div class="mt-2">
                  <Input
                    type="number"
                    min={minimumTopupAmount}
                    max={100000}
                    step={topupMethod === "card" ? 1 : 0.01}
                    bind:value={topupAmount}
                    placeholder="Custom amount"
                    class="w-32"
                  />
                </div>
              </div>

              <!-- Method -->
              <div>
                <span class="block text-sm font-medium text-muted-foreground mb-2"
                  >Payment Method</span
                >
                <div class="flex gap-2">
                  <Button
                    variant={topupMethod === "card" ? "default" : "outline"}
                    onclick={() => (topupMethod = "card")}
                    class="gap-2"
                  >
                    <CreditCardIcon class="w-4 h-4" />
                    Card
                  </Button>
                  <Button
                    variant={topupMethod === "crypto" ? "default" : "outline"}
                    onclick={() => (topupMethod = "crypto")}
                    class="gap-2"
                  >
                    <CoinsIcon class="w-4 h-4" />
                    Crypto
                  </Button>
                </div>
              </div>

              {#if topupMethod === "crypto"}
                <div>
                  <span class="block text-sm font-medium text-muted-foreground mb-2"
                    >Asset (ETH or USDC)</span
                  >
                  <div class="flex gap-2">
                    {#each ["ETH", "USDC"] as asset (asset)}
                      <Button
                        variant={cryptoAsset === asset ? "default" : "outline"}
                        size="sm"
                        onclick={() => (cryptoAsset = asset as "ETH" | "USDC")}
                      >
                        {asset}
                      </Button>
                    {/each}
                  </div>
                </div>
                <p class="text-xs text-muted-foreground">
                  Up to €100 in EUR can use the customer-anonymous simplified VAT document path. A
                  larger payment or a VAT-number claim needs a complete billing profile before an
                  address is issued.
                </p>
              {:else}
                <div>
                  <span class="block text-sm font-medium text-muted-foreground mb-2"
                    >Fiat Provider</span
                  >
                  <div class="flex gap-2">
                    {#each availableCardProviders as provider (provider)}
                      <Button
                        variant={cardProvider === provider ? "default" : "outline"}
                        size="sm"
                        onclick={() => (cardProvider = provider as "STRIPE" | "MOLLIE")}
                      >
                        {provider === "STRIPE" ? "Stripe" : "Mollie"}
                      </Button>
                    {/each}
                  </div>
                  {#if availableCardProviders.length === 0}
                    <p class="mt-2 text-xs text-warning">
                      No fiat payment provider is currently configured. Use crypto or contact
                      support.
                    </p>
                  {:else}
                    <p class="mt-2 text-xs text-muted-foreground">
                      Minimum {formatCurrency(5, presentmentCurrency)}. The provider may enforce a
                      higher currency-specific minimum.
                    </p>
                  {/if}
                </div>
              {/if}

              {#if topupGuidance}
                <div class="rounded-md border border-warning/40 bg-warning/10 p-3 text-sm">
                  <p>{topupGuidance}</p>
                  {#if topupGuidance.includes("billing profile")}
                    <Button href={resolve("/settings")} variant="ghost" size="sm" class="mt-2 px-0">
                      Complete billing profile
                    </Button>
                  {/if}
                </div>
              {/if}
            </div>
            <div class="slab-actions">
              {#if topupMethod === "card"}
                <Button
                  onclick={handleCardTopup}
                  disabled={topupLoading || topupAmount < 5 || availableCardProviders.length === 0}
                  class="gap-2"
                >
                  {topupLoading
                    ? "Processing..."
                    : `Pay ${formatCurrency(topupAmount, presentmentCurrency)}`}
                </Button>
              {:else}
                <Button
                  onclick={handleCryptoTopup}
                  disabled={topupLoading || topupAmount < 0.01}
                  class="gap-2"
                >
                  {topupLoading ? "Creating..." : "Get Deposit Address"}
                </Button>
              {/if}
            </div>
          {/if}
        </div>

        <!-- Card checkout in a non-EUR presentment currency -->
        {#if cardCheckout}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <CreditCardIcon class="w-4 h-4 text-success" />
                <h3>Checkout Ready</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <p class="text-sm text-foreground">
                Pays <span class="font-semibold tabular-nums"
                  >{formatCents(cardCheckout.amountCents, cardCheckout.currency)}</span
                >
                and credits
                <span class="font-semibold tabular-nums"
                  >{conversionCredit(cardCheckout.conversion)}</span
                >.
              </p>
            </div>
            <div class="slab-actions">
              <!-- eslint-disable svelte/no-navigation-without-resolve -->
              <Button href={cardCheckout.checkoutUrl} class="gap-2">Continue to checkout</Button>
              <!-- eslint-enable svelte/no-navigation-without-resolve -->
            </div>
          </div>
        {/if}

        <!-- Crypto Deposit Address (if created) -->
        {#if cryptoDeposit}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <CoinsIcon class="w-4 h-4 text-success" />
                <h3>Deposit {cryptoDeposit.asset}</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <p class="text-sm text-foreground mb-1">
                Send <span class="font-semibold tabular-nums"
                  >{cryptoDeposit.expectedAmountToken}
                  {cryptoDeposit.asset}</span
                >
                to the address below on
                <span class="font-mono text-xs">{cryptoDeposit.network}</span>.
              </p>
              <p class="text-xs text-muted-foreground mb-3">
                Locked at ${cryptoDeposit.quotedPriceUsd}/{cryptoDeposit.asset} ({cryptoDeposit.quoteSource}).
                Quote valid until {formatDate(cryptoDeposit.expiresAt)}.
              </p>
              {#if cryptoDeposit.conversion}
                <p class="text-xs text-muted-foreground mb-3">
                  {formatCents(
                    cryptoDeposit.conversion.originalAmountCents,
                    cryptoDeposit.conversion.originalCurrency
                  )} credits {conversionCredit(cryptoDeposit.conversion)}.
                </p>
              {/if}
              <div
                class="flex items-center gap-2 p-3 bg-muted/30 rounded-md font-mono text-sm break-all"
              >
                <span class="flex-1">{cryptoDeposit.depositAddress}</span>
                <Button variant="ghost" size="sm" onclick={copyAddress}>
                  <CopyIcon class="w-4 h-4" />
                </Button>
              </div>

              {#if cryptoStatus}
                <div class="mt-4 p-3 rounded-md bg-muted/20 text-sm">
                  {#if cryptoStatus.status === "pending"}
                    <p class="text-muted-foreground">Waiting for your transaction…</p>
                  {:else if cryptoStatus.status === "confirming"}
                    <p>
                      Detected{#if cryptoStatus.txHash}
                        (<span class="font-mono text-xs break-all">{cryptoStatus.txHash}</span
                        >){/if}.
                      {cryptoStatus.confirmations} confirmation{cryptoStatus.confirmations === 1
                        ? ""
                        : "s"} so far — crediting once confirmed.
                    </p>
                  {:else if cryptoStatus.status === "completed"}
                    <p class="text-success">
                      Credited {#if cryptoStatus.creditedAmountCents !== null && cryptoStatus.creditedAmountCurrency}{formatCents(
                          cryptoStatus.creditedAmountCents,
                          cryptoStatus.creditedAmountCurrency
                        )}{/if}{#if cryptoStatus.conversion && conversionRate(cryptoStatus.conversion)}
                        ({formatCents(
                          cryptoStatus.conversion.originalAmountCents,
                          cryptoStatus.conversion.originalCurrency
                        )} at {conversionRate(cryptoStatus.conversion)}){/if}.
                      {#if cryptoStatus.txHash}
                        <span class="font-mono text-xs break-all">tx: {cryptoStatus.txHash}</span>
                      {/if}
                    </p>
                  {:else if cryptoStatus.status === "expired"}
                    <p class="text-warning">Address expired. Generate a new one to continue.</p>
                  {/if}
                </div>
              {/if}
            </div>
          </div>
        {/if}

        <!-- Transaction History -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <HistoryIcon class="w-4 h-4 text-muted-foreground" />
              <h3>Transaction History</h3>
            </div>
            <span class="text-sm text-muted-foreground">{totalTransactions} transactions</span>
          </div>
          <div class="slab-body">
            {#if transactions.length === 0}
              <div class="p-4 text-center text-muted-foreground">No transactions yet</div>
            {:else}
              <div class="divide-y divide-[hsl(var(--tn-fg-gutter)/0.3)]">
                {#each transactions as tx (tx.id)}
                  {@const TxIcon = getTransactionIcon(tx.transactionType)}
                  <div class="flex items-center justify-between p-4">
                    <div class="flex items-center gap-3">
                      {#if isPositiveAmount(tx.amountCents)}
                        <div
                          class="w-8 h-8 rounded-full flex items-center justify-center bg-success/10"
                        >
                          <TxIcon class="w-4 h-4 text-success" />
                        </div>
                      {:else}
                        <div class="w-8 h-8 rounded-full flex items-center justify-center bg-muted">
                          <TxIcon class="w-4 h-4 text-muted-foreground" />
                        </div>
                      {/if}
                      <div>
                        <p class="font-medium capitalize">{tx.transactionType}</p>
                        <p class="text-sm text-muted-foreground">
                          {tx.description || formatDate(tx.createdAt)}
                        </p>
                      </div>
                    </div>
                    <div class="text-right">
                      {#if isPositiveAmount(tx.amountCents)}
                        <p class="font-medium tabular-nums text-success">
                          +{formatCents(tx.amountCents, LEDGER_CURRENCY)}
                        </p>
                      {:else}
                        <p class="font-medium tabular-nums text-foreground">
                          {formatCents(tx.amountCents, LEDGER_CURRENCY)}
                        </p>
                      {/if}
                      <p class="text-sm text-muted-foreground tabular-nums">
                        Balance: {formatCents(tx.balanceAfterCents, LEDGER_CURRENCY)}
                      </p>
                    </div>
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>
