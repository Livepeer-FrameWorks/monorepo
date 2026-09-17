<script lang="ts">
  import { page } from "$app/state";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import { isPlatformOperatorUser } from "$lib/navigation";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import IncidentDetailView from "$lib/components/incidents/IncidentDetailView.svelte";

  const incidentId = $derived(page.params.id ?? "");
  const isOperator = $derived(isPlatformOperatorUser($auth.user));
</script>

<svelte:head>
  <title>Platform Admin — Incident | FrameWorks</title>
</svelte:head>

{#if !isOperator}
  <EmptyState
    icon="ShieldCheck"
    title="Platform operator access required"
    description="This admin view is restricted to users with the platform operator grant."
    size="md"
    showAction={false}
  />
{:else}
  <!-- Skipper reports belong to the incident's tenant, so operators see the report ID, not a link. -->
  {#key incidentId}
    <IncidentDetailView
      {incidentId}
      backHref={resolve("/admin/incidents")}
      backLabel="All incidents"
      showTenant
      live
    />
  {/key}
{/if}
