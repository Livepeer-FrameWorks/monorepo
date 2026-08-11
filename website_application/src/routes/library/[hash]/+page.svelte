<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { GetStorageArtifactsConnectionStore, type MediaRetentionTarget$options } from "$houdini";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { Badge } from "$lib/components/ui/badge";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";
  import type { ContentType } from "$lib/config";
  import AssetRetentionDialog from "$lib/components/library/AssetRetentionDialog.svelte";
  import { formatBytes } from "$lib/utils/formatters";
  import { formatDate } from "$lib/utils/stream-helpers";

  const artifactsStore = new GetStorageArtifactsConnectionStore();

  let hash = $derived(page.params.hash as string);
  let loading = $state(true);
  let loadError = $state(false);
  let showRetentionDialog = $state(false);

  type ArtifactNode = NonNullable<ReturnType<typeof pickNodes>>[number];

  function pickNodes() {
    return $artifactsStore.data?.storageArtifactsConnection?.nodes ?? [];
  }
  // Resolve the single artifact via the exact tenant-scoped hash lookup.
  let artifact = $derived<ArtifactNode | null>(pickNodes().find((n) => n.hash === hash) ?? null);
  // False when the lifecycle backend was unavailable: the flags are unknown, not false,
  // so the tier/lifecycle presentation must show "unknown", not inactive/not-synced.
  let lifecycleAvailable = $derived(
    $artifactsStore.data?.storageArtifactsConnection?.lifecycleAvailable ?? true
  );

  const FilmIcon = getIconComponent("Film");
  const ArrowLeftIcon = getIconComponent("ArrowLeft");
  const BarChart2Icon = getIconComponent("BarChart2");
  const HardDriveIcon = getIconComponent("HardDrive");
  const ClockIcon = getIconComponent("Clock");
  const SnowflakeIcon = getIconComponent("Snowflake");

  // StorageArtifactKind → playback content type + display label + retention target.
  function contentTypeFor(kind: string): ContentType {
    if (kind === "CLIP") return "clip";
    if (kind === "DVR") return "dvr";
    return "vod"; // VOD + CHAPTER play back as VOD
  }
  function kindLabel(kind: string): string {
    switch (kind) {
      case "VOD":
        return "VOD";
      case "DVR":
        return "Recording";
      case "CHAPTER":
        return "Chapter";
      case "CLIP":
        return "Clip";
      default:
        return kind;
    }
  }
  function retentionTargetFor(kind: string): MediaRetentionTarget$options | null {
    if (kind === "VOD" || kind === "CHAPTER") return "VOD";
    if (kind === "DVR") return "DVR";
    if (kind === "CLIP") return "CLIP";
    return null;
  }
  // A chapter's retention is governed by its parent DVR, not per-asset — the backend
  // rejects per-chapter retention writes, so the editor is only offered for other kinds.
  function canEditRetention(kind: string): boolean {
    return kind !== "CHAPTER" && retentionTargetFor(kind) !== null;
  }

  // Durable (S3) storage and local node availability are INDEPENDENT facts that can coexist —
  // an asset can be synced to S3 AND have a local node copy at the same time. Render them separately
  // rather than collapsing to a single mutually-exclusive tier. isSynced/isFinalized are DURABLE
  // catalog facts (Commodore, reconciler-repaired). hasLocalCopy is NOT durable: it is a
  // PLACEMENT-derived Periscope overlay (present full local node copy, origin or cache) and is
  // nullable — null means the placement overlay is unavailable (unknown), never "no local copy".
  let durableState = $derived.by(() => {
    if (!artifact) return null;
    if (!lifecycleAvailable)
      return { label: "Unknown", tone: "text-muted-foreground", frozen: false };
    // Durable anchor is isSynced. S3-only ("Frozen · S3", read-through relay) is claimed ONLY when
    // sync is confirmed AND the placement overlay affirmatively reports zero warm copies
    // (hasLocalCopy === false); an unknown/absent overlay (null) stays at "Synced · S3".
    if (artifact.isSynced)
      return artifact.hasLocalCopy === false
        ? { label: "Frozen · S3", tone: "text-info", frozen: true }
        : { label: "Synced · S3", tone: "text-success", frozen: false };
    return { label: "Not yet durable", tone: "text-muted-foreground", frozen: false };
  });
  let localState = $derived.by(() => {
    if (!artifact) return null;
    // hasLocalCopy is an INDEPENDENT live Periscope overlay, not part of the durable sync lifecycle.
    // null means the overlay is unavailable (unknown) — never claim "No local copy" from that.
    if (artifact.hasLocalCopy == null) return { label: "Unknown", tone: "text-muted-foreground" };
    return artifact.hasLocalCopy
      ? { label: "Local copy present", tone: "text-warning" }
      : { label: "No local copy", tone: "text-muted-foreground" };
  });
  let storageNote = $derived.by(() => {
    if (!artifact) return "";
    if (!lifecycleAvailable) return "Storage lifecycle temporarily unavailable";
    // S3-only (read-through relay) is only claimed when sync is confirmed AND no warm copy is observed.
    if (artifact.isSynced && artifact.hasLocalCopy === false)
      return "Durable in S3; served via read-through relay";
    if (artifact.isSynced)
      return (
        "Durable in object storage" + (artifact.hasLocalCopy ? "; also present on a node" : "")
      );
    return "";
  });

  function formatTrackDuration(seconds: number | null | undefined): string {
    if (!seconds || seconds <= 0) return "";
    const s = Math.round(seconds);
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    const sec = s % 60;
    return h > 0
      ? `${h}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}`
      : `${m}:${String(sec).padStart(2, "0")}`;
  }

  // Compact one-line summary of a projected A/V track (codec + geometry/rates).
  function trackSummary(t: {
    type: string;
    codec: string;
    resolution?: number | string | null;
    fps?: number | null;
    channels?: number | null;
    sampleRate?: number | null;
    bitrateKbps?: number | null;
  }): string {
    const parts: string[] = [];
    if (t.codec) parts.push(t.codec);
    if (t.resolution) parts.push(String(t.resolution));
    if (t.fps) parts.push(`${Math.round(Number(t.fps))}fps`);
    if (t.channels) parts.push(`${t.channels}ch`);
    if (t.sampleRate) parts.push(`${Math.round(Number(t.sampleRate) / 1000)}kHz`);
    if (t.bitrateKbps) parts.push(`${t.bitrateKbps}kbps`);
    return parts.join(" · ");
  }

  // Lifecycle checklist shown as pills. Finalized/Synced are DURABLE catalog facts. Local copy is the
  // live Periscope placement overlay — nullable and shown ONLY when known, so an unavailable overlay
  // never renders as an inactive pill (and null is never coerced to false).
  let lifecycle = $derived.by(() => {
    if (!artifact) return [];
    const pills: { label: string; on: boolean }[] = [];
    // Finalized/Synced are durable catalog facts, gated on durable availability.
    if (lifecycleAvailable) {
      pills.push({ label: "Finalized", on: !!artifact.isFinalized });
      pills.push({ label: "Synced to S3", on: !!artifact.isSynced });
    }
    // Local copy is the independent live Periscope placement overlay: shown only when known (null =
    // overlay unavailable), so an unavailable overlay never renders as an inactive pill. The S3-only
    // ("Frozen") state is the durableState label above (isSynced && hasLocalCopy === false), not a
    // separate pill.
    if (artifact.hasLocalCopy != null) {
      pills.push({ label: "Local copy", on: artifact.hasLocalCopy });
    }
    return pills;
  });

  async function load() {
    loading = true;
    loadError = false;
    try {
      const result = await artifactsStore.fetch({
        variables: { input: { artifactHash: hash, first: 1 } },
      });
      // Houdini may resolve with an in-band errors array — a failed request must not
      // masquerade as "asset not found".
      const errs = (result as { errors?: unknown[] })?.errors;
      if (Array.isArray(errs) && errs.length > 0) {
        loadError = true;
      }
    } catch (error) {
      console.error("Failed to load asset:", error);
      loadError = true;
    } finally {
      loading = false;
    }
  }

  onMount(load);
</script>

<svelte:head>
  <title>{artifact?.title ?? "Asset"} - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <div
    class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 flex justify-between items-center gap-4"
  >
    <div class="flex items-center gap-3 min-w-0">
      <Button
        variant="ghost"
        size="icon"
        onclick={() => goto(resolve("/library"))}
        title="Back to library"
      >
        <ArrowLeftIcon class="w-4 h-4" />
      </Button>
      <FilmIcon class="w-5 h-5 text-primary shrink-0" />
      <div class="min-w-0">
        <div class="flex items-center gap-2">
          <h1 class="text-xl font-bold text-foreground truncate">
            {artifact?.title ?? (loading ? "Loading…" : "Asset")}
          </h1>
          {#if artifact}
            <Badge variant="outline" class="text-[10px] shrink-0">{kindLabel(artifact.kind)}</Badge>
          {/if}
        </div>
        {#if artifact?.streamTitle}
          <p class="text-sm text-muted-foreground truncate">from {artifact.streamTitle}</p>
        {/if}
      </div>
    </div>
    {#if artifact}
      <Button
        variant="ghost"
        size="sm"
        class="gap-2 shrink-0"
        onclick={() => goto(resolve(`/library/${hash}/analytics`))}
      >
        <BarChart2Icon class="w-4 h-4" />
        Analytics
      </Button>
    {/if}
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if loading}
      <div class="flex items-center justify-center min-h-64">
        <div class="loading-spinner w-8 h-8"></div>
      </div>
    {:else if loadError}
      <div class="flex flex-col items-center justify-center min-h-64 gap-3 text-center">
        <p class="text-sm text-destructive">Couldn't load this asset.</p>
        <Button variant="outline" size="sm" onclick={() => load()}>Retry</Button>
      </div>
    {:else if !artifact}
      <div class="flex flex-col items-center justify-center min-h-64 gap-3 text-center">
        <p class="text-sm text-muted-foreground">Asset not found.</p>
        <Button variant="outline" size="sm" onclick={() => goto(resolve("/library"))}>
          Back to library
        </Button>
      </div>
    {:else}
      <div class="dashboard-grid">
        <!-- Playback -->
        {#if artifact.playbackId}
          <div class="slab col-span-full">
            <div class="slab-header">
              <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
                Playback
              </h3>
            </div>
            <div class="slab-body--padded">
              <PlaybackProtocols
                contentId={artifact.playbackId}
                contentType={contentTypeFor(artifact.kind)}
              />
            </div>
          </div>
        {/if}

        <!-- Asset info -->
        <div class="slab">
          <div class="slab-header">
            <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
              Asset Information
            </h3>
          </div>
          <div class="slab-body--padded space-y-3 text-sm">
            <div class="flex justify-between">
              <span class="text-muted-foreground">Type</span>
              <span class="text-foreground">{kindLabel(artifact.kind)}</span>
            </div>
            <div class="flex justify-between">
              <span class="text-muted-foreground">Status</span>
              <span class="text-foreground">{artifact.status}</span>
            </div>
            <div class="flex justify-between gap-4">
              <span class="text-muted-foreground shrink-0">Hash</span>
              <span class="font-mono text-xs text-foreground truncate">{artifact.hash}</span>
            </div>
            <div class="flex justify-between">
              <span class="text-muted-foreground">Created</span>
              <span class="text-foreground">{formatDate(artifact.createdAt)}</span>
            </div>
            {#if artifact.durationSeconds}
              <div class="flex justify-between">
                <span class="text-muted-foreground">Duration</span>
                <span class="text-foreground">{formatTrackDuration(artifact.durationSeconds)}</span>
              </div>
            {/if}
            {#if artifact.tracks && artifact.tracks.length > 0}
              <div class="pt-1 space-y-1.5">
                <span class="text-muted-foreground">Tracks</span>
                <!-- Index key: this is a fixed per-artifact snapshot rendered once (not a live
                     reordering list), and tracks legitimately duplicate (e.g. two same-codec
                     audio tracks) so there's no stable per-track id to key on. -->
                {#each artifact.tracks as track, trackIndex (trackIndex)}
                  <div class="flex justify-between gap-4">
                    <Badge variant="outline" class="text-[10px] shrink-0 uppercase"
                      >{track.type}</Badge
                    >
                    <span class="text-xs text-foreground text-right truncate"
                      >{trackSummary(track)}</span
                    >
                  </div>
                {/each}
              </div>
            {/if}
          </div>
        </div>

        <!-- Storage & retention -->
        <div class="slab">
          <div class="slab-header flex items-center gap-2">
            <HardDriveIcon class="w-4 h-4 text-muted-foreground" />
            <h3 class="font-semibold text-xs uppercase tracking-wide text-muted-foreground">
              Storage
            </h3>
            <span class="ml-auto inline-flex items-center gap-2 text-xs font-medium">
              {#if durableState}
                <span class="inline-flex items-center gap-1 {durableState.tone}">
                  {#if durableState.frozen}<SnowflakeIcon class="w-3.5 h-3.5" />{/if}
                  {durableState.label}
                </span>
              {/if}
              {#if localState}
                <span class="text-muted-foreground">·</span>
                <span class={localState.tone}>{localState.label}</span>
              {/if}
            </span>
          </div>
          <div class="slab-body--padded space-y-3 text-sm">
            {#if storageNote}
              <p class="text-xs text-muted-foreground">{storageNote}</p>
            {/if}
            {#if lifecycle.length > 0}
              <div class="flex flex-wrap gap-1.5">
                {#each lifecycle as step (step.label)}
                  <span
                    class="px-2 py-0.5 rounded text-[11px] font-medium border {step.on
                      ? 'text-success border-success/30 bg-success/10'
                      : 'text-muted-foreground border-border/40'}"
                  >
                    {step.label}
                  </span>
                {/each}
              </div>
            {/if}
            {#if !lifecycleAvailable}
              <p class="text-xs text-warning">Storage lifecycle temporarily unavailable.</p>
            {/if}
            {#if artifact.storageClusterId}
              <div class="flex justify-between gap-4">
                <span class="text-muted-foreground shrink-0">Cluster</span>
                <span class="font-mono text-xs text-foreground truncate"
                  >{artifact.storageClusterId}</span
                >
              </div>
            {/if}
            {#if artifact.storageLocation}
              <div class="flex justify-between gap-4">
                <span class="text-muted-foreground shrink-0">Location</span>
                <span class="font-mono text-xs text-foreground truncate"
                  >{artifact.storageLocation}</span
                >
              </div>
            {/if}
            <div class="flex justify-between">
              <span class="text-muted-foreground">Size</span>
              <span class="font-mono text-foreground"
                >{artifact.sizeBytes ? formatBytes(artifact.sizeBytes) : "—"}</span
              >
            </div>
            {#if artifact.storageCost}
              <div class="flex justify-between">
                <span class="text-muted-foreground">Storage cost</span>
                <span class="font-mono text-foreground">
                  {artifact.storageCost.perMonth}
                  {artifact.storageCost.currency}/mo
                </span>
              </div>
            {/if}
            <div class="flex justify-between items-center gap-2">
              <span class="text-muted-foreground inline-flex items-center gap-1">
                <ClockIcon class="w-3.5 h-3.5" /> Retention
              </span>
              <span class="text-foreground text-right">
                {#if artifact.effectiveRetention?.retentionDays}
                  {artifact.effectiveRetention.retentionDays}d
                  <span class="text-xs text-muted-foreground"
                    >({artifact.effectiveRetention.source})</span
                  >
                {:else}
                  —
                {/if}
              </span>
            </div>
            {#if artifact.expiresAt}
              <div class="flex justify-between">
                <span class="text-muted-foreground">Expires</span>
                <span class="text-foreground">{formatDate(artifact.expiresAt)}</span>
              </div>
            {/if}
            {#if canEditRetention(artifact.kind)}
              <Button
                variant="outline"
                size="sm"
                class="w-full mt-2"
                onclick={() => (showRetentionDialog = true)}
              >
                Edit retention policy
              </Button>
            {/if}
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

{#if artifact && canEditRetention(artifact.kind)}
  <AssetRetentionDialog
    bind:open={showRetentionDialog}
    assetType={retentionTargetFor(artifact.kind) as MediaRetentionTarget$options}
    assetId={artifact.hash}
    assetName={artifact.title}
    currentExpiresAt={artifact.expiresAt}
    onClose={() => (showRetentionDialog = false)}
    onSaved={load}
  />
{/if}
