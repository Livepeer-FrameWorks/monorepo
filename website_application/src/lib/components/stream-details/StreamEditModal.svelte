<script lang="ts">
  import { preventDefault } from "svelte/legacy";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Checkbox } from "$lib/components/ui/checkbox";
  import { Label } from "$lib/components/ui/label";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { getIconComponent } from "$lib/iconUtils";

  let { open = $bindable(false), stream, loading = false, onSave } = $props();

  // Per-stream retention overrides are an empty-string sentinel for
  // "inherit tenant default" so a typed 0 (keep forever) survives the
  // controlled-input round-trip.
  type OverrideField = number | "" | null;

  let formData = $state<{
    name: string;
    description: string;
    record: boolean;
    pullSourceUri: string;
    pullSourceEnabled: boolean;
    pullSourceAllowedClusterIds: string;
    pullSourceAllowedClustersDirty: boolean;
    dvrChapterMode: "WINDOW_SIZED" | "FIXED_INTERVAL" | "NONE";
    dvrChapterIntervalSeconds: string;
    dvrRetentionOverride: OverrideField;
    clipRetentionOverride: OverrideField;
  }>({
    name: "",
    description: "",
    record: false,
    pullSourceUri: "",
    pullSourceEnabled: true,
    pullSourceAllowedClusterIds: "",
    pullSourceAllowedClustersDirty: false,
    dvrChapterMode: "NONE",
    dvrChapterIntervalSeconds: "3600",
    dvrRetentionOverride: null,
    clipRetentionOverride: null,
  });

  // Compare against initial values to decide which mutations to fire on save.
  let initialDvrOverride = $state<OverrideField>(null);
  let initialClipOverride = $state<OverrideField>(null);

  // Sync form when stream changes — seed the allowed-clusters text field with
  // the existing pin so an enabled-toggle preserves placement on save.
  $effect(() => {
    if (stream) {
      const dvrOverride: OverrideField =
        stream.retentionOverrides?.dvrRetentionDaysOverride ?? null;
      const clipOverride: OverrideField =
        stream.retentionOverrides?.clipRetentionDaysOverride ?? null;
      formData = {
        name: stream.name || "",
        description: stream.description || "",
        record: stream.record || false,
        pullSourceUri: "",
        pullSourceEnabled: stream.pullSource?.enabled ?? true,
        pullSourceAllowedClusterIds: (stream.pullSource?.allowedClusterIds ?? []).join(", "),
        pullSourceAllowedClustersDirty: false,
        dvrChapterMode: (stream.dvrChapterMode ?? "NONE") as
          | "WINDOW_SIZED"
          | "FIXED_INTERVAL"
          | "NONE",
        dvrChapterIntervalSeconds: stream.dvrChapterIntervalSeconds
          ? String(stream.dvrChapterIntervalSeconds)
          : "3600",
        dvrRetentionOverride: dvrOverride,
        clipRetentionOverride: clipOverride,
      };
      initialDvrOverride = dvrOverride;
      initialClipOverride = clipOverride;
    }
  });

  function parseOverrideInput(raw: string): OverrideField {
    if (raw === "") return "";
    const n = Number(raw);
    if (!Number.isFinite(n) || n < 0) return null;
    return n;
  }

  function overrideChanged(current: OverrideField, initial: OverrideField): boolean {
    // "" represents an intent to clear; null represents "field never had an
    // override". They behave identically server-side but differ in user
    // intent (only "" triggers a clear RPC when there was a prior value).
    const norm = (v: OverrideField) => (v === "" || v === null ? null : v);
    return norm(current) !== norm(initial);
  }

  async function handleSubmit() {
    const interval =
      formData.dvrChapterMode === "FIXED_INTERVAL"
        ? Number(formData.dvrChapterIntervalSeconds)
        : null;

    // Pack the retention-override payload only when something changed; let
    // the page handler decide whether to fire setStreamRetentionOverrides.
    const dvrDirty = overrideChanged(formData.dvrRetentionOverride, initialDvrOverride);
    const clipDirty = overrideChanged(formData.clipRetentionOverride, initialClipOverride);
    const retentionPayload =
      dvrDirty || clipDirty
        ? {
            dvr: dvrDirty
              ? formData.dvrRetentionOverride === "" || formData.dvrRetentionOverride === null
                ? { clear: true as const }
                : { value: formData.dvrRetentionOverride }
              : undefined,
            clip: clipDirty
              ? formData.clipRetentionOverride === "" || formData.clipRetentionOverride === null
                ? { clear: true as const }
                : { value: formData.clipRetentionOverride }
              : undefined,
          }
        : undefined;

    await onSave?.({
      name: formData.name,
      description: formData.description,
      record: formData.record,
      pullSourceUri: formData.pullSourceUri,
      pullSourceEnabled: formData.pullSourceEnabled,
      pullSourceAllowedClusterIds: formData.pullSourceAllowedClusterIds,
      pullSourceAllowedClustersDirty: formData.pullSourceAllowedClustersDirty,
      dvrChapterMode: formData.dvrChapterMode === "NONE" ? null : formData.dvrChapterMode,
      dvrChapterIntervalSeconds: Number.isFinite(interval) && interval ? interval : null,
      retentionOverrides: retentionPayload,
    });
  }
</script>

<Dialog {open} onOpenChange={(value) => (open = value)}>
  <DialogContent
    class="max-w-md rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground"
        >Edit Stream</DialogTitle
      >
      <DialogDescription class="text-xs text-muted-foreground/70">
        Update the name, description, or recording preferences for this stream.
      </DialogDescription>
    </DialogHeader>

    <form
      id="edit-stream-form"
      onsubmit={preventDefault(handleSubmit)}
      class="slab-body--padded space-y-4"
    >
      <div class="space-y-2">
        <label for="editName" class="block text-sm font-medium text-foreground">
          Stream Name
        </label>
        <Input
          id="editName"
          type="text"
          bind:value={formData.name}
          required
          class="transition-all focus:ring-2 focus:ring-primary"
        />
      </div>

      <div class="space-y-2">
        <label for="editDescription" class="block text-sm font-medium text-foreground">
          Description
        </label>
        <Textarea
          id="editDescription"
          bind:value={formData.description}
          rows={3}
          class="transition-all focus:ring-2 focus:ring-primary"
        />
      </div>

      <div class="flex items-start space-x-2">
        <Checkbox id="editRecord" bind:checked={formData.record} />
        <Label for="editRecord" class="text-sm text-foreground">Enable Recording</Label>
      </div>

      {#if formData.record}
        <div class="space-y-2 border-l border-[hsl(var(--tn-fg-gutter)/0.3)] pl-3">
          <Label for="editChapterMode" class="block text-sm font-medium text-foreground">
            Chapter mode
          </Label>
          <select
            id="editChapterMode"
            bind:value={formData.dvrChapterMode}
            class="w-full rounded-none border border-input bg-background px-3 py-2 text-sm focus:ring-2 focus:ring-primary"
          >
            <option value="NONE">Off</option>
            <option value="WINDOW_SIZED">Window-sized (DVR window)</option>
            <option value="FIXED_INTERVAL">Fixed interval (≥ 1 hour)</option>
          </select>
          {#if formData.dvrChapterMode === "FIXED_INTERVAL"}
            <Label for="editChapterInterval" class="block text-sm font-medium text-foreground">
              Interval seconds (≥ 3600)
            </Label>
            <Input
              id="editChapterInterval"
              type="number"
              min="3600"
              step="3600"
              bind:value={formData.dvrChapterIntervalSeconds}
            />
          {/if}
          <p class="text-xs text-muted-foreground">
            Chapter mode is snapshotted onto the recording at StartDVR. Changes apply to the next
            recording, not in-flight ones.
          </p>
        </div>
      {/if}

      <div class="space-y-2 border-l border-[hsl(var(--tn-fg-gutter)/0.3)] pl-3">
        <div class="text-sm font-medium text-foreground">Retention overrides</div>
        <p class="text-xs text-muted-foreground">
          Override the tenant DVR / clip retention defaults for artifacts from this stream. Leave
          empty to inherit. 0 = keep forever (paid tiers only; Free clamps to its cap).
        </p>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <Label for="editDvrRetention" class="text-xs">DVR retention (days)</Label>
            <Input
              id="editDvrRetention"
              type="number"
              min="0"
              value={formData.dvrRetentionOverride ?? ""}
              oninput={(e) =>
                (formData.dvrRetentionOverride = parseOverrideInput(
                  (e.target as HTMLInputElement).value
                ))}
              placeholder="inherit"
            />
          </div>
          <div>
            <Label for="editClipRetention" class="text-xs">Clip retention (days)</Label>
            <Input
              id="editClipRetention"
              type="number"
              min="0"
              value={formData.clipRetentionOverride ?? ""}
              oninput={(e) =>
                (formData.clipRetentionOverride = parseOverrideInput(
                  (e.target as HTMLInputElement).value
                ))}
              placeholder="inherit"
            />
          </div>
        </div>
      </div>

      {#if stream?.ingestMode === "PULL"}
        <div class="space-y-2">
          <label for="editPullSource" class="block text-sm font-medium text-foreground">
            Replace Pull Source URI
          </label>
          <Input
            id="editPullSource"
            type="text"
            bind:value={formData.pullSourceUri}
            placeholder={stream.pullSource?.sourceUriRedacted ?? "rtsp://camera.example.net/live"}
            class="font-mono text-xs transition-all focus:ring-2 focus:ring-primary"
          />
        </div>

        <div class="flex items-start space-x-2">
          <Checkbox id="editPullEnabled" bind:checked={formData.pullSourceEnabled} />
          <Label for="editPullEnabled" class="text-sm text-foreground">Enable Pull Source</Label>
        </div>

        <div class="space-y-2">
          <label for="editPullAllowedClusters" class="block text-sm font-medium text-foreground">
            Allowed clusters
          </label>
          <Input
            id="editPullAllowedClusters"
            type="text"
            bind:value={formData.pullSourceAllowedClusterIds}
            oninput={() => (formData.pullSourceAllowedClustersDirty = true)}
            placeholder="warehouse-edge, eu-west-edge"
            class="font-mono text-xs transition-all focus:ring-2 focus:ring-primary"
          />
          <p class="text-xs text-muted-foreground">
            Comma-separated cluster IDs pinning this pull source. Leave the field as it loaded to
            preserve the current pin; clear it to allow any media cluster (public sources only).
          </p>
        </div>
      {/if}
    </form>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
        onclick={() => (open = false)}
      >
        Cancel
      </Button>
      <Button
        type="submit"
        variant="ghost"
        disabled={loading}
        class="rounded-none h-12 flex-1 hover:bg-muted/10 text-primary hover:text-primary/80 gap-2"
        form="edit-stream-form"
      >
        {#if loading}
          {@const SvelteComponent = getIconComponent("Loader")}
          <SvelteComponent class="w-4 h-4 animate-spin" />
        {/if}
        Save Changes
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
