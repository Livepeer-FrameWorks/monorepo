<script lang="ts">
  import { SetNodeCapacityStore } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { toast } from "$lib/stores/toast";
  import { budgetFromMbps, budgetInput, type NodeCapacity } from "./capacity";

  interface Props {
    nodeId: string;
    capacity: NodeCapacity | null;
    onCapacityChanged?: () => Promise<void> | void;
  }
  let { nodeId, capacity, onCapacityChanged }: Props = $props();
  const mutation = new SetNodeCapacityStore();
  let egress = $state("");
  let ingress = $state("");
  let revision = $state<number | null>(null);
  let boundNode = $state("");
  let saving = $state(false);
  let changed = $derived(capacity !== null && revision !== null && capacity.revision !== revision);

  function load(value = capacity) {
    if (!value) return;
    egress = budgetInput(value.egressBudgetBytesPerSec, value.egressSource);
    ingress = budgetInput(value.ingressBudgetBytesPerSec, value.ingressSource);
    revision = value.revision;
  }
  $effect(() => {
    if (boundNode !== nodeId) {
      boundNode = nodeId;
      revision = null;
    }
    if (revision === null && capacity) load(capacity);
  });

  async function save() {
    if (revision === null || changed || saving) return;
    try {
      const egressBudgetBytesPerSec = budgetFromMbps(egress);
      const ingressBudgetBytesPerSec = budgetFromMbps(ingress);
      saving = true;
      const response = await mutation.mutate({
        input: {
          nodeId,
          egressBudgetBytesPerSec,
          ingressBudgetBytesPerSec,
          expectedRevision: revision,
        },
      });
      const result = response.data?.setNodeCapacity;
      if (result?.__typename === "InfrastructureNode") {
        if (result.capacity) load(result.capacity);
        toast.success("Node capacity saved");
        await onCapacityChanged?.();
      } else {
        toast.error(
          result?.message ?? response.errors?.[0]?.message ?? "Capacity could not be saved"
        );
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Capacity could not be saved");
    } finally {
      saving = false;
    }
  }
</script>

<section class="border border-border rounded-md p-4">
  <h3 class="text-base font-semibold mb-2">Bandwidth capacity</h3>
  <p class="text-xs text-muted-foreground mb-4">
    Set this node's link budgets in Mbit/s. Leave a direction blank for default 1 Gbit/s. New
    traffic uses up to 85% of the budget.
  </p>
  {#if capacity}
    <div class="text-sm mb-3">
      <p>
        Egress: {(capacity.egressBudgetBytesPerSec / 125_000).toLocaleString()} Mbit/s · {capacity.egressSource ===
        "DEFAULT"
          ? "default"
          : "operator"}
      </p>
      <p>
        Ingress: {(capacity.ingressBudgetBytesPerSec / 125_000).toLocaleString()} Mbit/s · {capacity.ingressSource ===
        "DEFAULT"
          ? "default"
          : "operator"}
      </p>
    </div>
    {#if capacity.appliedRevision === null}
      <p class="text-xs text-warning mb-3">
        Applied capacity is unknown. The edge may be offline or Helmsman may not report capacity.
      </p>
    {:else if capacity.appliedRevision !== capacity.revision}
      <p class="text-xs text-warning mb-3">Capacity not applied yet.</p>
    {/if}
    {#if changed}
      <p class="text-xs text-warning mb-2">Budget changed. Reload capacity before saving.</p>
      <Button variant="outline" size="sm" disabled={saving} onclick={() => load()}
        >Reload capacity</Button
      >
    {/if}
    <form
      class="grid gap-3 mt-3"
      onsubmit={(event) => {
        event.preventDefault();
        void save();
      }}
    >
      <div>
        <Label for="capacity-egress">Egress budget (Mbit/s)</Label>
        <Input
          id="capacity-egress"
          type="text"
          inputmode="decimal"
          placeholder="Default: 1000"
          bind:value={egress}
          disabled={saving}
        />
      </div>
      <div>
        <Label for="capacity-ingress">Ingress budget (Mbit/s)</Label>
        <Input
          id="capacity-ingress"
          type="text"
          inputmode="decimal"
          placeholder="Default: 1000"
          bind:value={ingress}
          disabled={saving}
        />
      </div>
      <div class="flex flex-wrap gap-2">
        <Button type="submit" disabled={saving || changed}
          >{saving ? "Saving…" : "Save capacity"}</Button
        >
        <Button
          type="button"
          variant="ghost"
          disabled={saving || changed}
          onclick={() => {
            egress = "";
            ingress = "";
          }}>Use defaults</Button
        >
      </div>
    </form>
  {:else}
    <p class="text-sm text-muted-foreground">
      Capacity unavailable. Refresh when the control plane is reachable.
    </p>
  {/if}
</section>
