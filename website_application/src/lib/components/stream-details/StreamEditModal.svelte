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
  import { pullSourcePlacementClass } from "$lib/utils/pull-source";
  import PullSourceClusterPicker from "./PullSourceClusterPicker.svelte";

  interface EditableStream {
    name?: string | null;
    description?: string | null;
    record?: boolean | null;
    ingestMode?: "PUSH" | "PULL" | "MANAGED" | string | null;
    pullSource?: {
      sourceUriRedacted?: string | null;
      enabled?: boolean | null;
      class?: string | null;
      allowedClusterIds?: string[] | null;
    } | null;
    dvrChapterMode?: "WINDOW_SIZED" | "FIXED_INTERVAL" | "NONE" | null;
    dvrChapterIntervalSeconds?: number | null;
    retentionOverrides?: {
      dvrRetentionDaysOverride?: number | null;
      clipRetentionDaysOverride?: number | null;
    } | null;
  }

  interface EditResult {
    name: string;
    description: string;
    record: boolean;
    pullSourceUri: string;
    pullSourceEnabled: boolean;
    pullSourceAllowedClusterIds: string;
    pullSourceAllowedClustersDirty: boolean;
    dvrChapterMode: "WINDOW_SIZED" | "FIXED_INTERVAL" | null;
    dvrChapterIntervalSeconds: number | null;
    retentionOverrides?: {
      dvr?: { clear: true } | { value: number };
      clip?: { clear: true } | { value: number };
    };
  }

  let {
    open = $bindable(false),
    stream,
    clusterOptions = [],
    loading = false,
    onSave,
  }: {
    open: boolean;
    stream: EditableStream | null;
    clusterOptions?: Array<{ clusterId: string; clusterName: string }>;
    loading?: boolean;
    onSave?: (value: EditResult) => Promise<void> | void;
  } = $props();

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
  const pullSourceClass = $derived(
    formData.pullSourceUri.trim()
      ? pullSourcePlacementClass(formData.pullSourceUri)
      : stream?.pullSource?.class === "private"
        ? "private"
        : "public"
  );
  const pullSourcePinsRequired = $derived(
    stream?.ingestMode === "PULL" && pullSourceClass === "private"
  );
  const pullSourcePinsMissing = $derived(
    pullSourcePinsRequired && formData.pullSourceAllowedClusterIds.trim() === ""
  );

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
    class="max-w-md max-h-[calc(100vh-2rem)] rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-y-auto"
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
            Historical chapters
          </Label>
          <select
            id="editChapterMode"
            bind:value={formData.dvrChapterMode}
            class="w-full rounded-none border border-input bg-background px-3 py-2 text-sm focus:ring-2 focus:ring-primary"
          >
            <option value="NONE">Off — rolling DVR only</option>
            <option value="WINDOW_SIZED">Window-sized archive chapters</option>
            <option value="FIXED_INTERVAL">Fixed archive interval (≥ 1 hour)</option>
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
            Off still records the rolling DVR window for live time-shift playback. Historical
            chapters create finalized VOD artifacts for replay after media rolls out of that window.
            This setting is snapshotted at StartDVR; changes apply to the next recording.
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
          <PullSourceClusterPicker
            bind:selectedIds={formData.pullSourceAllowedClusterIds}
            options={clusterOptions}
            required={pullSourcePinsRequired}
            onchange={() => (formData.pullSourceAllowedClustersDirty = true)}
          />
          <p class="text-xs text-muted-foreground">
            {#if pullSourcePinsRequired}
              Private and multicast sources must be pinned to clusters that can reach them and
              explicitly allow private pull sources.
            {:else}
              Select the clusters that may open this source, or use automatic placement for a public
              source.
            {/if}
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
        disabled={loading || pullSourcePinsMissing}
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
