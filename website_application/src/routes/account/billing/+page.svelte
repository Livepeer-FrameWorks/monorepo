<script>
  import { onMount } from "svelte";
  import { auth } from "$lib/stores/auth";
  import { billingAPI } from "$lib/api";

  let isAuthenticated = false;
  let loading = true;
  let billingStatus = null;
  let availableTiers = [];
  let usageRecords = [];
  let invoices = [];
  let error = null;

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
      // Load billing status (includes subscription and tier info)
      const statusResponse = await billingAPI.getBillingStatus();
      billingStatus = statusResponse.data || {
        subscription: { status: 'inactive' },
        tier: { display_name: 'Free', tier_name: 'free' },
        payment_methods: []
      };

      // Load available tiers for upgrade/downgrade options
      const tiersResponse = await billingAPI.getTiers();
      availableTiers = tiersResponse.data || [];

      // Load recent usage records
      const usageResponse = await billingAPI.getUsageRecords({ 
        billing_month: new Date().toISOString().substring(0, 7) // Current month YYYY-MM
      });
      usageRecords = usageResponse.data?.usage_records || [];

      // Load recent invoices  
      const invoicesResponse = await billingAPI.getInvoices();
      invoices = invoicesResponse.data?.invoices || [];

      console.log('Billing status loaded:', billingStatus);
      console.log('Available tiers:', availableTiers);
      console.log('Usage records:', usageRecords);
      console.log('Invoices:', invoices);

    } catch (err) {
      error = err?.response?.data?.error || err?.message || String(err);
      console.error("Failed to load billing data:", err);
      
      // Fallback to basic data structure for display
      billingStatus = {
        subscription: { status: 'error' },
        tier: { tier_name: "unknown", display_name: "Unknown Tier" },
        payment_methods: []
      };
    }
  }

  async function handleTierChange(newTierID) {
    try {
      loading = true;
      await billingAPI.updateSubscription({ tier_id: newTierID });
      await loadBillingData(); // Reload to show updated subscription
      alert("Subscription updated successfully!");
    } catch (err) {
      alert(`Failed to update subscription: ${err?.message || err}`);
    } finally {
      loading = false;
    }
  }

  async function createPayment(invoiceId, method) {
    try {
      const response = await billingAPI.createPayment(invoiceId, method, billingStatus?.tier?.currency || 'EUR');
      
      if (response.data.payment_url) {
        // Redirect to payment provider (Mollie/Stripe)
        window.location.href = response.data.payment_url;
      } else if (response.data.wallet_address) {
        // Show crypto payment info
        alert(`Please send payment to: ${response.data.wallet_address}`);
      }
    } catch (err) {
      alert(`Failed to create payment: ${err?.message || err}`);
    }
  }
</script>

<svelte:head>
  <title>Billing & Usage - FrameWorks</title>
</svelte:head>

<div class="space-y-8 page-transition">
  <!-- Page Header -->
  <div class="flex justify-between items-start">
    <div>
      <h1 class="text-3xl font-bold text-tokyo-night-fg mb-2">
        💳 Billing & Usage
      </h1>
      <p class="text-tokyo-night-fg-dark">
        Manage your subscription, view usage, and payment history
      </p>
    </div>
  </div>

  {#if loading}
    <div class="flex items-center justify-center min-h-64">
      <div class="loading-spinner w-8 h-8" />
    </div>
  {:else if error}
    <div class="card border-tokyo-night-red/30">
      <div class="text-center py-12">
        <div class="text-6xl mb-4">❌</div>
        <h3 class="text-xl font-semibold text-tokyo-night-red mb-2">
          Failed to Load Billing Data
        </h3>
        <p class="text-tokyo-night-fg-dark mb-6">{error}</p>
        <button class="btn-primary" on:click={loadBillingData}>
          Try Again
        </button>
      </div>
    </div>
  {:else}
    <!-- Current Plan -->
    <div class="glow-card p-6">
      <div class="flex justify-between items-start mb-6">
        <div>
          <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            Current Plan
          </h2>
          <p class="text-tokyo-night-fg-dark">
            Your current subscription and billing status
          </p>
        </div>
        <span class="bg-tokyo-night-green/20 text-tokyo-night-green px-3 py-1 rounded-full text-sm font-medium capitalize">
          {billingStatus.subscription?.status || 'inactive'}
        </span>
      </div>

      <div class="grid md:grid-cols-3 gap-6">
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
          <h3 class="text-sm text-tokyo-night-comment mb-1">Plan</h3>
          <p class="text-lg font-semibold text-tokyo-night-fg">
            {billingStatus.tier?.display_name || 'Free'}
          </p>
        </div>
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
          <h3 class="text-sm text-tokyo-night-comment mb-1">Price</h3>
          <p class="text-lg font-semibold text-tokyo-night-fg">
            {billingStatus.tier?.base_price === 0 || !billingStatus.tier?.base_price ? 'Free' : `${billingStatus.tier.currency || '$'}${billingStatus.tier.base_price}/${billingStatus.tier.billing_period || 'month'}`}
          </p>
        </div>
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
          <h3 class="text-sm text-tokyo-night-comment mb-1">Next Billing</h3>
          <p class="text-lg font-semibold text-tokyo-night-fg">
            {billingStatus.subscription?.next_billing_date ? new Date(billingStatus.subscription.next_billing_date).toLocaleDateString() : 'N/A'}
          </p>
        </div>
      </div>

      <div class="flex space-x-3 mt-6">
        <button class="btn-primary">
          Upgrade Plan
        </button>
        <button class="btn-secondary">
          Manage Payment Methods
        </button>
      </div>
    </div>

    <!-- Usage Overview -->
    <div class="glow-card p-6">
      <h2 class="text-xl font-semibold text-tokyo-night-fg mb-6">
        Usage Overview
      </h2>
      
      <div class="grid md:grid-cols-2 lg:grid-cols-4 gap-6">
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
          <h3 class="text-sm text-tokyo-night-comment mb-1">Bandwidth Used</h3>
          <p class="text-2xl font-bold text-tokyo-night-blue">0 GB</p>
          <p class="text-xs text-tokyo-night-comment">This month</p>
        </div>
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
          <h3 class="text-sm text-tokyo-night-comment mb-1">Stream Hours</h3>
          <p class="text-2xl font-bold text-tokyo-night-green">0 hrs</p>
          <p class="text-xs text-tokyo-night-comment">This month</p>
        </div>
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
          <h3 class="text-sm text-tokyo-night-comment mb-1">Storage Used</h3>
          <p class="text-2xl font-bold text-tokyo-night-purple">0 MB</p>
          <p class="text-xs text-tokyo-night-comment">Total</p>
        </div>
        <div class="bg-tokyo-night-bg-highlight p-4 rounded-lg">
          <h3 class="text-sm text-tokyo-night-comment mb-1">API Calls</h3>
          <p class="text-2xl font-bold text-tokyo-night-yellow">0</p>
          <p class="text-xs text-tokyo-night-comment">This month</p>
        </div>
      </div>

      <div class="mt-6 p-4 bg-tokyo-night-yellow/10 border border-tokyo-night-yellow/30 rounded-lg">
        <div class="flex items-center space-x-2">
          <svg class="w-5 h-5 text-tokyo-night-yellow" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 16h-1v-4h-1m1-4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z" />
          </svg>
          <span class="text-tokyo-night-yellow font-medium text-sm">Usage Tracking Integration</span>
        </div>
        <p class="text-tokyo-night-yellow/80 text-sm mt-2">
          Usage metrics are being collected but not yet connected to billing. Full usage-based billing coming soon!
        </p>
      </div>
    </div>

    <!-- Invoice History -->
    <div class="glow-card p-6">
      <h2 class="text-xl font-semibold text-tokyo-night-fg mb-6">
        Invoice History
      </h2>
      
      {#if invoices.length === 0}
        <div class="text-center py-12">
          <div class="text-6xl mb-4">📄</div>
          <h3 class="text-xl font-semibold text-tokyo-night-fg mb-2">
            No Invoices Yet
          </h3>
          <p class="text-tokyo-night-fg-dark">
            Your invoice history will appear here once you upgrade to a paid plan
          </p>
        </div>
      {:else}
        <!-- Invoice list would go here -->
        <p class="text-tokyo-night-comment">Invoice history will be displayed here</p>
      {/if}
    </div>
  {/if}
</div> 