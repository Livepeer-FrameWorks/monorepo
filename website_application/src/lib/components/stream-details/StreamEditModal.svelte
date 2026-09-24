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
  import { getRecordingRetentionMaxDays } from "$lib/stores/capabilities.svelte";
  import SourceLocationControl from "./SourceLocationControl.svelte";
  import SourceLocationSummary from "./SourceLocationSummary.svelte";
  import {
    anySourceLocation,
    draftFromSourceLocation,
    sameSourceLocation,
    sourceLocationInput,
    sourceLocationProblem,
    type SourceLocationClusterChoice,
    type SourceLocationDraft,
    type SourceLocationValue,
  } from "$lib/source-location";
  import { untrack } from "svelte";

  type ChapterMode = "WINDOW_SIZED" | "FIXED_INTERVAL" | "NONE";

  interface EditableStream {
    id?: string | null;
    name?: string | null;
    description?: string | null;
    record?: boolean | null;
    ingestMode?: "PUSH" | "PULL" | "MANAGED" | string | null;
    pullSource?: {
      sourceUriRedacted?: string | null;
      enabled?: boolean | null;
      class?: string | null;
    } | null;
    sourceLocation?: SourceLocationValue | null;
    dvrChapterMode?: ChapterMode | null;
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
    /** Present only when the user changed the source location. */
    sourceLocation?: SourceLocationDraft;
    dvrChapterMode: ChapterMode;
    /** Set only for FIXED_INTERVAL; 0 clears a stored interval. */
    dvrChapterIntervalSeconds: number;
    retentionOverrides?: {
      dvr?: { clear: true } | { value: number };
      clip?: { clear: true } | { value: number };
    };
  }

  let {
    open = $bindable(false),
    stream,
    clusterOptions = [],
    placementHref,
    loading = false,
    onSave,
  }: {
    open: boolean;
    stream: EditableStream | null;
    clusterOptions?: SourceLocationClusterChoice[];
    placementHref?: string;
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
    sourceLocation: SourceLocationDraft;
    dvrChapterMode: ChapterMode;
    dvrChapterIntervalHours: string;
    dvrRetentionOverride: OverrideField;
    clipRetentionOverride: OverrideField;
  }>({
    name: "",
    description: "",
    record: false,
    pullSourceUri: "",
    pullSourceEnabled: true,
    sourceLocation: anySourceLocation(),
    dvrChapterMode: "WINDOW_SIZED",
    dvrChapterIntervalHours: "1",
    dvrRetentionOverride: null,
    clipRetentionOverride: null,
  });
  // The "live rewind only" choice lives under an advanced disclosure; it
  // starts open when the stream already keeps nothing.
  let advancedRecordingOpen = $state(false);

  // The tier's upper bound on retention. Null means uncapped or not yet read;
  // the server clamps either way.
  let retentionMaxDays = $derived(getRecordingRetentionMaxDays());

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
  // Null when the saved location is CUSTOM; only the placement editor changes it.
  const savedSourceLocation = $derived(draftFromSourceLocation(stream?.sourceLocation));
  const sourceLocationChanged = $derived(
    !!savedSourceLocation && !sameSourceLocation(formData.sourceLocation, savedSourceLocation)
  );
  // A saved location is only re-validated when the user touches it or the URI,
  // so an unrelated edit is never blocked by the server's current state.
  const sourceLocationBlocked = $derived(
    stream?.ingestMode === "PULL" &&
      !!savedSourceLocation &&
      (sourceLocationChanged || !!formData.pullSourceUri.trim()) &&
      !!sourceLocationProblem(formData.sourceLocation, pullSourceClass, clusterOptions)
  );

  // The form is seeded when the modal opens (or switches to another stream),
  // not on every stream emission: the page refreshes the stream on a poll and
  // on subscriptions, and re-seeding then would discard the user's edits.
  const streamKey = $derived(stream?.id ?? null);
  $effect(() => {
    if (!open) return;
    void streamKey;
    untrack(() => {
      if (stream) seedForm(stream);
    });
  });

  function seedForm(source: EditableStream) {
    const dvrOverride: OverrideField = source.retentionOverrides?.dvrRetentionDaysOverride ?? null;
    const clipOverride: OverrideField =
      source.retentionOverrides?.clipRetentionDaysOverride ?? null;
    const mode: ChapterMode = source.dvrChapterMode ?? "NONE";
    const intervalSeconds = source.dvrChapterIntervalSeconds ?? 0;
    formData = {
      name: source.name || "",
      description: source.description || "",
      record: source.record || false,
      pullSourceUri: "",
      pullSourceEnabled: source.pullSource?.enabled ?? true,
      sourceLocation: draftFromSourceLocation(source.sourceLocation) ?? anySourceLocation(),
      dvrChapterMode: mode,
      dvrChapterIntervalHours:
        intervalSeconds >= 3600 ? String(Math.round(intervalSeconds / 3600)) : "1",
      dvrRetentionOverride: dvrOverride,
      clipRetentionOverride: clipOverride,
    };
    advancedRecordingOpen = mode === "NONE";
    initialDvrOverride = dvrOverride;
    initialClipOverride = clipOverride;
  }

  function setLiveRewindOnly(checked: boolean) {
    formData.dvrChapterMode = checked ? "NONE" : "WINDOW_SIZED";
  }

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
    const hours = Math.floor(Number(formData.dvrChapterIntervalHours));
    const interval =
      formData.dvrChapterMode === "FIXED_INTERVAL" && Number.isFinite(hours) && hours >= 1
        ? hours * 3600
        : 0;

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
      sourceLocation: sourceLocationChanged
        ? sourceLocationInput(formData.sourceLocation)
        : undefined,
      dvrChapterMode: formData.dvrChapterMode,
      dvrChapterIntervalSeconds: interval,
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

      <div class="space-y-1">
        <div class="flex items-start space-x-2">
          <Checkbox id="editRecord" bind:checked={formData.record} />
          <Label for="editRecord" class="text-sm text-foreground">Record broadcasts</Label>
        </div>
        <p class="pl-6 text-xs text-muted-foreground">
          Saves every broadcast so it can be replayed after it ends. While live, viewers can rewind
          within the live rewind window.
        </p>
      </div>

      {#if formData.record}
        <div class="space-y-2 border-l border-[hsl(var(--tn-fg-gutter)/0.3)] pl-3">
          {#if formData.dvrChapterMode !== "NONE"}
            <Label for="editChapterMode" class="block text-sm font-medium text-foreground">
              Split saved recordings into
            </Label>
            <select
              id="editChapterMode"
              bind:value={formData.dvrChapterMode}
              class="w-full rounded-none border border-input bg-background px-3 py-2 text-sm focus:ring-2 focus:ring-primary"
            >
              <option value="WINDOW_SIZED">
                Parts the length of the live rewind window, from broadcast start
              </option>
              <option value="FIXED_INTERVAL">Fixed clock-aligned parts every N hours (UTC)</option>
            </select>
            {#if formData.dvrChapterMode === "FIXED_INTERVAL"}
              <Label for="editChapterInterval" class="block text-sm font-medium text-foreground">
                Hours per part
              </Label>
              <Input
                id="editChapterInterval"
                type="number"
                min="1"
                step="1"
                required
                bind:value={formData.dvrChapterIntervalHours}
              />
            {/if}
          {/if}
          <details bind:open={advancedRecordingOpen} class="text-sm">
            <summary class="cursor-pointer text-xs text-muted-foreground">Advanced</summary>
            <div class="mt-2 flex items-start space-x-2">
              <Checkbox
                id="editLiveRewindOnly"
                checked={formData.dvrChapterMode === "NONE"}
                onCheckedChange={(checked) => setLiveRewindOnly(checked === true)}
              />
              <Label for="editLiveRewindOnly" class="text-sm text-foreground">
                Don't save — live rewind only (nothing is kept once it leaves the live rewind
                window)
              </Label>
            </div>
          </details>
          <p class="text-xs text-muted-foreground">
            Applies from the next broadcast; a recording in progress keeps its setting.
          </p>
        </div>
      {/if}

      <div class="space-y-2 border-l border-[hsl(var(--tn-fg-gutter)/0.3)] pl-3">
        <div class="text-sm font-medium text-foreground">Retention overrides</div>
        <p class="text-xs text-muted-foreground">
          Override the tenant DVR / clip retention defaults for artifacts from this stream. Leave
          empty to inherit.
          {#if retentionMaxDays !== null}
            Your tier caps retention at {retentionMaxDays} days; longer values are clamped.
          {:else}
            0 = keep forever (paid tiers only; Free clamps to its cap).
          {/if}
        </p>
        <div class="grid grid-cols-2 gap-3">
          <div>
            <Label for="editDvrRetention" class="text-xs">DVR retention (days)</Label>
            <Input
              id="editDvrRetention"
              type="number"
              min="0"
              max={retentionMaxDays ?? undefined}
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
              max={retentionMaxDays ?? undefined}
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

        {#if savedSourceLocation}
          <SourceLocationControl
            value={formData.sourceLocation}
            clusters={clusterOptions}
            sourceClass={pullSourceClass}
            onchange={(next) => (formData.sourceLocation = next)}
          />
        {:else}
          <div class="space-y-2">
            <p class="text-sm font-medium text-foreground">Where can this source be reached?</p>
            <SourceLocationSummary location={stream.sourceLocation} {placementHref} />
          </div>
        {/if}
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
        disabled={loading || sourceLocationBlocked}
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
