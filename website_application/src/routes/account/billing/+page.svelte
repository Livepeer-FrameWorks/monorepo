<script>
  import { onMount } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { billingService } from "$lib/graphql/services/billing.js";
  import { toast } from "$lib/stores/toast.js";
  import SkeletonLoader from "$lib/components/SkeletonLoader.svelte";
  import { Button } from "$lib/components/ui/button";

  let isAuthenticated = false;
  let loading = $state(true);
  let billingStatus = $state(null);
  let availableTiers = $state([]);
  let usageRecords = $state([]);
  let invoices = $state([]);
  let error = $state(null);

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadBillingData();
    loading = false;
  });

  async function loadBillingData() {
    try {
      // Load all billing data in parallel
      const [statusData, tiersData, usageData, invoicesData] = await Promise.all([
        billingService.getBillingStatus().catch(() => null),
        billingService.getBillingTiers().catch(() => []),
        billingService.getUsageRecords().catch(() => []),
        billingService.getInvoices().catch(() => [])
      ]);
      
      billingStatus = statusData;
      availableTiers = tiersData || [];
      usageRecords = usageData || [];
      invoices = invoicesData || [];

    } catch (err) {
      console.error('Failed to load billing data:', err);
      error = 'Failed to load billing information. Please try again later.';
      toast.error('Failed to load billing information. Please refresh the page.');
    }
  }

  async function createPayment(amount, method = 'CARD') {
    try {
      await billingService.createPayment({
        amount,
        currency: 'USD',
        method
      });
      await loadBillingData(); // Refresh data
      toast.success('Payment processed successfully!');
    } catch (err) {
      console.error('Failed to create payment:', err);
      toast.error('Failed to process payment. Please try again.');
    }
  }

  function formatCurrency(amount, currency = 'USD') {
    return new Intl.NumberFormat('en-US', {
      style: 'currency',
      currency: currency
    }).format(amount);
  }

  function formatDate(dateString) {
    return new Date(dateString).toLocaleDateString();
  }

  function getStatusColor(status) {
    switch (status?.toLowerCase()) {
      case 'active': return 'text-green-500';
      case 'past_due': return 'text-yellow-500';
      case 'cancelled': return 'text-red-500';
      default: return 'text-gray-500';
    }
  }
</script>

<svelte:head>
  <title>Billing - FrameWorks</title>
</svelte:head>

<div class="min-h-screen bg-tokyo-night-bg text-tokyo-night-fg">
  <div class="max-w-4xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
    <div class="mb-8">
      <h1 class="text-3xl font-bold text-tokyo-night-blue mb-2">
        Billing & Subscription
      </h1>
      <p class="text-tokyo-night-comment">
        Manage your subscription, usage, and payment information
      </p>
    </div>

    {#if loading}
      <!-- Billing Status Skeleton -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <SkeletonLoader type="text-lg" className="w-48 mb-4" />
        <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
          {#each Array(3) as _, index (index)}
            <div>
              <SkeletonLoader type="text-sm" className="w-24 mb-1" />
              <SkeletonLoader type="text" className="w-20" />
            </div>
          {/each}
        </div>
      </div>

      <!-- Available Tiers Skeleton -->
      <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
        <SkeletonLoader type="text-lg" className="w-32 mb-4" />
        <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
          {#each Array(3) as _, index (index)}
            <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
              <SkeletonLoader type="text" className="w-16 mb-2" />
              <SkeletonLoader type="text-lg" className="w-12 mb-3" />
              <SkeletonLoader type="text-sm" className="w-full mb-2" />
              <SkeletonLoader type="text-sm" className="w-3/4 mb-4" />
              <SkeletonLoader type="custom" className="w-full h-10 rounded" />
            </div>
          {/each}
        </div>
      </div>
    {:else if error}
      <div class="bg-red-500 bg-opacity-20 border border-red-500 rounded-lg p-4 mb-8">
        <p class="text-red-300">{error}</p>
      </div>
    {:else}
      <!-- Current Subscription Status -->
      {#if billingStatus}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Current Subscription</h2>
          <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
            <div>
              <p class="text-sm text-tokyo-night-comment">Current Plan</p>
              <p class="text-lg font-semibold">{billingStatus.currentTier?.name || 'Free'}</p>
            </div>
            <div>
              <p class="text-sm text-tokyo-night-comment">Status</p>
              <p class="text-lg font-semibold {getStatusColor(billingStatus.status)}">{billingStatus.status || 'Active'}</p>
            </div>
            <div>
              <p class="text-sm text-tokyo-night-comment">Next Billing Date</p>
              <p class="text-lg font-semibold">{formatDate(billingStatus.nextBillingDate)}</p>
            </div>
          </div>
          
          {#if billingStatus.outstandingAmount > 0}
            <div class="mt-4 p-4 bg-yellow-500 bg-opacity-20 border border-yellow-500 rounded-lg">
              <p class="text-yellow-300">
                Outstanding Balance: {formatCurrency(billingStatus.outstandingAmount)}
              </p>
              <button
                onclick={() => createPayment(billingStatus.outstandingAmount)}
                class="mt-2 bg-yellow-600 text-white px-4 py-2 rounded hover:bg-yellow-700 transition-colors"
              >
                Pay Now
              </button>
            </div>
          {/if}
        </div>
      {/if}

      <!-- Available Tiers -->
      {#if availableTiers.length > 0}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Available Plans</h2>
          <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
            {#each availableTiers as tier (tier.id ?? tier.name)}
              <div class="bg-tokyo-night-bg rounded-lg p-4 border border-tokyo-night-selection">
                <h3 class="text-lg font-semibold mb-2">{tier.name}</h3>
                <div class="text-2xl font-bold text-tokyo-night-blue mb-2">
                  {formatCurrency(tier.price, tier.currency)}
                  <span class="text-sm text-tokyo-night-comment">/month</span>
                </div>
                
                {#if tier.description}
                  <p class="text-sm text-tokyo-night-comment mb-4">{tier.description}</p>
                {/if}
                
                {#if tier.features && tier.features.length > 0}
                  <ul class="space-y-1 mb-4">
                    {#each tier.features as feature, index (index)}
                      <li class="text-sm flex items-center">
                        <span class="text-green-500 mr-2">✓</span>
                        {feature}
                      </li>
                    {/each}
                  </ul>
                {/if}
                
                <Button
                  variant="outline"
                  class="w-full"
                  disabled
                  title="Tier changes will be available once billing launches"
                >
                  {billingStatus?.currentTier?.id === tier.id ? 'Current Plan' : 'Coming Soon'}
                </Button>
              </div>
            {/each}
          </div>
        </div>
      {/if}

      <!-- Recent Invoices -->
      {#if invoices.length > 0}
        <div class="bg-tokyo-night-surface rounded-lg p-6 mb-8">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Recent Invoices</h2>
          <div class="overflow-x-auto">
            <table class="w-full">
              <thead>
                <tr class="border-b border-tokyo-night-selection">
                  <th class="text-left py-2">Invoice ID</th>
                  <th class="text-left py-2">Amount</th>
                  <th class="text-left py-2">Status</th>
                  <th class="text-left py-2">Due Date</th>
                  <th class="text-left py-2">Created</th>
                </tr>
              </thead>
              <tbody>
                {#each invoices.slice(0, 5) as invoice (invoice.id)}
                  <tr class="border-b border-tokyo-night-selection">
                    <td class="py-2 font-mono text-sm">{invoice.id}</td>
                    <td class="py-2">{formatCurrency(invoice.amount, invoice.currency)}</td>
                    <td class="py-2">
                      <span class="px-2 py-1 text-xs rounded-full {getStatusColor(invoice.status)} bg-opacity-20">
                        {invoice.status}
                      </span>
                    </td>
                    <td class="py-2">{formatDate(invoice.dueDate)}</td>
                    <td class="py-2">{formatDate(invoice.createdAt)}</td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>
        </div>
      {/if}

      <!-- Usage Records -->
      {#if usageRecords.length > 0}
        <div class="bg-tokyo-night-surface rounded-lg p-6">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Current Usage</h2>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            {#each usageRecords.slice(0, 4) as record, index (record.id ?? `${record.resourceType}-${index}`)}
              <div class="bg-tokyo-night-bg rounded-lg p-4">
                <div class="text-sm text-tokyo-night-comment">{record.resourceType}</div>
                <div class="text-lg font-semibold">{record.quantity} {record.unit}</div>
                <div class="text-sm text-tokyo-night-comment">{formatCurrency(record.cost)}</div>
              </div>
            {/each}
          </div>
        </div>
      {:else}
        <div class="bg-tokyo-night-surface rounded-lg p-6">
          <h2 class="text-xl font-semibold mb-4 text-tokyo-night-cyan">Usage</h2>
          <p class="text-tokyo-night-comment">No usage data available for this period.</p>
        </div>
      {/if}
    {/if}
  </div>
</div>
