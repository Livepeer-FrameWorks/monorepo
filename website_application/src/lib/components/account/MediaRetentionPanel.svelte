<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { MediaRetentionPolicyStore, SetMediaRetentionPolicyStore } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import { toast } from "$lib/stores/toast";
  import { HardDrive } from "lucide-svelte";

  // Storage retention defaults: edits the tenant-wide DVR retention policy
  // that applies to every new finalized recording. Per-recording overrides
  // happen in the library.

  const policyStore = new MediaRetentionPolicyStore();
  const setPolicyMutation = new SetMediaRetentionPolicyStore();

  let saving = $state(false);
  let proposed = $state<number | null>(null);

  let policy = $derived($policyStore.data?.mediaRetentionPolicy ?? null);
  let bound = $derived(policy?.bounds.maxRecordingRetentionDays ?? 0);
  let effective = $derived(policy?.effectiveRecordingRetentionDays ?? 0);
  let tenantValue = $derived(policy?.recordingRetentionDays ?? null);
  let displayValue = $derived(proposed ?? tenantValue ?? effective);

  onMount(async () => {
    await policyStore.fetch();
    proposed = $policyStore.data?.mediaRetentionPolicy?.recordingRetentionDays ?? null;
  });

  async function save() {
    if (!proposed || proposed < 1) {
      toast.error("Retention must be at least 1 day");
      return;
    }
    if (bound > 0 && proposed > bound) {
      toast.error(`Retention cannot exceed your tier bound (${bound} days)`);
      return;
    }
    saving = true;
    try {
      const result = await setPolicyMutation.mutate({
        input: { recordingRetentionDays: proposed },
      });
      const data = result.data?.setMediaRetentionPolicy;
      if (data && "recordingRetentionDays" in data) {
        toast.success(`Retention default set to ${data.effectiveRecordingRetentionDays} days`);
        await policyStore.fetch({ policy: "NetworkOnly" });
      } else if (data && "message" in data) {
        toast.error(String(data.message));
      }
    } catch (err) {
      toast.error(`Failed to update retention: ${(err as Error).message}`);
    } finally {
      saving = false;
    }
  }

  async function resetToTier() {
    if (bound <= 0) {
      toast.error("Tier bound unknown");
      return;
    }
    proposed = bound;
    await save();
  }
</script>

<div class="slab">
  <div class="slab-header">
    <div class="flex items-center gap-2">
      <HardDrive class="w-4 h-4 text-primary" />
      <h3>Storage retention defaults</h3>
    </div>
  </div>
  <div class="slab-body--padded">
    <p class="text-sm text-muted-foreground mb-4">
      How long every new finalized DVR recording is kept before automatic deletion. Per-recording
      overrides happen in <a href={resolve("/library")} class="text-primary hover:underline"
        >your library</a
      >. Lowering this value reduces storage cost; raising it (up to your tier bound) keeps 24/7
      archives accessible longer.
    </p>

    {#if $policyStore.fetching && !policy}
      <p class="text-sm text-muted-foreground">Loading policy…</p>
    {:else if policy}
      <div class="space-y-4 max-w-md">
        <div class="grid grid-cols-2 gap-3 text-sm">
          <div>
            <div class="text-xs text-muted-foreground">Tenant default</div>
            <div class="font-mono">{tenantValue ?? "(unset — using tier)"}</div>
          </div>
          <div>
            <div class="text-xs text-muted-foreground">Tier bound</div>
            <div class="font-mono">{bound} days</div>
          </div>
          <div class="col-span-2">
            <div class="text-xs text-muted-foreground">Effective today</div>
            <div class="font-mono">{effective} days</div>
          </div>
        </div>

        <div class="flex items-end gap-2">
          <div class="flex-1">
            <Label for="retention-days" class="text-xs">Recording retention (days)</Label>
            <Input
              id="retention-days"
              type="number"
              min="1"
              max={bound > 0 ? bound : undefined}
              bind:value={proposed}
              disabled={saving}
            />
          </div>
          <Button onclick={save} disabled={saving || !proposed || proposed === tenantValue}>
            Save
          </Button>
          {#if tenantValue !== null}
            <Button variant="outline" onclick={resetToTier} disabled={saving}>Reset to tier</Button>
          {/if}
        </div>

        {#if displayValue !== effective}
          <p class="text-xs text-warning">
            Proposed value differs from effective — click <strong>Save</strong> to apply.
          </p>
        {/if}
      </div>
    {:else}
      <p class="text-sm text-destructive">Failed to load retention policy.</p>
    {/if}
  </div>
</div>
