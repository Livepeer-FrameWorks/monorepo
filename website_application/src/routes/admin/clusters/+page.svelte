<script lang="ts">
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { GetPlatformClustersStore, type GetPlatformClusters$result } from "$houdini";
  import { auth } from "$lib/stores/auth";
  import { isPlatformOperatorUser } from "$lib/navigation";
  import { Badge } from "$lib/components/ui/badge";
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

  const ServerIcon = getIconComponent("Server");

  const clustersStore = new GetPlatformClustersStore();

  type ClusterRow = NonNullable<GetPlatformClusters$result["platform"]>["clusters"][number];

  let loading = $state(true);
  let accessDenied = $state(false);

  let isOperator = $derived(isPlatformOperatorUser($auth.user));
  let rows = $derived(($clustersStore.data?.platform?.clusters ?? []) as ClusterRow[]);

  async function loadClusters() {
    loading = true;
    accessDenied = false;
    const result = await clustersStore.fetch().catch(() => null);
    if (!result?.data?.platform) {
      accessDenied = true;
    }
    loading = false;
  }

  function openTenant(tenantId: string) {
    void goto(resolve(`/admin/tenants/${tenantId}` as "/"));
  }

  function formatMbps(bytesPerSec?: number): string {
    if (!bytesPerSec) return "0.0";
    return ((bytesPerSec * 8) / 1_000_000).toFixed(1);
  }

  onMount(() => {
    void loadClusters();
  });
</script>

<svelte:head>
  <title>Platform Admin — Clusters | FrameWorks</title>
</svelte:head>

{#if !loading && (accessDenied || !isOperator)}
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
        <div class="flex items-center gap-2">
          <ServerIcon class="w-4 h-4 text-info" />
          <h3>Clusters</h3>
        </div>
      </div>
      <div class="slab-body">
        {#if loading}
          <div class="p-6 text-sm text-muted-foreground">Loading clusters…</div>
        {:else if rows.length === 0}
          <EmptyState
            icon="Server"
            title="No clusters"
            description="Quartermaster returned no clusters."
            size="sm"
            showAction={false}
          />
        {:else}
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Cluster</TableHead>
                <TableHead>Type</TableHead>
                <TableHead class="text-right">Live streams</TableHead>
                <TableHead class="text-right">Viewers</TableHead>
                <TableHead class="text-right">In Mbps</TableHead>
                <TableHead class="text-right">Out Mbps</TableHead>
                <TableHead class="text-right">Nodes</TableHead>
                <TableHead>Tenants</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {#each rows as row (row.cluster.clusterId)}
                <TableRow>
                  <TableCell>
                    <div class="font-medium">{row.cluster.clusterName}</div>
                    <div class="text-xs text-muted-foreground">{row.cluster.clusterId}</div>
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary">{row.cluster.clusterType}</Badge>
                  </TableCell>
                  <TableCell class="text-right">{row.liveStats?.activeStreams ?? "—"}</TableCell>
                  <TableCell class="text-right">{row.liveStats?.currentViewers ?? "—"}</TableCell>
                  <TableCell class="text-right"
                    >{formatMbps(row.liveStats?.uploadBytesPerSec)}</TableCell
                  >
                  <TableCell class="text-right"
                    >{formatMbps(row.liveStats?.downloadBytesPerSec)}</TableCell
                  >
                  <TableCell class="text-right">{row.liveStats?.activeNodes ?? "—"}</TableCell>
                  <TableCell>
                    <div class="flex flex-wrap items-center gap-1">
                      {#each row.tenants as tenant (tenant.id)}
                        <button
                          class="cursor-pointer"
                          onclick={() => openTenant(tenant.id)}
                          title={tenant.subdomain ?? tenant.id}
                        >
                          <Badge>{tenant.name}</Badge>
                        </button>
                      {/each}
                      {#if row.tenantCount > row.tenants.length}
                        <span class="text-xs text-muted-foreground">
                          +{row.tenantCount - row.tenants.length} more
                        </span>
                      {/if}
                    </div>
                  </TableCell>
                </TableRow>
              {/each}
            </TableBody>
          </Table>
        {/if}
      </div>
    </div>
  </div>
{/if}
