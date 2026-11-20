<script lang="ts">
  import { onMount } from "svelte";
  import { TranscodeLadderPolicyStore, SetTranscodeLadderPolicyStore } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Label } from "$lib/components/ui/label";
  import { toast } from "$lib/stores/toast";
  import { Layers } from "lucide-svelte";
  import { ladderSummary } from "./ladders";

  // The ladder the tenant's streams and VOD uploads inherit. A stream's own
  // ladder, set in its settings, takes precedence.
  const policyStore = new TranscodeLadderPolicyStore();
  const setPolicy = new SetTranscodeLadderPolicyStore();

  let policy = $derived($policyStore.data?.transcodeLadderPolicy ?? null);
  let choice = $state("");
  let saving = $state(false);
  let planDefault = $derived(policy?.ladders.find((ladder) => ladder.name === policy?.planDefault));

  $effect(() => {
    if (policy) choice = policy.tenantLadder ?? "";
  });

  // A failed load leaves policy null, which the slab reports.
  onMount(() => {
    policyStore.fetch().catch(() => {});
  });

  async function save() {
    saving = true;
    try {
      const result = await setPolicy.mutate({ input: { ladder: choice } });
      const data = result.data?.setTranscodeLadderPolicy;
      if (data && "effectiveLadder" in data) {
        toast.success("Default transcode ladder updated");
        await policyStore.fetch({ policy: "NetworkOnly" });
      } else if (data && "message" in data) {
        toast.error(String(data.message));
      }
    } catch (err) {
      toast.error(`Save failed: ${(err as Error).message}`);
    } finally {
      saving = false;
    }
  }
</script>

<div class="slab col-span-full">
  <div class="slab-header">
    <div class="flex items-center gap-2">
      <Layers class="w-4 h-4 text-primary" />
      <h3>Default transcode ladder</h3>
    </div>
  </div>
  <div class="slab-body--padded">
    {#if $policyStore.fetching && !policy}
      <p class="text-sm text-muted-foreground">Loading ladders…</p>
    {:else if policy && policy.ladders.length > 0}
      <div class="space-y-3 max-w-2xl">
        <p class="text-sm text-muted-foreground">
          The renditions your streams and VOD uploads get unless a stream picks its own. Changes
          apply to live streams from their next broadcast and to later uploads.
        </p>
        <Label for="tenantTranscodeLadder" class="text-xs">Ladder</Label>
        <div class="flex gap-2">
          <select
            id="tenantTranscodeLadder"
            bind:value={choice}
            disabled={saving}
            class="w-full rounded-none border border-input bg-background px-3 py-2 text-sm"
          >
            <option value="">Plan default ({planDefault?.displayName ?? policy.planDefault})</option
            >
            {#each policy.ladders as ladder (ladder.name)}
              <option value={ladder.name}>{ladder.displayName}</option>
            {/each}
          </select>
          <Button onclick={save} disabled={saving || choice === (policy.tenantLadder ?? "")}>
            {saving ? "Saving…" : "Save"}
          </Button>
        </div>
        <ul class="space-y-1 text-xs text-muted-foreground">
          {#each policy.ladders as ladder (ladder.name)}
            <li>
              <span class="text-foreground">{ladder.displayName}</span>: {ladderSummary(
                ladder.renditionNames
              )}
            </li>
          {/each}
        </ul>
      </div>
    {:else if policy}
      <p class="text-sm text-muted-foreground">
        Your plan has no transcode ladders to choose from.
      </p>
    {:else}
      <p class="text-sm text-destructive">Failed to load transcode ladders.</p>
    {/if}
  </div>
</div>
