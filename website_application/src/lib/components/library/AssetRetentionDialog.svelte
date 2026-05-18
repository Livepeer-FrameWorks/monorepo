<script lang="ts">
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Label } from "$lib/components/ui/label";
  import {
    UpdateMediaRetentionStore,
    ResetMediaRetentionOverrideStore,
    type MediaRetentionTarget$options,
  } from "$houdini";
  import { toast } from "$lib/stores/toast";
  import { AlertTriangle, RotateCcw, Save } from "lucide-svelte";

  // Per-asset retention editor used by the library page for DVR / clip / VOD
  // rows. Wraps updateMediaRetention (set days/until) and
  // resetMediaRetentionOverride (clear override → fall back to cascade).
  //
  // Cost-affecting: shortening retention schedules the asset for deletion at
  // the new horizon. The Save button copy spells this out; reset goes back
  // to whatever the tenant default / tier entitlement would resolve to.

  interface Props {
    open: boolean;
    assetType: MediaRetentionTarget$options;
    assetId: string;
    assetName?: string;
    currentExpiresAt?: string | null;
    onClose: () => void;
    onSaved?: () => void | Promise<void>;
  }

  let {
    open = $bindable(),
    assetType,
    assetId,
    assetName = "",
    currentExpiresAt = null,
    onClose,
    onSaved,
  }: Props = $props();

  const updateMutation = new UpdateMediaRetentionStore();
  const resetMutation = new ResetMediaRetentionOverrideStore();

  let proposedDays = $state<number | null>(null);
  let saving = $state(false);

  $effect(() => {
    if (open) {
      // When the dialog opens, seed the input from the current horizon so the
      // operator can adjust relative to where it is, not from blank.
      proposedDays = remainingDays(currentExpiresAt);
    }
  });

  function remainingDays(iso: string | null | undefined): number | null {
    if (!iso) return null;
    const ms = new Date(iso).getTime() - Date.now();
    if (Number.isNaN(ms) || ms <= 0) return 1;
    return Math.max(1, Math.ceil(ms / (24 * 60 * 60 * 1000)));
  }

  function close() {
    if (saving) return;
    open = false;
    onClose();
  }

  async function save(keepForever = false) {
    const days = keepForever ? 0 : proposedDays;
    if (days === null || days === undefined || days < 0) {
      toast.error("Retention must be 0 (keep forever) or a positive day count");
      return;
    }
    saving = true;
    try {
      const result = await updateMutation.mutate({
        input: {
          targetType: assetType,
          targetId: assetId,
          retentionDays: days,
        },
      });
      const data = result.data?.updateMediaRetention;
      switch (data?.__typename) {
        case "EffectiveRetention":
          toast.success(
            data.retentionDays === 0
              ? "Retention set to keep forever"
              : `Retention set to ${data.retentionDays} days`
          );
          await onSaved?.();
          close();
          break;
        case "ValidationError":
          toast.error(`${data.message}${data.field ? ` (${data.field})` : ""}`);
          break;
        case "NotFoundError":
          toast.error(data.message ?? "Asset not found");
          break;
        case "AuthError":
          toast.error(data.message ?? "Not authorised");
          break;
        default:
          toast.error("No response");
      }
    } catch (err) {
      toast.error(`Update failed: ${(err as Error).message}`);
    } finally {
      saving = false;
    }
  }

  async function resetToDefault() {
    saving = true;
    try {
      const result = await resetMutation.mutate({
        input: { targetType: assetType, targetId: assetId },
      });
      const data = result.data?.resetMediaRetentionOverride;
      switch (data?.__typename) {
        case "EffectiveRetention":
          toast.success(`Reset to ${data.retentionDays} days (${data.source.toLowerCase()})`);
          await onSaved?.();
          close();
          break;
        case "ValidationError":
          toast.error(`${data.message}${data.field ? ` (${data.field})` : ""}`);
          break;
        case "NotFoundError":
          toast.error(data.message ?? "Asset not found");
          break;
        case "AuthError":
          toast.error(data.message ?? "Not authorised");
          break;
      }
    } catch (err) {
      toast.error(`Reset failed: ${(err as Error).message}`);
    } finally {
      saving = false;
    }
  }
</script>

<Dialog bind:open onOpenChange={(o) => (o ? null : close())}>
  <DialogContent class="max-w-md">
    <DialogHeader>
      <DialogTitle>Edit retention</DialogTitle>
      <DialogDescription>
        {assetName || assetId}
      </DialogDescription>
    </DialogHeader>

    <div class="space-y-3">
      {#if currentExpiresAt}
        <div class="text-xs text-muted-foreground">
          Current horizon:
          <code class="font-mono">{new Date(currentExpiresAt).toLocaleString()}</code>
        </div>
      {/if}
      <div>
        <Label for="retention-days" class="text-xs">Keep this asset for (days)</Label>
        <Input
          id="retention-days"
          type="number"
          min="0"
          bind:value={proposedDays}
          disabled={saving}
        />
        <div class="text-xs text-muted-foreground mt-1">
          0 = keep forever (paid tiers only; Free clamps to the tier cap).
        </div>
      </div>
      <div class="border border-warning/30 bg-warning/5 rounded p-2 text-xs flex items-start gap-2">
        <AlertTriangle class="w-3.5 h-3.5 text-warning shrink-0 mt-0.5" />
        <div>
          Lowering retention schedules the asset for deletion at the new horizon. The cleanup job
          runs hourly.
        </div>
      </div>
    </div>

    <DialogFooter class="gap-2">
      <Button variant="outline" onclick={resetToDefault} disabled={saving}>
        <RotateCcw class="w-4 h-4 mr-1" /> Use default
      </Button>
      <Button variant="outline" onclick={() => save(true)} disabled={saving}>Keep forever</Button>
      <Button onclick={() => save(false)} disabled={saving || proposedDays === null}>
        <Save class="w-4 h-4 mr-1" /> Save
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
