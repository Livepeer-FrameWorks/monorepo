<script lang="ts">
  import { page } from "$app/state";
  import { resolve } from "$app/paths";
  import IncidentDetailView from "$lib/components/incidents/IncidentDetailView.svelte";

  const incidentId = $derived(page.params.id ?? "");
</script>

<svelte:head>
  <title>Incident - FrameWorks</title>
</svelte:head>

{#key incidentId}
  <IncidentDetailView
    {incidentId}
    backHref={resolve("/infrastructure/incidents")}
    backLabel="All incidents"
    reportHref={(reportId) => `${resolve("/skipper")}?report=${encodeURIComponent(reportId)}`}
    live
  />
{/key}
