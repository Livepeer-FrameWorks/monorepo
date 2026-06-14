<script lang="ts">
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { GetPlatformTenantsStore, type GetPlatformTenants$result } from "$houdini";
  import { auth } from "$lib/stores/auth";
  import { isPlatformOperatorUser } from "$lib/navigation";
  import { resolveTimeRange, TIME_RANGE_OPTIONS, DEFAULT_TIME_RANGE } from "$lib/utils/time-range";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
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
  import { Input } from "$lib/components/ui/input";
  import { getIconComponent } from "$lib/iconUtils";

  const BuildingIcon = getIconComponent("Building2");
  const RadioIcon = getIconComponent("Radio");
  const EyeIcon = getIconComponent("Eye");
  const CreditCardIcon = getIconComponent("CreditCard");

  const tenantsStore = new GetPlatformTenantsStore();

  type TenantRow = NonNullable<GetPlatformTenants$result["platform"]>["tenants"]["rows"][number];

  let timeRange = $state(DEFAULT_TIME_RANGE);
  let searchTerm = $state("");
  let loading = $state(true);
  let accessDenied = $state(false);

  let isOperator = $derived(isPlatformOperatorUser($auth.user));
  let rows = $derived(($tenantsStore.data?.platform?.tenants?.rows ?? []) as TenantRow[]);

  let filteredRows = $derived(
    rows.filter((row) => {
      if (!searchTerm.trim()) return true;
      const needle = searchTerm.toLowerCase();
      return (
        row.tenantId.toLowerCase().includes(needle) ||
        (row.tenant?.name ?? "").toLowerCase().includes(needle) ||
        (row.tenant?.subdomain ?? "").toLowerCase().includes(needle) ||
        (row.billing?.tierName ?? "").toLowerCase().includes(needle)
      );
    })
  );

  let totals = $derived({
    tenants: rows.length,
    liveStreams: rows.reduce((sum, r) => sum + (r.activity?.liveStreams ?? 0), 0),
    viewerHours: rows.reduce((sum, r) => sum + (r.activity?.viewerHours ?? 0), 0),
    outstanding: rows.reduce((sum, r) => sum + (r.billing?.outstandingAmount ?? 0), 0),
  });

  async function loadTenants() {
    loading = true;
    accessDenied = false;
    const range = resolveTimeRange(timeRange);
    const result = await tenantsStore
      .fetch({ variables: { timeRange: { start: range.start, end: range.end }, limit: 200 } })
      .catch(() => null);
    if (!result?.data?.platform) {
      accessDenied = true;
    }
    loading = false;
  }

  function handleTimeRangeChange(value: string | undefined) {
    if (!value || value === timeRange) return;
    timeRange = value;
    void loadTenants();
  }

  function openTenant(row: TenantRow) {
    void goto(resolve(`/admin/tenants/${row.tenantId}` as "/"));
  }

  function billingBadgeVariant(status?: string): "default" | "secondary" | "destructive" {
    if (!status) return "secondary";
    if (status === "suspended" || status === "cancelled") return "destructive";
    return "default";
  }

  function formatHours(value?: number): string {
    return (value ?? 0).toFixed(1);
  }

  function formatMoney(amount: number, currency = "EUR"): string {
    return new Intl.NumberFormat("en-IE", { style: "currency", currency }).format(amount);
  }

  function formatDate(value?: string | Date | null): string {
    if (!value) return "—";
    const date = typeof value === "string" ? new Date(value) : value;
    return date.toLocaleDateString();
  }

  onMount(() => {
    void loadTenants();
  });
</script>

<svelte:head>
  <title>Platform Admin — Tenants | FrameWorks</title>
</svelte:head>

{#if !loading && (accessDenied || !isOperator)}
  <EmptyState
    icon="ShieldCheck"
    title="Platform operator access required"
    description="This admin view is restricted to users with the platform operator grant."
    size="md"
    showAction={false}
  />
{:else}
  <div class="space-y-0">
    <div class="slab">
      <div class="slab-header">
        <div class="flex items-center gap-2">
          <BuildingIcon class="w-4 h-4 text-info" />
          <h3>Tenant activity</h3>
        </div>
        <div class="flex items-center gap-2">
          <Input class="w-56" placeholder="Filter tenants…" bind:value={searchTerm} />
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
      </div>

      <GridSeam cols={4} stack="2x2" surface="panel" flush={true}>
        <DashboardMetricCard
          icon={BuildingIcon}
          iconColor="text-info"
          value={totals.tenants}
          valueColor="text-foreground"
          label="Tenants"
        />
        <DashboardMetricCard
          icon={RadioIcon}
          iconColor="text-success"
          value={totals.liveStreams}
          valueColor="text-foreground"
          label="Live streams"
        />
        <DashboardMetricCard
          icon={EyeIcon}
          iconColor="text-primary"
          value={formatHours(totals.viewerHours)}
          valueColor="text-foreground"
          label="Viewer hours"
        />
        <DashboardMetricCard
          icon={CreditCardIcon}
          iconColor="text-warning"
          value={formatMoney(totals.outstanding)}
          valueColor="text-foreground"
          label="Outstanding"
        />
      </GridSeam>

      <div class="slab-body">
        {#if loading}
          <div class="p-6 text-sm text-muted-foreground">Loading tenant activity…</div>
        {:else if filteredRows.length === 0}
          <EmptyState
            icon="Building2"
            title="No tenants match"
            description="No tenants matched the current filter and time range."
            size="sm"
            showAction={false}
          />
        {:else}
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Tenant</TableHead>
                <TableHead>Tier</TableHead>
                <TableHead>Billing</TableHead>
                <TableHead class="text-right">Live</TableHead>
                <TableHead class="text-right">Viewers now</TableHead>
                <TableHead class="text-right">Ingest h</TableHead>
                <TableHead class="text-right">Viewer h</TableHead>
                <TableHead class="text-right">Egress GB</TableHead>
                <TableHead class="text-right">API reqs</TableHead>
                <TableHead class="text-right">Outstanding</TableHead>
                <TableHead>Last stream</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {#each filteredRows as row (row.tenantId)}
                <TableRow class="cursor-pointer" onclick={() => openTenant(row)}>
                  <TableCell>
                    <div class="font-medium">{row.tenant?.name ?? row.tenantId}</div>
                    {#if row.tenant?.subdomain}
                      <div class="text-xs text-muted-foreground">{row.tenant.subdomain}</div>
                    {/if}
                  </TableCell>
                  <TableCell>{row.billing?.tierName ?? "—"}</TableCell>
                  <TableCell>
                    {#if row.billing}
                      <Badge variant={billingBadgeVariant(row.billing.status)}>
                        {row.billing.status}
                      </Badge>
                      {#if row.billing.overdueInvoices > 0}
                        <Badge variant="destructive">{row.billing.overdueInvoices} overdue</Badge>
                      {/if}
                    {:else}
                      <Badge variant="secondary">no subscription</Badge>
                    {/if}
                  </TableCell>
                  <TableCell class="text-right">{row.activity.liveStreams}</TableCell>
                  <TableCell class="text-right">{row.activity.currentViewers}</TableCell>
                  <TableCell class="text-right">{formatHours(row.activity.ingestHours)}</TableCell>
                  <TableCell class="text-right">{formatHours(row.activity.viewerHours)}</TableCell>
                  <TableCell class="text-right">{row.activity.egressGb.toFixed(2)}</TableCell>
                  <TableCell class="text-right">{row.activity.apiRequests}</TableCell>
                  <TableCell class="text-right">
                    {row.billing
                      ? formatMoney(row.billing.outstandingAmount, row.billing.currency)
                      : "—"}
                  </TableCell>
                  <TableCell>{formatDate(row.activity.lastStreamAt)}</TableCell>
                </TableRow>
              {/each}
            </TableBody>
          </Table>
        {/if}
      </div>
    </div>
  </div>
{/if}
