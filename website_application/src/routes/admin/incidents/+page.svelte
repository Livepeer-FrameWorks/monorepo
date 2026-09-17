<script lang="ts">
  import { onMount } from "svelte";
  import { get } from "svelte/store";
  import { resolve } from "$app/paths";
  import { fragment, GetPlatformIncidentsStore, IncidentFieldsStore } from "$houdini";
  import { auth } from "$lib/stores/auth";
  import { isPlatformOperatorUser } from "$lib/navigation";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import IncidentStatusFilter from "$lib/components/incidents/IncidentStatusFilter.svelte";
  import IncidentTable from "$lib/components/incidents/IncidentTable.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { applyIncidentUpdates, type IncidentRow, type IncidentStatus } from "$lib/incidents";
  import { watchIncidentUpdates } from "$lib/stores/incidents.svelte";

  const PAGE_SIZE = 50;
  // The Gateway caps incident pages at 200 rows.
  const MAX_PAGE_SIZE = 200;
  const AlertIcon = getIconComponent("AlertTriangle");

  const incidentsStore = new GetPlatformIncidentsStore();
  const incidentFields = new IncidentFieldsStore();

  type Scope = "" | "PLATFORM" | "TENANT";

  let scope = $state<Scope>("");
  let statuses = $state<IncidentStatus[]>(["FIRING", "ACKNOWLEDGED"]);
  let tenantId = $state("");
  let clusterId = $state("");
  // The tenant and cluster inputs apply on submit; loads and live updates use
  // the values last applied, not unsubmitted typing.
  let appliedTenantId = "";
  let appliedClusterId = "";
  let incidents = $state<IncidentRow[]>([]);
  let totalCount = $state(0);
  let endCursor = $state<string | null>(null);
  let hasNextPage = $state(false);
  let loading = $state(true);
  let loadingMore = $state(false);
  let accessDenied = $state(false);
  let loadError = $state("");
  let request = 0;

  const isOperator = $derived(isPlatformOperatorUser($auth.user));

  /**
   * `reset` applies the filter inputs and loads the first page, `more` appends
   * the next one, and `refresh` reloads every loaded row for the applied filters
   * without a loading state; a failed refresh keeps the rows shown.
   */
  async function load(mode: "reset" | "more" | "refresh" = "reset") {
    const current = ++request;
    if (mode === "reset") {
      appliedTenantId = tenantId.trim();
      appliedClusterId = clusterId.trim();
    }
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
            scope: scope || null,
            statuses: statuses.length ? statuses : null,
            tenantId: appliedTenantId || null,
            clusterId: appliedClusterId || null,
          },
        },
      });
      if (current !== request) return;
      const connection = result.data?.platform?.incidents;
      if (!connection) {
        if (mode === "refresh" && incidents.length > 0) return;
        // A failed incidents field nulls the whole platform object, so only a
        // non-operator is treated as denied; operators see the error.
        if (isOperator && result.errors?.length) loadError = result.errors[0].message;
        else if (isOperator) loadError = "Could not load incidents.";
        else accessDenied = true;
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
    // Operators receive changes to every incident. Events carry no scope or
    // tenant, so with those filters set an unloaded incident that matches the
    // status and cluster filters refetches rather than being assumed outside
    // the list.
    return watchIncidentUpdates(({ events, reconnected }) => {
      if (accessDenied) return;
      // A request that started before these changes may return rows without them.
      if (reconnected || loading || loadingMore) {
        void load("refresh");
        return;
      }
      const update = applyIncidentUpdates(
        incidents,
        events,
        { statuses, clusterId: appliedClusterId },
        hasNextPage
      );
      incidents = update.rows;
      if (update.refetch) void load("refresh");
    });
  });
</script>

<svelte:head>
  <title>Platform Admin — Incidents | FrameWorks</title>
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
          <AlertIcon class="w-4 h-4 text-info" />
          <h3>Incidents{loading ? "" : ` (${totalCount})`}</h3>
        </div>
      </div>
      <form
        class="slab-body--padded flex flex-wrap items-center gap-3 border-b border-[hsl(var(--tn-fg-gutter)/0.3)]"
        onsubmit={(event) => {
          event.preventDefault();
          void load();
        }}
      >
        <IncidentStatusFilter
          value={statuses}
          onchange={(next) => {
            statuses = next;
            void load();
          }}
        />
        <label class="flex items-center gap-2 text-sm text-muted-foreground">
          Scope
          <select
            class="border border-border bg-background px-2 py-1 text-sm text-foreground"
            value={scope}
            onchange={(event) => {
              scope = event.currentTarget.value as Scope;
              void load();
            }}
          >
            <option value="">All scopes</option>
            <option value="PLATFORM">Platform</option>
            <option value="TENANT">Tenant</option>
          </select>
        </label>
        <Input class="w-64" placeholder="Tenant ID" aria-label="Tenant ID" bind:value={tenantId} />
        <Input
          class="w-48"
          placeholder="Cluster ID"
          aria-label="Cluster ID"
          bind:value={clusterId}
        />
        <Button type="submit" variant="outline" size="sm">Apply</Button>
      </form>
      <div class="slab-body">
        {#if loading && incidents.length === 0}
          <div class="p-6 text-sm text-muted-foreground">Loading incidents…</div>
        {:else if loadError}
          <p role="alert" class="p-6 text-sm text-destructive">{loadError}</p>
        {:else if incidents.length === 0}
          <EmptyState
            icon="AlertTriangle"
            title="No incidents match these filters"
            description="Incidents are created from Alertmanager notifications. Platform-scope incidents cover shared infrastructure; tenant-scope incidents cover clusters a tenant owns."
            size="sm"
            showAction={false}
          />
        {:else}
          <IncidentTable
            {incidents}
            showScope
            hrefFor={(id) => resolve("/admin/incidents/[id]", { id })}
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
{/if}
