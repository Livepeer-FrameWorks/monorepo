<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import { resolve } from "$app/paths";
  import {
    fragment,
    GetClustersAccessStore,
    GetIncidentsStore,
    IncidentFieldsStore,
  } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import IncidentStatusFilter from "$lib/components/incidents/IncidentStatusFilter.svelte";
  import IncidentTable from "$lib/components/incidents/IncidentTable.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { applyIncidentUpdates, type IncidentRow, type IncidentStatus } from "$lib/incidents";
  import { watchIncidentUpdates } from "$lib/stores/incidents.svelte";

  const PAGE_SIZE = 25;
  // The Gateway caps incident pages at 200 rows.
  const MAX_PAGE_SIZE = 200;
  const AlertIcon = getIconComponent("AlertTriangle");

  const incidentsStore = new GetIncidentsStore();
  const clustersStore = new GetClustersAccessStore();
  const incidentFields = new IncidentFieldsStore();

  let statuses = $state<IncidentStatus[]>([]);
  let clusterId = $state("");
  let incidents = $state<IncidentRow[]>([]);
  let totalCount = $state(0);
  let endCursor = $state<string | null>(null);
  let hasNextPage = $state(false);
  let loading = $state(true);
  let loadingMore = $state(false);
  let loadError = $state("");
  let request = 0;

  const ownedClusters = $derived(
    ($clustersStore.data?.clustersAccess ?? []).filter((cluster) => cluster.accessLevel === "owner")
  );
  const filtered = $derived(statuses.length > 0 || clusterId !== "");

  function clusterName(id: string) {
    return (
      ($clustersStore.data?.clustersAccess ?? []).find((cluster) => cluster.clusterId === id)
        ?.clusterName || id
    );
  }

  /**
   * `reset` loads the first page, `more` appends the next one, and `refresh`
   * reloads every loaded row for the current filters without a loading state;
   * a failed refresh keeps the rows shown.
   */
  async function load(mode: "reset" | "more" | "refresh" = "reset") {
    const current = ++request;
    if (mode === "more") loadingMore = true;
    else if (mode === "reset") loading = true;
    if (mode !== "refresh") loadError = "";
    try {
      const result = await incidentsStore.fetch({
        policy: "NetworkOnly",
        variables: {
          first:
            mode === "refresh"
              ? Math.min(MAX_PAGE_SIZE, Math.max(PAGE_SIZE, incidents.length))
              : PAGE_SIZE,
          after: mode === "more" ? endCursor : null,
          filter: {
            statuses: statuses.length ? statuses : null,
            clusterId: clusterId || null,
          },
        },
      });
      if (current !== request) return;
      const connection = result.data?.incidentsConnection;
      if (!connection) {
        if (mode === "refresh" && incidents.length > 0) return;
        loadError = result.errors?.[0]?.message ?? "Could not load incidents.";
        if (mode !== "more") incidents = [];
        return;
      }
      const rows = connection.nodes.map((node) => get(fragment(node, incidentFields)));
      incidents =
        mode === "more"
          ? [...incidents, ...rows.filter((row) => !incidents.some((item) => item.id === row.id))]
          : rows;
      totalCount = connection.totalCount;
      endCursor = connection.pageInfo.endCursor;
      hasNextPage = connection.pageInfo.hasNextPage;
      loadError = "";
    } catch {
      if (current === request && (mode !== "refresh" || incidents.length === 0)) {
        loadError = "Could not load incidents.";
      }
    } finally {
      if (current === request) {
        loading = false;
        loadingMore = false;
      }
    }
  }

  onMount(() => {
    void load();
    void clustersStore.fetch().catch(() => null);
    return watchIncidentUpdates(({ events, reconnected }) => {
      // A request that started before these changes may return rows without them.
      if (reconnected || loading || loadingMore) {
        void load("refresh");
        return;
      }
      const update = applyIncidentUpdates(incidents, events, { statuses, clusterId }, hasNextPage);
      incidents = update.rows;
      if (update.refetch) void load("refresh");
    });
  });
</script>

<svelte:head>
  <title>Incidents - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex items-center gap-3">
      <AlertIcon class="w-5 h-5 text-primary" />
      <div>
        <h1 class="text-xl font-bold text-foreground">Incidents</h1>
        <p class="text-sm text-muted-foreground">
          Alerts raised by platform monitoring on clusters you own, grouped into incidents
        </p>
      </div>
    </div>
    <div class="mt-3 flex flex-wrap items-center justify-between gap-3">
      <IncidentStatusFilter
        value={statuses}
        onchange={(next) => {
          statuses = next;
          void load();
        }}
      />
      {#if ownedClusters.length > 1}
        <label class="flex items-center gap-2 text-sm text-muted-foreground">
          Cluster
          <select
            class="border border-border bg-background px-2 py-1 text-sm text-foreground"
            value={clusterId}
            onchange={(event) => {
              clusterId = event.currentTarget.value;
              void load();
            }}
          >
            <option value="">All clusters</option>
            {#each ownedClusters as cluster (cluster.clusterId)}
              <option value={cluster.clusterId}>{cluster.clusterName}</option>
            {/each}
          </select>
        </label>
      {/if}
    </div>
  </div>

  <div class="flex-1 overflow-y-auto">
    <div class="slab">
      <div class="slab-header">
        <h3>
          {loading ? "Incidents" : `${totalCount} ${totalCount === 1 ? "incident" : "incidents"}`}
        </h3>
      </div>
      <div class="slab-body--flush">
        {#if loading && incidents.length === 0}
          <p role="status" class="p-6 text-sm text-muted-foreground">Loading incidents…</p>
        {:else if loadError}
          <p role="alert" class="p-6 text-sm text-destructive">{loadError}</p>
        {:else if incidents.length === 0}
          <EmptyState
            icon="AlertTriangle"
            title={filtered ? "No incidents match these filters" : "No incidents"}
            description={filtered
              ? "Clear a filter to see other incidents."
              : "Incidents come from platform alerting on clusters you own. When an alert fires on one of your clusters, it appears here with its alerts and timeline."}
            size="md"
            showAction={false}
          />
        {:else}
          <IncidentTable
            {incidents}
            {clusterName}
            hrefFor={(id) => resolve("/infrastructure/incidents/[id]", { id })}
          />
        {/if}
      </div>
      {#if hasNextPage && !loadError}
        <div class="slab-actions">
          <Button variant="ghost" disabled={loadingMore} onclick={() => load("more")}>
            {loadingMore ? "Loading…" : "Load more"}
          </Button>
        </div>
      {/if}
    </div>
  </div>
</div>
