<script lang="ts">
  import { Checkbox } from "$lib/components/ui/checkbox";

  interface ClusterOption {
    clusterId: string;
    clusterName: string;
  }

  let {
    selectedIds = $bindable(),
    options = [],
    required = false,
    disabled = false,
    onchange,
  }: {
    selectedIds: string;
    options?: ClusterOption[];
    required?: boolean;
    disabled?: boolean;
    onchange?: () => void;
  } = $props();

  const selected = $derived(
    selectedIds
      .split(",")
      .map((value) => value.trim())
      .filter(Boolean)
  );
  const choices = $derived.by(() => {
    const known = [...options];
    for (const clusterId of selected) {
      if (!known.some((option) => option.clusterId === clusterId)) {
        known.push({ clusterId, clusterName: clusterId });
      }
    }
    return known;
  });

  function setSelection(next: string[]) {
    selectedIds = next.join(", ");
    onchange?.();
  }

  function toggle(clusterId: string, checked: boolean) {
    const next = checked
      ? [...selected.filter((value) => value !== clusterId), clusterId]
      : selected.filter((value) => value !== clusterId);
    setSelection(next);
  }
</script>

<fieldset class="space-y-2" {disabled}>
  <legend class="text-sm font-medium text-muted-foreground">
    Source clusters{#if required}<span class="text-destructive"> *</span>{/if}
  </legend>
  {#if !required}
    <label class="flex items-start gap-3 border border-border/50 p-3">
      <Checkbox
        checked={selected.length === 0}
        onCheckedChange={(checked) => checked && setSelection([])}
      />
      <span>
        <span class="block text-sm font-medium">Any compatible cluster</span>
        <span class="block text-xs text-muted-foreground">
          Let FrameWorks choose from your connected media capacity.
        </span>
      </span>
    </label>
  {/if}
  {#if choices.length}
    {#each choices as option (option.clusterId)}
      <label class="flex items-start gap-3 border border-border/50 p-3">
        <Checkbox
          checked={selected.includes(option.clusterId)}
          onCheckedChange={(checked) => toggle(option.clusterId, checked)}
        />
        <span class="min-w-0">
          <span class="block text-sm font-medium truncate">{option.clusterName}</span>
          <span class="block text-xs font-mono text-muted-foreground truncate">
            {option.clusterId}
          </span>
        </span>
      </label>
    {/each}
  {:else}
    <p class="border border-border/50 p-3 text-sm text-muted-foreground">
      No connected clusters are available to pin. Public sources can use automatic placement;
      private sources need an eligible self-hosted cluster first.
    </p>
  {/if}
</fieldset>
