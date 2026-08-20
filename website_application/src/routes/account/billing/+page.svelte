<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import { get } from "svelte/store";
  import { base, resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    fragment,
    GetBillingStatusStore,
    GetBillingDetailsStore,
    GetBillingTiersStore,
    GetInvoicesStore,
    GetPaymentsStore,
    GetMollieMandatesStore,
    CreatePaymentStore,
    CreateStripeCheckoutStore,
    CreateMollieFirstPaymentStore,
    CreateMollieSubscriptionStore,
    ChangeBillingTierStore,
    BillingTierFieldsStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import PrepaidBalanceWidget from "$lib/components/PrepaidBalanceWidget.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { PaymentMethod, type PaymentMethod$options } from "$houdini";

  // Houdini stores
  const billingStatusStore = new GetBillingStatusStore();
  const billingDetailsStore = new GetBillingDetailsStore();
  const billingTiersStore = new GetBillingTiersStore();
  const invoicesStore = new GetInvoicesStore();
  const paymentsStore = new GetPaymentsStore();
  const mollieMandatesStore = new GetMollieMandatesStore();
  const createPaymentMutation = new CreatePaymentStore();
  const stripeCheckoutMutation = new CreateStripeCheckoutStore();
  const mollieFirstPaymentMutation = new CreateMollieFirstPaymentStore();
  const mollieSubscriptionMutation = new CreateMollieSubscriptionStore();
  const changeTierMutation = new ChangeBillingTierStore();

  // Fragment stores for unmasking nested data
  const tierFragmentStore = new BillingTierFieldsStore();

  let isAuthenticated = false;
  let error = $state<string | null>(null);
  let selectedTierId = $state("");
  let setupLoadingProvider = $state<string | null>(null);
  let mollieFirstPaymentMethod = $state("creditcard");
  let changeTierLoadingId = $state<string | null>(null);
  let paymentLoadingInvoiceId = $state<string | null>(null);
  type BillingDocument = {
    id: string;
    kind: "invoice" | "simplified_invoice" | "payment_receipt" | "credit_note";
    document_number: string;
    amount_cents: number;
    currency: string;
    status: string;
    download_filename: string;
  };
  let billingDocuments = $state<BillingDocument[]>([]);
  let invoiceCryptoPayment = $state<{
    invoiceId: string;
    walletAddress: string;
    asset: string;
    network: string;
    expectedAmountToken: string;
    quotedPriceUsd: string;
    quoteSource: string;
    expiresAt: string | null;
  } | null>(null);

  // Derived state from Houdini stores
  let loading = $derived(
    $billingStatusStore.fetching ||
      $billingDetailsStore.fetching ||
      $billingTiersStore.fetching ||
      $invoicesStore.fetching ||
      $paymentsStore.fetching
  );
  let billingStatus = $derived($billingStatusStore.data?.billingStatus ?? null);
  let billingDetails = $derived($billingDetailsStore.data?.billingDetails ?? null);
  let availableTiers = $derived($billingTiersStore.data?.billingTiers ?? []);
  let invoices = $derived($invoicesStore.data?.invoicesConnection?.edges?.map((e) => e.node) ?? []);
  let recentPayments = $derived(
    $paymentsStore.data?.paymentsConnection?.edges?.map((edge) => edge.node) ??
      billingStatus?.recentPayments ??
      []
  );
  let availableSetupProviders = $derived(
    new Set((billingStatus?.setupProviders ?? []).map((provider) => provider.toLowerCase()))
  );
  let availablePaymentMethods = $derived.by(() => {
    const methods =
      (billingStatus as { paymentMethods?: PaymentMethod$options[] } | null)?.paymentMethods ?? [];
    return new Set(methods.map((method) => String(method)));
  });

  // Unmask fragment data for currentTier using get() pattern
  let currentTier = $derived(
    billingStatus?.currentTier ? get(fragment(billingStatus.currentTier, tierFragmentStore)) : null
  );

  // Subscribe to auth store
  const unsubscribeAuth = auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onDestroy(() => {
    unsubscribeAuth();
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    const params = new URLSearchParams(window.location.search);
    const requestedInvoiceId = params.get("invoice");
    showPaymentReturnToast();
    await loadBillingData();
    await handleSetupReturn(params);
    if (requestedInvoiceId && invoices.some((invoice) => invoice.id === requestedInvoiceId)) {
      expandedInvoiceId = requestedInvoiceId;
      document.getElementById(`invoice-${requestedInvoiceId}`)?.scrollIntoView({ block: "center" });
    }
  });

  function showPaymentReturnToast() {
    const params = new URLSearchParams(window.location.search);
    const payment = params.get("payment");
    if (payment === "success") {
      toast.success("Payment submitted. Your invoice will update after confirmation.");
    } else if (payment === "cancelled") {
      toast.info("Payment cancelled");
    } else if (payment === "demo") {
      toast.success("Demo payment created");
    } else {
      return;
    }
    window.history.replaceState({}, "", resolve("/account/billing"));
  }

  async function loadBillingData() {
    try {
      // Load all billing data in parallel
      await Promise.all([
        billingStatusStore.fetch().catch(() => null),
        billingDetailsStore.fetch().catch(() => null),
        billingTiersStore.fetch().catch(() => null),
        invoicesStore.fetch().catch(() => null),
        paymentsStore.fetch({ variables: { page: { first: 50 } } }).catch(() => null),
        loadBillingDocuments(),
      ]);
    } catch (err) {
      console.error("Failed to load billing data:", err);
      error = "Failed to load billing information. Please try again later.";
      toast.error("Failed to load billing information. Please refresh the page.");
    }
  }

  async function loadBillingDocuments() {
    const response = await fetch(`${base}/v1/billing/documents`, {
      credentials: "include",
    });
    if (!response.ok) {
      return;
    }
    const payload = (await response.json()) as { documents?: BillingDocument[] };
    billingDocuments = payload.documents ?? [];
  }

  function billingDocumentLabel(kind: BillingDocument["kind"]) {
    return {
      invoice: "Invoice",
      simplified_invoice: "Simplified invoice",
      payment_receipt: "Payment receipt",
      credit_note: "Credit note",
    }[kind];
  }

  function billingDocumentURL(document: BillingDocument) {
    return `${base}/v1/billing/documents/${document.kind}/${document.id}`;
  }

  async function createPayment(
    invoiceId?: string,
    method: PaymentMethod$options = PaymentMethod.CARD
  ) {
    let targetInvoiceId: string | undefined;
    try {
      const targetInvoice = invoiceId
        ? invoices.find((inv) => inv.id === invoiceId)
        : invoices.find((inv) => inv.status === "PENDING" || inv.status === "OVERDUE");
      targetInvoiceId = targetInvoice?.id;
      if (!targetInvoiceId) {
        toast.error("No payable invoice found");
        return;
      }
      paymentLoadingInvoiceId = targetInvoiceId;
      const result = await createPaymentMutation.mutate({
        input: {
          method,
          invoiceId: targetInvoiceId,
          returnUrl: `${window.location.origin}${resolve("/account/billing")}`,
        },
      });
      const payment = result.data?.createPayment;
      if (payment?.__typename === "Payment") {
        if (payment.walletAddress) {
          invoiceCryptoPayment = {
            invoiceId: targetInvoiceId,
            walletAddress: payment.walletAddress,
            asset: payment.assetSymbol || String(method).replace("CRYPTO_", ""),
            network: payment.network || "",
            expectedAmountToken: payment.expectedAmountToken || "",
            quotedPriceUsd: payment.quotedPriceUsd || "",
            quoteSource: payment.quoteSource || "",
            expiresAt: payment.expiresAt ?? null,
          };
          toast.success("Deposit address created");
        } else if (payment.paymentUrl) {
          window.location.href = payment.paymentUrl;
          return;
        } else {
          toast.success("Payment created");
        }
      }
      await loadBillingData();
    } catch (err) {
      console.error("Failed to create payment:", err);
      toast.error("Failed to process payment. Please try again.");
    } finally {
      if (paymentLoadingInvoiceId === targetInvoiceId) {
        paymentLoadingInvoiceId = null;
      }
    }
  }

  function copyInvoiceCryptoAddress() {
    if (!invoiceCryptoPayment?.walletAddress) return;
    navigator.clipboard.writeText(invoiceCryptoPayment.walletAddress);
    toast.success("Address copied to clipboard");
  }

  function selectedPostpaidTier() {
    return availableTiers.find(
      (tier) =>
        tier.id === selectedTierId &&
        tier.tierLevel >= 1 &&
        tier.basePrice > 0 &&
        !tier.isEnterprise
    );
  }

  function setupErrorMessage(result: { message?: string } | null | undefined, fallback: string) {
    return result?.message || fallback;
  }

  function clearSetupReturnParams() {
    const url = new URL(window.location.href);
    url.searchParams.delete("setup");
    url.searchParams.delete("tier");
    window.history.replaceState({}, "", `${url.pathname}${url.search}${url.hash}`);
  }

  async function handleSetupReturn(params: URLSearchParams) {
    const provider = params.get("setup");
    const tierId = params.get("tier");
    if (provider === "cancelled") {
      toast.info("Billing setup cancelled");
      clearSetupReturnParams();
      return;
    }
    if (provider === "stripe") {
      setupLoadingProvider = "stripe";
      for (let attempt = 0; attempt < 15; attempt++) {
        const response = await billingStatusStore
          .fetch({ policy: "NetworkOnly" })
          .catch(() => null);
        if (response?.data?.billingStatus?.collectionReady) {
          toast.success("Stripe billing setup confirmed");
          clearSetupReturnParams();
          setupLoadingProvider = null;
          return;
        }
        await new Promise<void>((resolveDelay) => window.setTimeout(resolveDelay, 2000));
      }
      toast.info("Stripe setup is still confirming. This page will reflect the webhook shortly.");
      setupLoadingProvider = null;
      return;
    }
    if (provider !== "mollie" || !tierId) return;

    setupLoadingProvider = "mollie";
    for (let attempt = 0; attempt < 15; attempt++) {
      await mollieMandatesStore.fetch({ policy: "NetworkOnly" }).catch(() => null);
      const mandate = $mollieMandatesStore.data?.mollieMandates.find(
        (candidate) => candidate.status.toLowerCase() === "valid"
      );
      if (mandate) {
        const response = await mollieSubscriptionMutation.mutate({
          tierId,
          mandateId: mandate.mandateId,
          description: "FrameWorks postpaid subscription",
        });
        const result = response.data?.createMollieSubscription;
        if (result?.__typename === "MollieSubscription") {
          toast.success("Mollie billing setup confirmed");
          clearSetupReturnParams();
          await loadBillingData();
          setupLoadingProvider = null;
          return;
        }
        toast.error(setupErrorMessage(result, "Failed to create the Mollie subscription"));
        setupLoadingProvider = null;
        return;
      }
      await new Promise<void>((resolveDelay) => window.setTimeout(resolveDelay, 2000));
    }
    toast.info("Mollie is still confirming the mandate. Reload this return URL to continue setup.");
    setupLoadingProvider = null;
  }

  async function startPostpaidSetup(provider: "stripe" | "mollie") {
    if (!selectedTierId) {
      toast.error("Please select a billing tier");
      return;
    }
    const tier = selectedPostpaidTier();
    if (!tier) {
      toast.error("Select an eligible paid tier");
      return;
    }
    if (!billingDetails?.isComplete) {
      toast.error("Complete your billing email and address before starting postpaid setup");
      return;
    }
    if (!availableSetupProviders.has(provider)) {
      toast.error(`${provider === "stripe" ? "Stripe" : "Mollie"} is not configured`);
      return;
    }

    setupLoadingProvider = provider;
    try {
      if (provider === "stripe") {
        const billingURL = `${window.location.origin}${resolve("/account/billing")}`;
        const response = await stripeCheckoutMutation.mutate({
          tierId: tier.id,
          billingPeriod: tier.billingPeriod || "monthly",
          successUrl: `${billingURL}?setup=stripe&tier=${encodeURIComponent(tier.id)}`,
          cancelUrl: `${billingURL}?setup=cancelled`,
        });
        const result = response.data?.createStripeCheckout;
        if (result?.__typename === "StripeCheckoutSession") {
          window.location.href = result.checkoutUrl;
          return;
        }
        throw new Error(setupErrorMessage(result, "Failed to start Stripe setup"));
      }

      const returnURL = `${window.location.origin}${resolve("/account/billing")}?setup=mollie&tier=${encodeURIComponent(tier.id)}`;
      const response = await mollieFirstPaymentMutation.mutate({
        tierId: tier.id,
        method: mollieFirstPaymentMethod,
        redirectUrl: returnURL,
      });
      const result = response.data?.createMollieFirstPayment;
      if (result?.__typename === "MollieFirstPayment") {
        window.location.href = result.paymentUrl;
        return;
      }
      throw new Error(setupErrorMessage(result, "Failed to start Mollie setup"));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to start postpaid setup");
    } finally {
      setupLoadingProvider = null;
    }
  }

  async function handleChangeBillingTier(tier: {
    id?: string | null;
    displayName?: string | null;
    tierLevel?: number | null;
  }) {
    if (!tier.id) {
      toast.error("Billing tier unavailable");
      return;
    }
    if (!currentTier?.tierLevel || tier.tierLevel == null) {
      toast.error("Current billing tier unavailable");
      return;
    }
    const isDowngrade = tier.tierLevel < currentTier.tierLevel;
    if (isDowngrade) {
      const ok = window.confirm(
        `Downgrade to ${tier.displayName ?? "this tier"} at the end of the current billing period?`
      );
      if (!ok) return;
    }

    changeTierLoadingId = tier.id;
    try {
      const result = await changeTierMutation.mutate({ tierId: tier.id });
      const data = result.data?.changeBillingTier;
      if (data?.__typename === "ChangeBillingTierPayload") {
        if (data.pendingTier) {
          const effective = data.effectiveAt ? formatDate(data.effectiveAt) : "period end";
          toast.success(`Downgrade scheduled for ${effective}`);
        } else {
          toast.success(
            data.message || `Upgraded to ${data.appliedTier?.displayName ?? "new tier"}`
          );
        }
        await loadBillingData();
        return;
      }
      if (data && "message" in data) {
        throw new Error(data.message);
      }
      throw new Error("Failed to change billing tier");
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to change billing tier");
    } finally {
      changeTierLoadingId = null;
    }
  }

  function moneyNumber(value: unknown): number {
    if (typeof value === "number") return Number.isFinite(value) ? value : 0;
    if (typeof value === "string") {
      const parsed = parseFloat(value);
      return Number.isFinite(parsed) ? parsed : 0;
    }
    return 0;
  }

  function formatCurrency(amount: unknown, currency = "EUR") {
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency: currency,
    }).format(moneyNumber(amount));
  }

  function formatUnitPrice(amount: unknown, currency = "EUR") {
    const value = moneyNumber(amount);
    const precision = Math.abs(value) > 0 && Math.abs(value) < 0.01 ? 6 : 2;
    return new Intl.NumberFormat("en-US", {
      style: "currency",
      currency,
      minimumFractionDigits: precision,
      maximumFractionDigits: 9,
    }).format(value);
  }

  function formatDate(dateString: string | null | undefined) {
    if (!dateString) return "N/A";
    return new Date(dateString).toLocaleDateString();
  }

  function paymentMethodAvailable(method: PaymentMethod$options) {
    return availablePaymentMethods.has(String(method));
  }

  function paymentMethodFromRecord(method: string): PaymentMethod$options | null {
    switch (method.toLowerCase()) {
      case "card":
        return PaymentMethod.CARD;
      case "crypto_usdc":
        return PaymentMethod.CRYPTO_USDC;
      case "crypto_eth":
        return PaymentMethod.CRYPTO_ETH;
      default:
        return null;
    }
  }

  function retryPayment(invoiceId: string, rawMethod: string) {
    const method = paymentMethodFromRecord(rawMethod);
    if (!method || !paymentMethodAvailable(method)) {
      toast.error("That payment method is not currently available");
      return;
    }
    createPayment(invoiceId, method);
  }

  function invoiceIsPayable(invoiceId: string) {
    return invoices.some(
      (invoice) =>
        invoice.id === invoiceId && (invoice.status === "PENDING" || invoice.status === "OVERDUE")
    );
  }

  function getStatusColor(status: string | null | undefined) {
    switch (status?.toLowerCase()) {
      case "active":
      case "confirmed":
      case "paid":
        return "text-success";
      case "past_due":
      case "pending":
      case "processing":
        return "text-warning";
      case "cancelled":
      case "failed":
        return "text-error";
      default:
        return "text-muted-foreground";
    }
  }

  // Track which invoice is expanded
  let expandedInvoiceId = $state<string | null>(null);

  function toggleInvoiceExpand(invoiceId: string) {
    expandedInvoiceId = expandedInvoiceId === invoiceId ? null : invoiceId;
  }

  type InvoiceLineItemLike = {
    lineKey: string;
    description: string;
    quantity: string;
    unit: string;
    dimensions: unknown;
    unitPrice: string;
    total: string;
    currency: string;
    clusterId?: string | null;
    clusterName?: string | null;
    clusterKind?: string | null;
    pricingSource?: string | null;
    pricingLabel?: string | null;
  };

  function lineItemDimensions(value: unknown): string[] {
    let parsed = value;
    if (typeof parsed === "string") {
      try {
        parsed = JSON.parse(parsed);
      } catch {
        return [];
      }
    }
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) return [];
    return Object.entries(parsed as Record<string, unknown>)
      .filter(([, dimensionValue]) => typeof dimensionValue === "string" && dimensionValue !== "")
      .sort(([left], [right]) => left.localeCompare(right))
      .map(([key, dimensionValue]) => `${key.replaceAll("_", " ")}: ${dimensionValue}`);
  }

  type LineItemGroup = {
    key: string;
    label: string;
    kind: string | null;
    items: InvoiceLineItemLike[];
  };

  // groupLineItems splits invoice line items into render groups: one per
  // cluster (platform-official → tenant-private → marketplace order),
  // plus a "Subscription" bucket for tenant-scoped lines like
  // base_subscription. Mirrors the email template's shape so the
  // dashboard and email match.
  function groupLineItems(items: readonly InvoiceLineItemLike[]): LineItemGroup[] {
    const platformScoped: InvoiceLineItemLike[] = [];
    const byCluster = new SvelteMap<string, LineItemGroup>();
    const order: string[] = [];
    for (const item of items) {
      if (!item.clusterId) {
        platformScoped.push(item);
        continue;
      }
      const existing = byCluster.get(item.clusterId);
      if (existing) {
        existing.items.push(item);
        continue;
      }
      const grp: LineItemGroup = {
        key: `cluster-${item.clusterId}`,
        label: item.clusterName || item.clusterId,
        kind: item.clusterKind ?? null,
        items: [item],
      };
      byCluster.set(item.clusterId, grp);
      order.push(item.clusterId);
    }
    const out: LineItemGroup[] = [];
    for (const kind of ["platform_official", "tenant_private", "third_party_marketplace"]) {
      for (const cid of order) {
        const grp = byCluster.get(cid)!;
        if (grp.kind === kind) out.push(grp);
      }
    }
    for (const cid of order) {
      const grp = byCluster.get(cid)!;
      if (
        grp.kind !== "platform_official" &&
        grp.kind !== "tenant_private" &&
        grp.kind !== "third_party_marketplace"
      ) {
        out.push(grp);
      }
    }
    if (platformScoped.length > 0) {
      out.push({
        key: "subscription",
        label: "Subscription",
        kind: null,
        items: platformScoped,
      });
    }
    return out;
  }

  // Trial days remaining
  const trialDaysRemaining = $derived.by(() => {
    if (!billingStatus?.trialEndsAt) return null;
    const trialEnd = new Date(billingStatus.trialEndsAt);
    const now = new Date();
    const diffMs = trialEnd.getTime() - now.getTime();
    if (diffMs <= 0) return 0;
    return Math.ceil(diffMs / (1000 * 60 * 60 * 24));
  });

  // Icons
  const CreditCardIcon = getIconComponent("CreditCard");
  const CalendarIcon = getIconComponent("Calendar");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const ShieldIcon = getIconComponent("Shield");
  const ReceiptIcon = getIconComponent("Receipt");
  const SparklesIcon = getIconComponent("Sparkles");
  const GaugeIcon = getIconComponent("Gauge");
  const ArrowUpIcon = getIconComponent("ArrowUp");
  const CoinsIcon = getIconComponent("Coins");
  const CopyIcon = getIconComponent("Copy");

  // Derived: is this a prepaid account that can upgrade?
  const isPrepaid = $derived(billingStatus?.subscription?.billingModel === "prepaid");
  const pendingTier = $derived(billingStatus?.subscription?.pendingTier ?? null);
  const pendingEffectiveAt = $derived(billingStatus?.subscription?.pendingEffectiveAt ?? null);
</script>

<svelte:head>
  <title>Billing - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center gap-3">
      <CreditCardIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Billing</h1>
        <p class="text-sm text-muted-foreground">
          Manage your subscription, usage, and payment information
        </p>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="px-4 sm:px-6 lg:px-8 py-6">
        <div class="flex items-center justify-center min-h-64">
          <div class="loading-spinner w-8 h-8"></div>
        </div>
      </div>
    {:else if error}
      <div class="px-4 sm:px-6 lg:px-8 py-6">
        <div class="bg-destructive/20 border border-destructive p-4">
          <p class="text-destructive">{error}</p>
        </div>
      </div>
    {:else}
      <div class="page-transition">
        <!-- Trial Countdown Banner (full-bleed) -->
        {#if trialDaysRemaining !== null && trialDaysRemaining > 0}
          <div
            class="bg-accent-purple/20 border-y border-accent-purple/50 px-4 sm:px-6 lg:px-8 py-4 mb-0"
          >
            <div class="flex items-center justify-between">
              <div>
                <p class="text-accent-purple font-semibold">Trial Period Active</p>
                <p class="text-sm text-muted-foreground">
                  Your trial ends on {formatDate(billingStatus?.trialEndsAt)} ({trialDaysRemaining} days
                  remaining)
                </p>
              </div>
              <div class="text-2xl font-bold text-accent-purple">
                {trialDaysRemaining} days
              </div>
            </div>
          </div>
        {:else if trialDaysRemaining === 0}
          <div class="bg-warning/20 border-y border-warning/50 px-4 sm:px-6 lg:px-8 py-4 mb-0">
            <p class="text-warning font-semibold">Trial Expired</p>
            <p class="text-sm text-muted-foreground">
              Your trial has ended. Please upgrade to continue using premium features.
            </p>
          </div>
        {/if}

        <!-- Current Subscription Status - GridSeam metrics -->
        {#if billingStatus}
          <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
            <div>
              <DashboardMetricCard
                icon={CreditCardIcon}
                iconColor="text-primary"
                value={currentTier?.displayName || "Free"}
                valueColor="text-foreground"
                label="Current Plan"
              />
            </div>
            <div>
              <DashboardMetricCard
                icon={CheckCircleIcon}
                iconColor={billingStatus.billingStatus === "active"
                  ? "text-success"
                  : "text-warning"}
                value={billingStatus.billingStatus || "Active"}
                valueColor={getStatusColor(billingStatus.billingStatus)}
                label="Status"
              />
            </div>
            <div>
              <DashboardMetricCard
                icon={CalendarIcon}
                iconColor="text-info"
                value={formatDate(billingStatus.nextBillingDate)}
                valueColor="text-foreground"
                label="Next Billing"
              />
            </div>
            {#if currentTier?.slaLevel}
              <div>
                <DashboardMetricCard
                  icon={ShieldIcon}
                  iconColor="text-success"
                  value={currentTier.slaLevel}
                  valueColor="text-success"
                  label="SLA Level"
                />
              </div>
            {:else}
              <div>
                <DashboardMetricCard
                  icon={CreditCardIcon}
                  iconColor="text-muted-foreground"
                  value={formatCurrency(
                    currentTier?.basePrice || 0,
                    currentTier?.currency || "EUR"
                  )}
                  valueColor="text-foreground"
                  label="Monthly Cost"
                />
              </div>
            {/if}
          </GridSeam>

          <!-- Outstanding Balance Alert -->
          {#if billingStatus.outstandingAmount > 0}
            <div class="bg-warning/20 border-y border-warning px-4 sm:px-6 lg:px-8 py-4">
              <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                <p class="text-warning font-semibold">
                  Outstanding Balance: {formatCurrency(
                    billingStatus.outstandingAmount,
                    billingStatus.currency
                  )}
                </p>
                <p class="text-sm text-muted-foreground">
                  {availablePaymentMethods.size > 0
                    ? "Choose the invoice and payment method below."
                    : "No payment methods are currently configured."}
                </p>
              </div>
            </div>
          {/if}
          {#if invoiceCryptoPayment}
            <div class="border-y border-success/60 bg-success/10 px-4 sm:px-6 lg:px-8 py-4">
              <div class="flex flex-col gap-3">
                <div class="flex items-center gap-2">
                  <CoinsIcon class="w-4 h-4 text-success" />
                  <p class="font-semibold">
                    Send {invoiceCryptoPayment.expectedAmountToken}
                    {invoiceCryptoPayment.asset}
                    on {invoiceCryptoPayment.network}
                  </p>
                </div>
                <p class="text-xs text-muted-foreground">
                  Locked at ${invoiceCryptoPayment.quotedPriceUsd}/{invoiceCryptoPayment.asset}
                  ({invoiceCryptoPayment.quoteSource}). Quote valid until {formatDate(
                    invoiceCryptoPayment.expiresAt
                  )}.
                </p>
                <div
                  class="flex items-center gap-2 p-3 bg-muted/30 rounded-md font-mono text-sm break-all"
                >
                  <span class="flex-1">{invoiceCryptoPayment.walletAddress}</span>
                  <Button variant="ghost" size="sm" onclick={copyInvoiceCryptoAddress}>
                    <CopyIcon class="w-4 h-4" />
                  </Button>
                </div>
              </div>
            </div>
          {/if}
        {/if}

        <!-- Main Content Grid -->
        <div class="dashboard-grid">
          <!-- Available Plans Slab -->
          {#if availableTiers.length > 0}
            <div class="slab col-span-full">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <SparklesIcon class="w-4 h-4 text-accent-purple" />
                  <h3>Available Plans</h3>
                </div>
              </div>
              <div class="slab-body--padded">
                <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
                  {#each availableTiers as tier (tier.id ?? tier.tierName ?? tier.displayName)}
                    <div
                      class="p-4 border border-border/50 bg-muted/10 {currentTier?.id === tier.id
                        ? 'ring-2 ring-primary'
                        : ''}"
                    >
                      <h4 class="text-lg font-semibold mb-2">{tier.displayName}</h4>
                      <div class="text-2xl font-bold text-primary mb-2">
                        {#if tier.isEnterprise}
                          <span class="text-lg">Contact us</span>
                        {:else}
                          {formatCurrency(tier.basePrice, tier.currency)}
                          <span class="text-sm text-muted-foreground font-normal"
                            >/{tier.billingPeriod || "month"}</span
                          >
                        {/if}
                      </div>

                      {#if tier.description}
                        <p class="text-sm text-muted-foreground mb-4">{tier.description}</p>
                      {/if}

                      {#if tier.features}
                        <ul class="space-y-1 mb-4 text-sm">
                          {#if tier.features.recording}
                            <li class="flex items-center">
                              <span class="text-success mr-2">✓</span> DVR Recording
                            </li>
                          {/if}
                          {#if tier.features.analytics}
                            <li class="flex items-center">
                              <span class="text-success mr-2">✓</span> Analytics
                            </li>
                          {/if}
                          {#if tier.features.apiAccess}
                            <li class="flex items-center">
                              <span class="text-success mr-2">✓</span> API Access
                            </li>
                          {/if}
                          {#if tier.features.customBranding}
                            <li class="flex items-center">
                              <span class="text-success mr-2">✓</span> Custom Branding
                            </li>
                          {/if}
                          {#if tier.features.sla}
                            <li class="flex items-center">
                              <span class="text-success mr-2">✓</span> SLA Guarantee
                            </li>
                          {/if}
                          {#if tier.supportLevel}
                            <li class="flex items-center">
                              <span class="text-info mr-2">●</span>
                              {tier.supportLevel} Support
                            </li>
                          {/if}
                        </ul>
                      {/if}

                      {#if currentTier?.id === tier.id}
                        <div
                          class="w-full text-center py-2 text-sm text-muted-foreground border-t border-border/30 mt-4"
                        >
                          Current Plan
                        </div>
                        {#if pendingTier}
                          <div
                            class="mt-3 border border-warning/50 bg-warning/10 px-3 py-2 text-xs text-warning"
                          >
                            Downgrading to {pendingTier.displayName} on {pendingEffectiveAt
                              ? formatDate(pendingEffectiveAt)
                              : "period end"}
                          </div>
                        {/if}
                      {:else if !isPrepaid && (tier.tierLevel ?? 0) >= 1}
                        <Button
                          class="w-full mt-4"
                          variant={(tier.tierLevel ?? 0) > (currentTier?.tierLevel ?? 0)
                            ? "default"
                            : "outline"}
                          disabled={changeTierLoadingId === tier.id}
                          onclick={() => handleChangeBillingTier(tier)}
                        >
                          {#if changeTierLoadingId === tier.id}
                            Updating...
                          {:else if (tier.tierLevel ?? 0) > (currentTier?.tierLevel ?? 0)}
                            Upgrade
                          {:else}
                            Downgrade
                          {/if}
                        </Button>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            </div>
          {/if}

          <!-- Prepaid Balance -->
          <PrepaidBalanceWidget />

          <!-- Upgrade to Postpaid (for prepaid accounts with verified email) -->
          {#if isPrepaid && $auth.user?.email}
            <div class="slab">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <ArrowUpIcon class="w-4 h-4 text-success" />
                  <h3>Upgrade to Postpaid</h3>
                </div>
              </div>
              <div class="slab-body--padded">
                <p class="text-sm text-muted-foreground mb-4">
                  You're currently on prepaid billing. Choose a provider and complete its hosted
                  setup before monthly postpaid billing is activated.
                </p>
                {#if !billingDetails?.isComplete}
                  <div class="mb-4 border border-warning/50 bg-warning/10 p-3 text-sm">
                    <p class="font-medium text-warning">Billing details required</p>
                    <p class="mt-1 text-muted-foreground">
                      Add a billing email and complete address before starting provider setup. A VAT
                      or tax identifier is optional and is not treated as verified automatically.
                    </p>
                    <Button href={resolve("/settings")} variant="outline" size="sm" class="mt-3">
                      Complete billing details
                    </Button>
                  </div>
                {/if}
                {#if availableTiers.length > 0}
                  <div class="space-y-2">
                    <label for="tierSelect" class="text-sm font-medium text-muted-foreground"
                      >Select a tier:</label
                    >
                    <select
                      id="tierSelect"
                      bind:value={selectedTierId}
                      class="w-full p-2 border border-input rounded-md bg-background text-foreground"
                    >
                      <option value="">Choose a tier...</option>
                      {#each availableTiers.filter((tier) => tier.tierLevel >= 1 && tier.basePrice > 0 && !tier.isEnterprise) as tier (tier.id)}
                        <option value={tier.id}>
                          {tier.displayName} - {formatCurrency(tier.basePrice, tier.currency)}/month
                        </option>
                      {/each}
                    </select>
                  </div>
                {:else}
                  <p class="text-sm text-muted-foreground">Loading billing tiers...</p>
                {/if}
                {#if availableSetupProviders.has("mollie")}
                  <div class="mt-4 space-y-2">
                    <label for="mollieMethod" class="text-sm font-medium text-muted-foreground"
                      >Mollie first-payment method:</label
                    >
                    <select
                      id="mollieMethod"
                      bind:value={mollieFirstPaymentMethod}
                      class="w-full p-2 border border-input rounded-md bg-background text-foreground"
                    >
                      <option value="creditcard">Credit card</option>
                      <option value="ideal">iDEAL</option>
                      <option value="bancontact">Bancontact</option>
                    </select>
                  </div>
                {/if}
              </div>
              <div class="slab-actions flex flex-wrap gap-2">
                {#if availableSetupProviders.has("stripe")}
                  <Button
                    onclick={() => startPostpaidSetup("stripe")}
                    disabled={setupLoadingProvider !== null ||
                      !selectedTierId ||
                      !billingDetails?.isComplete}
                  >
                    {setupLoadingProvider === "stripe" ? "Opening Stripe..." : "Set up with Stripe"}
                  </Button>
                {/if}
                {#if availableSetupProviders.has("mollie")}
                  <Button
                    variant="outline"
                    onclick={() => startPostpaidSetup("mollie")}
                    disabled={setupLoadingProvider !== null ||
                      !selectedTierId ||
                      !billingDetails?.isComplete}
                  >
                    {setupLoadingProvider === "mollie" ? "Opening Mollie..." : "Set up with Mollie"}
                  </Button>
                {/if}
                {#if availableSetupProviders.size === 0}
                  <p class="text-sm text-muted-foreground">
                    No postpaid provider is fully configured. Continue using prepaid billing or
                    contact support.
                  </p>
                {/if}
              </div>
            </div>
          {/if}

          <!-- Usage Link Slab -->
          <div class="slab">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <GaugeIcon class="w-4 h-4 text-primary" />
                <h3>Usage & Costs</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <p class="text-sm text-muted-foreground mb-4">
                Track your streaming hours, bandwidth consumption, and see what it's costing you.
              </p>
              <Button href={resolve("/analytics/usage")} variant="default" class="w-full gap-2">
                <GaugeIcon class="w-4 h-4" />
                View Usage & Costs
              </Button>
            </div>
          </div>

          {#if billingDocuments.length > 0}
            <div class="slab col-span-full">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <ReceiptIcon class="w-4 h-4 text-info" />
                  <h3>Billing documents</h3>
                </div>
              </div>
              <div class="slab-body--flush overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr class="border-b border-border/50 text-muted-foreground">
                      <th class="text-left py-3 px-4">Document</th>
                      <th class="text-left py-3 px-4">Number</th>
                      <th class="text-left py-3 px-4">Amount</th>
                      <th class="text-left py-3 px-4">Status</th>
                      <th class="text-right py-3 px-4">Download</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each billingDocuments as document (`${document.kind}:${document.id}`)}
                      <tr class="border-b border-border/30">
                        <td class="py-3 px-4">{billingDocumentLabel(document.kind)}</td>
                        <td class="py-3 px-4 font-mono">{document.document_number}</td>
                        <td class="py-3 px-4">
                          {formatCurrency(document.amount_cents / 100, document.currency)}
                        </td>
                        <td class="py-3 px-4">
                          <span class="px-2 py-1 text-xs {getStatusColor(document.status)}">
                            {document.status}
                          </span>
                        </td>
                        <td class="py-3 px-4 text-right">
                          <Button size="sm" variant="outline" href={billingDocumentURL(document)}>
                            Download
                          </Button>
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          {/if}

          <!-- Recent Invoices Slab -->
          {#if invoices.length > 0}
            <div class="slab col-span-full">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <ReceiptIcon class="w-4 h-4 text-info" />
                  <h3>Invoices</h3>
                </div>
              </div>
              <div class="slab-body--flush overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr class="border-b border-border/50 text-muted-foreground">
                      <th class="text-left py-3 px-4 w-8"></th>
                      <th class="text-left py-3 px-4">Invoice ID</th>
                      <th class="text-left py-3 px-4">Amount</th>
                      <th class="text-left py-3 px-4">Status</th>
                      <th class="text-left py-3 px-4">Due Date</th>
                      <th class="text-left py-3 px-4">Created</th>
                      <th class="text-right py-3 px-4">Payment</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each invoices as invoice (invoice.id)}
                      <tr
                        id={`invoice-${invoice.id}`}
                        class="border-b border-border/30 cursor-pointer hover:bg-muted/20 transition-colors"
                        onclick={() => toggleInvoiceExpand(invoice.id)}
                      >
                        <td class="py-3 px-4 text-center">
                          <span
                            class="text-muted-foreground text-xs transition-transform inline-block {expandedInvoiceId ===
                            invoice.id
                              ? 'rotate-90'
                              : ''}">▶</span
                          >
                        </td>
                        <td class="py-3 px-4 font-mono">{invoice.id}</td>
                        <td class="py-3 px-4">{formatCurrency(invoice.amount, invoice.currency)}</td
                        >
                        <td class="py-3 px-4">
                          <span class="px-2 py-1 text-xs {getStatusColor(invoice.status)}"
                            >{invoice.status}</span
                          >
                        </td>
                        <td class="py-3 px-4">{formatDate(invoice.dueDate)}</td>
                        <td class="py-3 px-4">{formatDate(invoice.createdAt)}</td>
                        <td class="py-3 px-4">
                          {#if invoice.status === "PENDING" || invoice.status === "OVERDUE"}
                            <div class="flex justify-end gap-2">
                              {#if paymentMethodAvailable(PaymentMethod.CARD)}
                                <Button
                                  size="sm"
                                  disabled={paymentLoadingInvoiceId !== null}
                                  onclick={(event) => {
                                    event.stopPropagation();
                                    createPayment(invoice.id, PaymentMethod.CARD);
                                  }}
                                >
                                  {paymentLoadingInvoiceId === invoice.id
                                    ? "Opening..."
                                    : "Pay by card"}
                                </Button>
                              {/if}
                              {#if paymentMethodAvailable(PaymentMethod.CRYPTO_USDC)}
                                <Button
                                  size="sm"
                                  variant="outline"
                                  disabled={paymentLoadingInvoiceId !== null}
                                  onclick={(event) => {
                                    event.stopPropagation();
                                    createPayment(invoice.id, PaymentMethod.CRYPTO_USDC);
                                  }}>USDC</Button
                                >
                              {/if}
                              {#if paymentMethodAvailable(PaymentMethod.CRYPTO_ETH)}
                                <Button
                                  size="sm"
                                  variant="outline"
                                  disabled={paymentLoadingInvoiceId !== null}
                                  onclick={(event) => {
                                    event.stopPropagation();
                                    createPayment(invoice.id, PaymentMethod.CRYPTO_ETH);
                                  }}>ETH</Button
                                >
                              {/if}
                            </div>
                          {/if}
                        </td>
                      </tr>
                      {#if expandedInvoiceId === invoice.id && invoice.lineItems?.length}
                        <tr class="bg-muted/10">
                          <td colspan="7" class="py-4 px-8">
                            <p
                              class="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-3"
                            >
                              Charges
                            </p>
                            {#each groupLineItems(invoice.lineItems) as group (`${invoice.id}-${group.key}`)}
                              <div class="mb-4 last:mb-0">
                                <div class="flex items-center gap-2 mb-2">
                                  <span class="text-sm font-semibold">{group.label}</span>
                                  {#if group.kind === "platform_official"}
                                    <span
                                      class="px-2 py-0.5 text-[10px] font-medium rounded-full bg-blue-50 text-blue-700"
                                      >Platform</span
                                    >
                                  {:else if group.kind === "tenant_private"}
                                    <span
                                      class="px-2 py-0.5 text-[10px] font-medium rounded-full bg-emerald-50 text-emerald-700"
                                      >Self-hosted</span
                                    >
                                  {:else if group.kind === "third_party_marketplace"}
                                    <span
                                      class="px-2 py-0.5 text-[10px] font-medium rounded-full bg-purple-50 text-purple-700"
                                      >Marketplace</span
                                    >
                                  {/if}
                                </div>
                                <table class="w-full text-sm">
                                  <thead>
                                    <tr class="text-xs text-muted-foreground">
                                      <th class="text-left py-1">Item</th>
                                      <th class="text-right py-1">Qty</th>
                                      <th class="text-right py-1">Unit Price</th>
                                      <th class="text-right py-1">Total</th>
                                    </tr>
                                  </thead>
                                  <tbody>
                                    {#each group.items as item (`${invoice.id}-${item.lineKey}`)}
                                      <tr
                                        class="border-t border-border/20"
                                        class:text-muted-foreground={moneyNumber(item.total) === 0}
                                      >
                                        <td class="py-2">
                                          <div>{item.description}</div>
                                          {#if lineItemDimensions(item.dimensions).length}
                                            <div class="text-[10px] text-muted-foreground">
                                              {lineItemDimensions(item.dimensions).join(" · ")}
                                            </div>
                                          {/if}
                                          {#if item.pricingLabel}
                                            <div class="text-[10px] text-muted-foreground">
                                              {item.pricingLabel}
                                            </div>
                                          {/if}
                                        </td>
                                        <td class="py-2 text-right font-mono">
                                          {item.quantity}{item.unit ? ` ${item.unit}` : ""}
                                        </td>
                                        <td class="py-2 text-right font-mono"
                                          >{formatUnitPrice(
                                            item.unitPrice,
                                            item.currency || invoice.currency
                                          )}</td
                                        >
                                        <td class="py-2 text-right font-mono font-semibold">
                                          {#if moneyNumber(item.total) === 0 && item.pricingSource !== "beta_free"}
                                            <span
                                              class="px-2 py-0.5 text-[10px] font-medium rounded-full bg-emerald-50 text-emerald-700"
                                              >Included</span
                                            >
                                          {:else}
                                            {formatCurrency(
                                              item.total,
                                              item.currency || invoice.currency
                                            )}
                                          {/if}
                                        </td>
                                      </tr>
                                    {/each}
                                  </tbody>
                                </table>
                              </div>
                            {/each}
                            {#if moneyNumber(invoice.prepaidCreditApplied) > 0}
                              <table class="w-full text-sm border-t border-border/40 mt-2 pt-2">
                                <tbody>
                                  <tr>
                                    <td class="py-2">Prepaid credit applied</td>
                                    <td class="py-2 text-right font-mono"
                                      >-{formatCurrency(
                                        invoice.prepaidCreditApplied,
                                        invoice.currency
                                      )}</td
                                    >
                                  </tr>
                                </tbody>
                              </table>
                            {/if}
                            {#if moneyNumber(invoice.grossMeteredAmount) > 0 && moneyNumber(invoice.meteredAmount) === 0}
                              <div
                                class="mt-2 pt-2 border-t border-border/40 text-xs text-emerald-700"
                              >
                                Metered usage would have cost {formatCurrency(
                                  invoice.grossMeteredAmount,
                                  invoice.currency
                                )} — usage is on us during beta. Metered total: {formatCurrency(
                                  0,
                                  invoice.currency
                                )}.
                              </div>
                            {/if}
                          </td>
                        </tr>
                      {/if}
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          {:else}
            <div class="slab col-span-full">
              <div class="slab-body--padded">
                <EmptyState
                  iconName="Receipt"
                  title="No invoices yet"
                  description="Your invoices and payment history will appear here once you have billing activity."
                />
              </div>
            </div>
          {/if}

          {#if recentPayments.length > 0}
            <div class="slab col-span-full">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <CreditCardIcon class="w-4 h-4 text-info" />
                  <h3>Payment History</h3>
                </div>
              </div>
              <div class="slab-body--flush overflow-x-auto">
                <table class="w-full text-sm">
                  <thead>
                    <tr class="border-b border-border/50 text-muted-foreground">
                      <th class="text-left py-3 px-4">Created</th>
                      <th class="text-left py-3 px-4">Invoice</th>
                      <th class="text-left py-3 px-4">Method</th>
                      <th class="text-right py-3 px-4">Amount</th>
                      <th class="text-left py-3 px-4">Status</th>
                      <th class="text-right py-3 px-4">Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {#each recentPayments as payment (payment.id)}
                      <tr class="border-b border-border/30">
                        <td class="py-3 px-4">{formatDate(payment.createdAt)}</td>
                        <td class="py-3 px-4">
                          <button
                            type="button"
                            class="font-mono text-left hover:text-primary"
                            onclick={() => {
                              expandedInvoiceId = payment.invoiceId;
                              document
                                .getElementById(`invoice-${payment.invoiceId}`)
                                ?.scrollIntoView({ block: "center" });
                            }}
                          >
                            {payment.invoiceId}
                          </button>
                        </td>
                        <td class="py-3 px-4 uppercase">{payment.method.replaceAll("_", " ")}</td>
                        <td class="py-3 px-4 text-right font-mono">
                          {formatCurrency(payment.amount, payment.currency)}
                        </td>
                        <td class="py-3 px-4">
                          <span class="px-2 py-1 text-xs {getStatusColor(payment.status)}">
                            {payment.status}
                          </span>
                        </td>
                        <td class="py-3 px-4 text-right">
                          {#if payment.status.toLowerCase() === "failed" && invoiceIsPayable(payment.invoiceId)}
                            <Button
                              size="sm"
                              variant="outline"
                              disabled={paymentLoadingInvoiceId !== null}
                              onclick={() => retryPayment(payment.invoiceId, payment.method)}
                            >
                              Retry
                            </Button>
                          {:else if payment.status.toLowerCase() === "pending"}
                            <span class="text-xs text-muted-foreground">Processing</span>
                          {:else}
                            <span class="text-xs text-muted-foreground">—</span>
                          {/if}
                        </td>
                      </tr>
                    {/each}
                  </tbody>
                </table>
              </div>
            </div>
          {/if}
        </div>
      </div>
    {/if}
  </div>
</div>
