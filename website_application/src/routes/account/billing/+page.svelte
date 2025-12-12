<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { GetBillingStatusStore, GetBillingTiersStore, GetInvoicesStore, CreatePaymentStore } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { PaymentMethod, type PaymentMethod$options } from "$houdini";

  // Houdini stores
  const billingStatusStore = new GetBillingStatusStore();
  const billingTiersStore = new GetBillingTiersStore();
  const invoicesStore = new GetInvoicesStore();
  const createPaymentMutation = new CreatePaymentStore();

  let isAuthenticated = false;
  let error = $state<string | null>(null);

  // Derived state from Houdini stores
  let loading = $derived(
    $billingStatusStore.fetching ||
    $billingTiersStore.fetching ||
    $invoicesStore.fetching
  );
  let billingStatus = $derived($billingStatusStore.data?.billingStatus ?? null);
  let availableTiers = $derived($billingTiersStore.data?.billingTiers ?? []);
  let invoices = $derived($invoicesStore.data?.invoices ?? []);

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadBillingData();
  });

  async function loadBillingData() {
    try {
      // Load all billing data in parallel
      await Promise.all([
        billingStatusStore.fetch().catch(() => null),
        billingTiersStore.fetch().catch(() => null),
        invoicesStore.fetch().catch(() => null)
      ]);

    } catch (err) {
      console.error('Failed to load billing data:', err);
      error = 'Failed to load billing information. Please try again later.';
      toast.error('Failed to load billing information. Please refresh the page.');
    }
  }

  async function createPayment(amount: number, invoiceId?: string, method: PaymentMethod$options = PaymentMethod.CARD) {
    try {
      // Find the most recent unpaid invoice if not provided
      const targetInvoiceId = invoiceId || invoices.find(inv => inv.status === 'PENDING')?.id;
      if (!targetInvoiceId) {
        toast.error('No pending invoice found');
        return;
      }
      await createPaymentMutation.mutate({
        input: {
          amount,
          currency: 'USD',
          method,
          invoiceId: targetInvoiceId
        }
      });
      await loadBillingData(); // Refresh data
      toast.success('Payment processed successfully!');
    } catch (err) {
      console.error('Failed to create payment:', err);
      toast.error('Failed to process payment. Please try again.');
    }
  }

  function formatCurrency(amount: number, currency = 'USD') {
    return new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency: currency
    }).format(amount);
  }

  function formatDate(dateString: string | null | undefined) {
    if (!dateString) return 'N/A';
    return new Date(dateString).toLocaleDateString();
  }

  function getStatusColor(status: string | null | undefined) {
    switch (status?.toLowerCase()) {
      case 'active': return 'text-success';
      case 'past_due': return 'text-warning';
      case 'cancelled': return 'text-error';
      default: return 'text-muted-foreground';
    }
  }

  function formatAllocation(allocation: { limit?: number | null; unit?: string | null; unitPrice?: number | null } | null | undefined) {
    if (!allocation?.limit) return null;
    const limit = allocation.limit >= 1000
      ? `${(allocation.limit / 1000).toFixed(0)}k`
      : allocation.limit.toString();
    return `${limit} ${allocation.unit || ''}`;
  }

  function formatOverageRate(rate: { unitPrice?: number | null; unit?: string | null } | null | undefined) {
    if (!rate?.unitPrice) return null;
    return `${formatCurrency(rate.unitPrice)}/${rate.unit || 'unit'}`;
  }

  // Track which invoice is expanded
  let expandedInvoiceId = $state<string | null>(null);

  function toggleInvoiceExpand(invoiceId: string) {
    expandedInvoiceId = expandedInvoiceId === invoiceId ? null : invoiceId;
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
  const CreditCardIcon = getIconComponent('CreditCard');
  const CalendarIcon = getIconComponent('Calendar');
  const CheckCircleIcon = getIconComponent('CheckCircle');
  const ShieldIcon = getIconComponent('Shield');
  const ReceiptIcon = getIconComponent('Receipt');
  const SparklesIcon = getIconComponent('Sparkles');
  const GaugeIcon = getIconComponent('Gauge');
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
        <div class="bg-accent-purple/20 border-y border-accent-purple/50 px-4 sm:px-6 lg:px-8 py-4 mb-0">
          <div class="flex items-center justify-between">
            <div>
              <p class="text-accent-purple font-semibold">Trial Period Active</p>
              <p class="text-sm text-muted-foreground">
                Your trial ends on {formatDate(billingStatus?.trialEndsAt)} ({trialDaysRemaining} days remaining)
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
              value={billingStatus.currentTier?.name || 'Free'}
              valueColor="text-foreground"
              label="Current Plan"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={CheckCircleIcon}
              iconColor={billingStatus.billingStatus === 'active' ? 'text-success' : 'text-warning'}
              value={billingStatus.billingStatus || 'Active'}
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
          {#if billingStatus.currentTier?.slaLevel}
            <div>
              <DashboardMetricCard
                icon={ShieldIcon}
                iconColor="text-success"
                value={billingStatus.currentTier.slaLevel}
                valueColor="text-success"
                label="SLA Level"
              />
            </div>
          {:else}
            <div>
              <DashboardMetricCard
                icon={CreditCardIcon}
                iconColor="text-muted-foreground"
                value={formatCurrency(billingStatus.currentTier?.basePrice || 0)}
                valueColor="text-foreground"
                label="Monthly Cost"
              />
            </div>
          {/if}
        </GridSeam>

        <!-- Outstanding Balance Alert -->
        {#if billingStatus.outstandingAmount > 0}
          <div class="bg-warning/20 border-y border-warning px-4 sm:px-6 lg:px-8 py-4">
            <div class="flex items-center justify-between">
              <p class="text-warning font-semibold">
                Outstanding Balance: {formatCurrency(billingStatus.outstandingAmount)}
              </p>
              <Button
                variant="default"
                onclick={() => billingStatus && createPayment(billingStatus.outstandingAmount)}
                class="bg-warning text-background hover:bg-warning/90"
              >
                Pay Now
              </Button>
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
                {#each availableTiers as tier (tier.id ?? tier.name)}
                  <div class="p-4 border border-border/50 bg-muted/10 {billingStatus?.currentTier?.id === tier.id ? 'ring-2 ring-primary' : ''}">
                    <h4 class="text-lg font-semibold mb-2">{tier.name}</h4>
                    <div class="text-2xl font-bold text-primary mb-2">
                      {#if tier.isEnterprise}
                        <span class="text-lg">Contact us</span>
                      {:else}
                        {formatCurrency(tier.basePrice, tier.currency)}
                        <span class="text-sm text-muted-foreground font-normal">/{tier.billingPeriod || 'month'}</span>
                      {/if}
                    </div>

                    {#if tier.description}
                      <p class="text-sm text-muted-foreground mb-4">{tier.description}</p>
                    {/if}

                    {#if tier.features}
                      <ul class="space-y-1 mb-4 text-sm">
                        {#if tier.features.recording}
                          <li class="flex items-center"><span class="text-success mr-2">✓</span> DVR Recording</li>
                        {/if}
                        {#if tier.features.analytics}
                          <li class="flex items-center"><span class="text-success mr-2">✓</span> Analytics</li>
                        {/if}
                        {#if tier.features.apiAccess}
                          <li class="flex items-center"><span class="text-success mr-2">✓</span> API Access</li>
                        {/if}
                        {#if tier.features.customBranding}
                          <li class="flex items-center"><span class="text-success mr-2">✓</span> Custom Branding</li>
                        {/if}
                        {#if tier.features.sla}
                          <li class="flex items-center"><span class="text-success mr-2">✓</span> SLA Guarantee</li>
                        {/if}
                        {#if tier.supportLevel}
                          <li class="flex items-center"><span class="text-info mr-2">●</span> {tier.supportLevel} Support</li>
                        {/if}
                      </ul>
                    {/if}

                    {#if billingStatus?.currentTier?.id === tier.id}
                      <div class="w-full text-center py-2 text-sm text-muted-foreground border-t border-border/30 mt-4">
                        Current Plan
                      </div>
                    {/if}
                  </div>
                {/each}
              </div>
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

        <!-- Recent Invoices Slab -->
        {#if invoices.length > 0}
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <ReceiptIcon class="w-4 h-4 text-info" />
                <h3>Recent Invoices</h3>
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
                  </tr>
                </thead>
                <tbody>
                  {#each invoices.slice(0, 5) as invoice (invoice.id)}
                    <tr
                      class="border-b border-border/30 cursor-pointer hover:bg-muted/20 transition-colors"
                      onclick={() => toggleInvoiceExpand(invoice.id)}
                    >
                      <td class="py-3 px-4 text-center">
                        <span class="text-muted-foreground text-xs transition-transform inline-block {expandedInvoiceId === invoice.id ? 'rotate-90' : ''}">▶</span>
                      </td>
                      <td class="py-3 px-4 font-mono">{invoice.id}</td>
                      <td class="py-3 px-4">{formatCurrency(invoice.amount, invoice.currency)}</td>
                      <td class="py-3 px-4">
                        <span class="px-2 py-1 text-xs {getStatusColor(invoice.status)}">{invoice.status}</span>
                      </td>
                      <td class="py-3 px-4">{formatDate(invoice.dueDate)}</td>
                      <td class="py-3 px-4">{formatDate(invoice.createdAt)}</td>
                    </tr>
                    {#if expandedInvoiceId === invoice.id && invoice.lineItems?.length}
                      <tr class="bg-muted/10">
                        <td colspan="6" class="py-4 px-8">
                          <p class="text-xs font-semibold text-muted-foreground uppercase tracking-wide mb-3">Line Items</p>
                          <table class="w-full text-sm">
                            <thead>
                              <tr class="text-xs text-muted-foreground">
                                <th class="text-left py-1">Description</th>
                                <th class="text-right py-1">Qty</th>
                                <th class="text-right py-1">Unit Price</th>
                                <th class="text-right py-1">Total</th>
                              </tr>
                            </thead>
                            <tbody>
                              {#each invoice.lineItems as item, idx (`${invoice.id}-${idx}`)}
                                <tr class="border-t border-border/20">
                                  <td class="py-2">{item.description}</td>
                                  <td class="py-2 text-right font-mono">{item.quantity}</td>
                                  <td class="py-2 text-right font-mono">{formatCurrency(item.unitPrice, invoice.currency)}</td>
                                  <td class="py-2 text-right font-mono font-semibold">{formatCurrency(item.total, invoice.currency)}</td>
                                </tr>
                              {/each}
                            </tbody>
                          </table>
                        </td>
                      </tr>
                    {/if}
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
