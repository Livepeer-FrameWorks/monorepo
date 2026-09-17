<script lang="ts">
  import { incidentStatuses, type IncidentStatus } from "$lib/incidents";

  let {
    value,
    onchange,
  }: {
    value: IncidentStatus[];
    onchange: (value: IncidentStatus[]) => void;
  } = $props();

  function toggle(status: IncidentStatus) {
    onchange(value.includes(status) ? value.filter((item) => item !== status) : [...value, status]);
  }

  const chip =
    "px-3 py-1 text-sm font-medium rounded-md border transition-colors disabled:opacity-50";
  const on = "border-primary bg-primary text-primary-foreground";
  const off = "border-border text-muted-foreground hover:text-foreground hover:bg-muted/50";
</script>

<div class="flex flex-wrap items-center gap-1" role="group" aria-label="Filter by status">
  <button
    type="button"
    class="{chip} {value.length === 0 ? on : off}"
    aria-pressed={value.length === 0}
    onclick={() => onchange([])}
  >
    All
  </button>
  {#each incidentStatuses as status (status.value)}
    <button
      type="button"
      class="{chip} {value.includes(status.value) ? on : off}"
      aria-pressed={value.includes(status.value)}
      onclick={() => toggle(status.value)}
    >
      {status.label}
    </button>
  {/each}
</div>
