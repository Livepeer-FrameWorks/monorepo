<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/stores";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { SvelteURLSearchParams } from "svelte/reactivity";
  import {
    GetPlatformTenantOverviewStore,
    GetPlatformTenantBillingStore,
    GetPlatformTenantContentStore,
  } from "$houdini";
  import { auth } from "$lib/stores/auth";
  import { isPlatformOperatorUser } from "$lib/navigation";
  import { resolveTimeRange, TIME_RANGE_OPTIONS, DEFAULT_TIME_RANGE } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { Tabs, TabsContent, TabsList, TabsTrigger } from "$lib/components/ui/tabs";
  import { Badge } from "$lib/components/ui/badge";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import { getIconComponent } from "$lib/iconUtils";

  const RadioIcon = getIconComponent("Radio");
  const EyeIcon = getIconComponent("Eye");
  const ServerIcon = getIconComponent("Server");
  const CreditCardIcon = getIconComponent("CreditCard");

  type TabId = "overview" | "billing" | "content";
  const TABS: TabId[] = ["overview", "billing", "content"];

  const overviewStore = new GetPlatformTenantOverviewStore();
  const billingStore = new GetPlatformTenantBillingStore();
  const contentStore = new GetPlatformTenantContentStore();

  let tenantId = $derived($page.params.id ?? "");
  let activeTab = $state<TabId>("overview");
  let timeRange = $state(DEFAULT_TIME_RANGE);
  let accessDenied = $state(false);
  let loadedTabs = $state(new Set<string>());

  let isOperator = $derived(isPlatformOperatorUser($auth.user));
  let detail = $derived($overviewStore.data?.platform?.tenant ?? null);
  let billing = $derived($billingStore.data?.platform?.tenant?.billing ?? null);
  let content = $derived($contentStore.data?.platform?.tenant?.content ?? null);

  function rangeVars() {
    const range = resolveTimeRange(timeRange);
    return { timeRange: { start: range.start, end: range.end } };
  }

  async function loadTab(tab: TabId, force = false) {
    const key = `${tab}:${timeRange}`;
    if (!force && loadedTabs.has(key)) return;
    loadedTabs.add(key);

    if (tab === "overview") {
      const result = await overviewStore
        .fetch({ variables: { id: tenantId, ...rangeVars() } })
        .catch(() => null);
      if (!result?.data?.platform) accessDenied = true;
    } else if (tab === "billing") {
      await billingStore
        .fetch({ variables: { id: tenantId, invoicesFirst: 25, ...rangeVars() } })
        .catch(() => null);
    } else if (tab === "content") {
      await contentStore.fetch({ variables: { id: tenantId } }).catch(() => null);
    }
  }

  function syncUrl() {
    const params = new SvelteURLSearchParams();
    if (activeTab !== "overview") params.set("tab", activeTab);
    const query = params.toString();
    const url = `/admin/tenants/${tenantId}${query ? `?${query}` : ""}`;
    void goto(resolve(url as "/"), { replaceState: true, keepFocus: true, noScroll: true });
  }

  function handleTabChange(value: string | undefined) {
    if (!value || value === activeTab) return;
    activeTab = value as TabId;
    syncUrl();
    void loadTab(activeTab);
  }

  function handleTimeRangeChange(value: string | undefined) {
    if (!value || value === timeRange) return;
    timeRange = value;
    void loadTab(activeTab, true);
  }

  function formatMoney(amount: number, currency = "EUR"): string {
    return new Intl.NumberFormat("en-IE", { style: "currency", currency }).format(amount);
  }

  function formatCents(cents: number, currency = "EUR"): string {
    return formatMoney(cents / 100, currency);
  }

  function formatDate(value?: string | Date | null): string {
    if (!value) return "—";
    const date = typeof value === "string" ? new Date(value) : value;
    return date.toLocaleDateString();
  }

  onMount(() => {
    const urlTab = $page.url.searchParams.get("tab");
    if (urlTab && TABS.includes(urlTab as TabId)) {
      activeTab = urlTab as TabId;
    }
    void loadTab("overview");
    if (activeTab !== "overview") void loadTab(activeTab);
  });
</script>

<svelte:head>
  <title>Platform Admin — Tenant | FrameWorks</title>
</svelte:head>

{#if accessDenied || (!isOperator && !$overviewStore.fetching)}
  <EmptyState
    icon="ShieldCheck"
    title="Platform operator access required"
    description="This admin view is restricted to owners/admins of the system tenant."
    size="md"
    showAction={false}
  />
{:else}
  <div class="space-y-0">
    <div class="slab">
      <div class="slab-header">
        <div class="flex items-center gap-3">
          <h3>{detail?.tenant?.name ?? tenantId}</h3>
          {#if detail?.tenant?.subdomain}
            <Badge variant="secondary">{detail.tenant.subdomain}</Badge>
          {/if}
        </div>
        <Select type="single" value={timeRange} onValueChange={handleTimeRangeChange}>
          <SelectTrigger class="w-40">
            {TIME_RANGE_OPTIONS.find((o) => o.value === timeRange)?.label ?? timeRange}
          </SelectTrigger>
          <SelectContent>
            {#each TIME_RANGE_OPTIONS as option (option.value)}
              <SelectItem value={option.value}>{option.label}</SelectItem>
            {/each}
          </SelectContent>
        </Select>
      </div>

      <div class="slab-body--padded">
        <Tabs value={activeTab} onValueChange={handleTabChange}>
          <TabsList>
            <TabsTrigger value="overview">Overview</TabsTrigger>
            <TabsTrigger value="billing">Billing</TabsTrigger>
            <TabsTrigger value="content">Content</TabsTrigger>
          </TabsList>

          <TabsContent value="overview">
            {#if detail}
              <GridSeam cols={4} stack="2x2" surface="panel" flush={true}>
                <DashboardMetricCard
                  icon={RadioIcon}
                  iconColor="text-success"
                  value={detail.activity.liveStreams}
                  valueColor="text-foreground"
                  label="Live streams"
                  subtitle={`${detail.activity.currentViewers} viewers now`}
                />
                <DashboardMetricCard
                  icon={ServerIcon}
                  iconColor="text-info"
                  value={detail.activity.ingestHours.toFixed(1)}
                  valueColor="text-foreground"
                  label="Ingest hours"
                />
                <DashboardMetricCard
                  icon={EyeIcon}
                  iconColor="text-primary"
                  value={detail.activity.viewerHours.toFixed(1)}
                  valueColor="text-foreground"
                  label="Viewer hours"
                  subtitle={`${detail.activity.uniqueViewers} unique`}
                />
                <DashboardMetricCard
                  icon={CreditCardIcon}
                  iconColor="text-warning"
                  value={detail.activity.apiRequests}
                  valueColor="text-foreground"
                  label="API requests"
                  subtitle={`${detail.activity.apiErrors} errors`}
                />
              </GridSeam>

              <div class="mt-4 grid grid-cols-2 gap-4 text-sm">
                <div>
                  <div class="text-muted-foreground">Tenant ID</div>
                  <div class="font-mono">{tenantId}</div>
                </div>
                <div>
                  <div class="text-muted-foreground">Created</div>
                  <div>{formatDate(detail.tenant?.createdAt)}</div>
                </div>
                <div>
                  <div class="text-muted-foreground">Last stream</div>
                  <div>{formatDate(detail.activity.lastStreamAt)}</div>
                </div>
                <div>
                  <div class="text-muted-foreground">Egress ({timeRange})</div>
                  <div>{detail.activity.egressGb.toFixed(2)} GB</div>
                </div>
              </div>
            {:else}
              <div class="p-6 text-sm text-muted-foreground">Loading…</div>
            {/if}
          </TabsContent>

          <TabsContent value="billing">
            {#if billing}
              {#if billing.snapshot}
                <div class="mb-4 flex flex-wrap items-center gap-2">
                  <Badge>{billing.snapshot.tierName}</Badge>
                  <Badge
                    variant={billing.snapshot.status === "suspended" ? "destructive" : "default"}
                  >
                    {billing.snapshot.status}
                  </Badge>
                  <Badge variant="secondary">{billing.snapshot.billingModel}</Badge>
                  {#if billing.snapshot.trialEndsAt}
                    <Badge variant="secondary"
                      >trial ends {formatDate(billing.snapshot.trialEndsAt)}</Badge
                    >
                  {/if}
                  <span class="text-sm text-muted-foreground">
                    Outstanding {formatMoney(
                      billing.snapshot.outstandingAmount,
                      billing.snapshot.currency
                    )}
                    · Prepaid {formatCents(
                      billing.snapshot.prepaidBalanceCents,
                      billing.snapshot.currency
                    )}
                  </span>
                </div>
              {/if}

              <h4 class="mb-2 text-sm font-medium">Invoices ({billing.invoices.totalCount})</h4>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Invoice</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead class="text-right">Amount</TableHead>
                    <TableHead>Due</TableHead>
                    <TableHead>Created</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {#each billing.invoices.edges as edge (edge.node.id)}
                    <TableRow>
                      <TableCell class="font-mono text-xs">{edge.node.id}</TableCell>
                      <TableCell>
                        <Badge variant={edge.node.status === "OVERDUE" ? "destructive" : "default"}>
                          {edge.node.status}
                        </Badge>
                      </TableCell>
                      <TableCell class="text-right">
                        {formatMoney(edge.node.amount, edge.node.currency)}
                      </TableCell>
                      <TableCell>{formatDate(edge.node.dueDate)}</TableCell>
                      <TableCell>{formatDate(edge.node.createdAt)}</TableCell>
                    </TableRow>
                  {/each}
                </TableBody>
              </Table>

              <h4 class="mb-2 mt-6 text-sm font-medium">
                Usage records ({billing.usageRecords.totalCount})
              </h4>
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Meter</TableHead>
                    <TableHead class="text-right">Value</TableHead>
                    <TableHead>Cluster</TableHead>
                    <TableHead>Period</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {#each billing.usageRecords.nodes as record (record.id)}
                    <TableRow>
                      <TableCell>{record.usageType}</TableCell>
                      <TableCell class="text-right">{record.usageValue.toFixed(3)}</TableCell>
                      <TableCell>{record.clusterId ?? "—"}</TableCell>
                      <TableCell class="text-xs">
                        {formatDate(record.periodStart)} – {formatDate(record.periodEnd)}
                      </TableCell>
                    </TableRow>
                  {/each}
                </TableBody>
              </Table>
            {:else}
              <div class="p-6 text-sm text-muted-foreground">Loading billing…</div>
            {/if}
          </TabsContent>

          <TabsContent value="content">
            {#if content}
              <GridSeam cols={4} stack="2x2" surface="panel" flush={true}>
                <DashboardMetricCard
                  icon={ServerIcon}
                  iconColor="text-info"
                  value={content.artifactCount}
                  valueColor="text-foreground"
                  label="Artifacts"
                  subtitle="VOD + DVR + clips"
                />
                <DashboardMetricCard
                  icon={EyeIcon}
                  iconColor="text-primary"
                  value={content.userCount}
                  valueColor="text-foreground"
                  label="Active users"
                />
                <DashboardMetricCard
                  icon={RadioIcon}
                  iconColor="text-success"
                  value={content.liveStreams}
                  valueColor="text-foreground"
                  label="Live streams"
                />
                <DashboardMetricCard
                  icon={CreditCardIcon}
                  iconColor="text-muted-foreground"
                  value={formatDate(content.lastStreamAt)}
                  valueColor="text-foreground"
                  label="Last stream"
                />
              </GridSeam>
            {:else}
              <div class="p-6 text-sm text-muted-foreground">Loading content…</div>
            {/if}
          </TabsContent>
        </Tabs>
      </div>
    </div>
  </div>
{/if}
