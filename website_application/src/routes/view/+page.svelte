<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import Player from "$lib/components/Player.svelte";
  import LoadingSpinner from "$lib/components/LoadingSpinner.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import { Button } from "$lib/components/ui/button";
  import type { ContentEndpoints, PlayerMetadata } from "@livepeer-frameworks/player-svelte";
  import {
    listDvrChapters,
    retrieveDvrChapter,
    type DVRChapter,
    type DVRChapterMode,
    type DVRChapterRef,
  } from "$lib/dvr-chapters";

  interface PlayerConfig {
    contentType?: "live" | "clip" | "dvr" | "vod";
    contentId: string;
    options: {
      autoplay: boolean;
      muted: boolean;
      controls: boolean;
      debug: boolean;
      devMode?: boolean;
      forceType?: string;
      playbackMode?: "auto" | "low-latency" | "quality";
    };
    endpoints?: ContentEndpoints | null;
  }

  let contentType = $state<"live" | "clip" | "dvr" | "vod" | null>(null);
  let contentId = $state("");
  let loading = $state(true);
  let error = $state("");
  let playerConfig = $state<PlayerConfig | null>(null);
  let streamMetadata = $state<PlayerMetadata | null>(null);
  let dvrChapters = $state<DVRChapterRef[]>([]);
  let selectedDvrChapter = $state<DVRChapter | null>(null);
  let selectedDvrChapterRef = $state<DVRChapterRef | null>(null);
  let dvrNextPageToken = $state<string | null>(null);
  let dvrRangeStartInput = $state("");
  let dvrRangeEndInput = $state("");
  let dvrChapterLoading = $state(false);
  let dvrListingUsesArtifactTimeline = $state(false);
  const dvrChapterPageSize = 200;

  // Derived display values from metadata
  let displayTitle = $derived(
    streamMetadata?.title ||
      (contentType === "live"
        ? "Live Stream"
        : contentType === "clip"
          ? "Clip"
          : contentType === "vod"
            ? "VOD Asset"
            : contentType === "dvr"
              ? "DVR Recording"
              : "Playback")
  );
  let _videoTrack = $derived(streamMetadata?.tracks?.find((t) => t.type === "video"));
  let resolutionLabel = $derived(_videoTrack ? `${_videoTrack.width}x${_videoTrack.height}` : null);
  let codecLabel = $derived(_videoTrack?.codec || null);
  let fpsLabel = $derived(_videoTrack?.fps ? `${_videoTrack.fps}fps` : null);
  let bitrateLabel = $derived(
    _videoTrack?.bitrate ? `${(_videoTrack.bitrate / 1000).toFixed(0)} kbps` : null
  );

  function handleMetadata(metadata: PlayerMetadata) {
    streamMetadata = metadata;
    if (!contentType) {
      const resolved = (metadata.contentType || "").toLowerCase();
      if (resolved === "live" || resolved === "clip" || resolved === "dvr" || resolved === "vod") {
        contentType = resolved as "live" | "clip" | "dvr" | "vod";
      } else if (metadata.isLive === true) {
        contentType = "live";
      } else if (metadata.isLive === false) {
        contentType = "vod";
      }
    }
  }

  function normalizeDvrChapterMode(mode: string | null): DVRChapterMode {
    const normalized = (mode || "").toUpperCase();
    if (normalized === "FIXED_INTERVAL") return "FIXED_INTERVAL";
    if (normalized === "EXPLICIT_RANGE") return "EXPLICIT_RANGE";
    return "WINDOW_SIZED";
  }

  function formatDvrChapterTime(valueMs: number) {
    return new Intl.DateTimeFormat(undefined, {
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
    }).format(new Date(valueMs));
  }

  function formatDvrChapterLabel(chapter: DVRChapterRef) {
    const prefix = chapter.isCurrent ? "Current" : "Chapter";
    const gap = chapter.hasGaps ? " - gaps" : "";
    return `${prefix}: ${formatDvrChapterTime(chapter.startMs)} - ${formatDvrChapterTime(chapter.endMs)}${gap}`;
  }

  function toDatetimeLocal(valueMs: number) {
    const date = new Date(valueMs);
    const offsetMs = date.getTimezoneOffset() * 60 * 1000;
    return new Date(valueMs - offsetMs).toISOString().slice(0, 16);
  }

  function fromDatetimeLocal(value: string) {
    const parsed = new Date(value).getTime();
    return Number.isFinite(parsed) ? parsed : 0;
  }

  function mergeDvrChapters(existing: DVRChapterRef[], incoming: DVRChapterRef[]) {
    const seen = new Set(existing.map((chapter) => chapter.chapterId));
    return [...existing, ...incoming.filter((chapter) => !seen.has(chapter.chapterId))];
  }

  function buildDvrEndpoints(chapter: DVRChapter, ref: DVRChapterRef): ContentEndpoints {
    return {
      primary: {
        nodeId: "dvr-chapter",
        protocol: "html5/application/vnd.apple.mpegurl",
        url: chapter.manifestUrl,
      },
      fallbacks: [],
      metadata: {
        title: formatDvrChapterLabel(ref),
        contentId: chapter.chapterId,
        contentType: "dvr",
        isLive: chapter.isCurrent,
        status: chapter.isCurrent ? "ONLINE" : "AVAILABLE",
        dvrStatus: chapter.isCurrent ? "recording" : "completed",
        durationSeconds: Math.max(0, Math.round((ref.endMs - ref.startMs) / 1000)),
      },
    };
  }

  async function selectDvrChapter(chapterRef: DVRChapterRef) {
    const chapter = await retrieveDvrChapter({
      dvrId: contentId,
      mode: chapterRef.mode,
      intervalSeconds: chapterRef.intervalSeconds,
      startMs: chapterRef.startMs,
      endMs: chapterRef.endMs,
    });

    selectedDvrChapter = chapter;
    selectedDvrChapterRef = chapterRef;
    playerConfig = {
      contentType: "dvr",
      contentId: chapter.chapterId,
      endpoints: buildDvrEndpoints(chapter, chapterRef),
      options: {
        autoplay: true,
        muted: true,
        controls: true,
        debug: true,
        devMode: true,
        forceType: "html5/application/vnd.apple.mpegurl",
        playbackMode: "quality",
      },
    };
  }

  async function loadDvrChapterPage(
    options: {
      append?: boolean;
      pageToken?: string | null;
      autoSelect?: boolean;
      fallbackExplicit?: boolean;
    } = {}
  ) {
    const startMs = fromDatetimeLocal(dvrRangeStartInput);
    const endMs = fromDatetimeLocal(dvrRangeEndInput);
    if (!startMs || !endMs || endMs <= startMs) {
      error = "Choose a valid DVR chapter range";
      return;
    }

    dvrChapterLoading = true;
    try {
      const page = await listDvrChapters({
        dvrId: contentId,
        rangeStartMs: startMs,
        rangeEndMs: endMs,
        pageSize: dvrChapterPageSize,
        pageToken: options.pageToken || null,
      });
      dvrListingUsesArtifactTimeline = false;
      dvrChapters = options.append ? mergeDvrChapters(dvrChapters, page.chapters) : page.chapters;
      dvrNextPageToken = page.nextPageToken || null;

      const defaultChapter =
        dvrChapters.find((chapter) => chapter.isCurrent) ||
        dvrChapters.reduce<DVRChapterRef | null>(
          (latest, chapter) => (!latest || chapter.startMs > latest.startMs ? chapter : latest),
          null
        );

      if (options.autoSelect && defaultChapter) {
        await selectDvrChapter(defaultChapter);
      } else if (options.autoSelect && options.fallbackExplicit) {
        const fallbackRef: DVRChapterRef = {
          chapterId: `range-${startMs}-${endMs}`,
          mode: "EXPLICIT_RANGE",
          intervalSeconds: null,
          startMs,
          endMs,
          isCurrent: endMs >= Date.now(),
          manifestS3Key: null,
          hasGaps: false,
          segmentCount: 0,
        };
        dvrChapters = [fallbackRef];
        dvrNextPageToken = null;
        await selectDvrChapter(fallbackRef);
      }
    } finally {
      dvrChapterLoading = false;
    }
  }

  async function loadDvrArtifactTimelinePage(
    options: { append?: boolean; pageToken?: string | null; autoSelect?: boolean } = {}
  ) {
    dvrChapterLoading = true;
    try {
      const page = await listDvrChapters({
        dvrId: contentId,
        pageSize: dvrChapterPageSize,
        pageToken: options.pageToken || null,
      });
      dvrListingUsesArtifactTimeline = true;
      dvrChapters = options.append ? mergeDvrChapters(dvrChapters, page.chapters) : page.chapters;
      dvrNextPageToken = page.nextPageToken || null;

      const defaultChapter =
        dvrChapters.find((chapter) => chapter.isCurrent) ||
        dvrChapters.reduce<DVRChapterRef | null>(
          (latest, chapter) => (!latest || chapter.startMs > latest.startMs ? chapter : latest),
          null
        );
      if (options.autoSelect && defaultChapter) {
        dvrRangeStartInput = toDatetimeLocal(defaultChapter.startMs);
        dvrRangeEndInput = toDatetimeLocal(defaultChapter.endMs);
        await selectDvrChapter(defaultChapter);
      }
    } finally {
      dvrChapterLoading = false;
    }
  }

  async function loadDvrPlayback(params: URLSearchParams) {
    const nowMs = Date.now();
    const explicitStart = Number(params.get("startMs") || params.get("start") || 0);
    const explicitEnd = Number(params.get("endMs") || params.get("end") || 0);
    const mode = normalizeDvrChapterMode(params.get("mode"));
    const interval = Number(params.get("intervalSeconds") || 0) || null;

    if (explicitStart > 0 && explicitEnd > explicitStart) {
      const ref: DVRChapterRef = {
        chapterId: params.get("chapterId") || `${explicitStart}:${explicitEnd}`,
        mode,
        intervalSeconds: interval,
        startMs: explicitStart,
        endMs: explicitEnd,
        isCurrent: explicitEnd >= nowMs,
        manifestS3Key: null,
        hasGaps: false,
        segmentCount: 0,
      };
      dvrChapters = [ref];
      await selectDvrChapter(ref);
      return;
    }

    await loadDvrArtifactTimelinePage({ autoSelect: true });

    if (!selectedDvrChapterRef) {
      dvrRangeStartInput = toDatetimeLocal(nowMs - 24 * 60 * 60 * 1000);
      dvrRangeEndInput = toDatetimeLocal(nowMs + 60 * 60 * 1000);
    }
  }

  onMount(async () => {
    // Parse URL parameters
    const params = page.url.searchParams;
    const typeParam = (params.get("type") || "").toLowerCase();
    contentId = params.get("id") || "";

    // Validate required parameters
    if (!contentId) {
      error = "Missing required parameter: id";
      loading = false;
      return;
    }

    try {
      if (["live", "clip", "dvr", "vod"].includes(typeParam)) {
        contentType = typeParam as "live" | "clip" | "dvr" | "vod";
      }

      if (contentType === "dvr") {
        await loadDvrPlayback(params);
        loading = false;
        return;
      }

      // Configure player based on content type
      playerConfig = {
        contentType: contentType || undefined,
        contentId,
        options: {
          autoplay: true,
          muted: true,
          controls: true,
          debug: true,
          devMode: true,
        },
      };

      loading = false;
    } catch (err) {
      console.error("Error setting up player:", err);
      error = "Failed to initialize player";
      loading = false;
    }
  });

  function goBack() {
    if (window.history.length > 1) {
      window.history.back();
    } else {
      goto(resolve("/"));
    }
  }
</script>

<svelte:head>
  <title>Viewing {displayTitle} - FrameWorks</title>
</svelte:head>

<div class="flex flex-col h-full overflow-y-auto p-4 md:p-6 bg-background">
  {#if loading}
    <div class="flex items-center justify-center flex-1 h-full min-h-[400px]">
      <LoadingSpinner />
    </div>
  {:else if error}
    <div class="flex items-center justify-center flex-1 h-full min-h-[400px]">
      <EmptyState title="Error" description={error} actionText="Go Back" onAction={goBack} />
    </div>
  {:else if playerConfig}
    <div class="max-w-7xl mx-auto w-full space-y-6">
      <!-- Header Slab -->
      <div class="slab h-auto">
        <div class="slab-header flex items-center justify-between">
          <div class="flex items-center gap-3">
            <Button variant="ghost" size="sm" onclick={goBack} class="gap-2">
              <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                <path
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  stroke-width="2"
                  d="M10 19l-7-7 7-7m8 14l-7-7 7-7"
                />
              </svg>
              Back
            </Button>
            <div class="h-4 w-px bg-[hsl(var(--tn-fg-gutter)/0.3)]"></div>
            <h1 class="text-sm font-semibold uppercase tracking-wide text-foreground">
              {displayTitle}
            </h1>
          </div>

          {#if contentType === "live" && streamMetadata?.viewers !== undefined}
            <div
              class="flex items-center gap-2 px-3 py-1 bg-[hsl(var(--tn-bg-dark)/0.5)] rounded border border-[hsl(var(--tn-fg-gutter)/0.3)]"
            >
              <span class="w-2 h-2 bg-[hsl(var(--tn-red))] rounded-full animate-pulse"></span>
              <span class="text-xs font-medium text-[hsl(var(--tn-red))] uppercase tracking-wider">
                {streamMetadata.viewers} Viewer{streamMetadata.viewers !== 1 ? "s" : ""}
              </span>
            </div>
          {/if}
        </div>
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-3 gap-6">
        <!-- Main Player Column -->
        <div class="lg:col-span-2 space-y-6">
          <div class="slab overflow-hidden bg-black shadow-xl border-none">
            <!-- Responsive Container -->
            <div class="relative w-full h-[65vh] min-h-[480px]">
              <Player
                contentId={playerConfig.contentId}
                contentType={playerConfig.contentType}
                endpoints={playerConfig.endpoints}
                options={playerConfig.options}
                onMetadata={handleMetadata}
              />
            </div>
          </div>
        </div>

        <!-- Info Column -->
        <div class="space-y-6">
          {#if contentType === "dvr"}
            <div class="slab">
              <div class="slab-header">
                <h3 class="font-medium text-[hsl(var(--tn-fg-dark))]">DVR Chapter</h3>
              </div>
              <div class="slab-body--padded space-y-3">
                <div class="grid grid-cols-1 gap-2">
                  <label
                    for="dvr-range-start"
                    class="text-xs uppercase tracking-wider text-muted-foreground"
                  >
                    From
                  </label>
                  <input
                    id="dvr-range-start"
                    type="datetime-local"
                    class="w-full rounded border border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background px-3 py-2 text-sm text-foreground"
                    bind:value={dvrRangeStartInput}
                  />
                  <label
                    for="dvr-range-end"
                    class="text-xs uppercase tracking-wider text-muted-foreground"
                  >
                    To
                  </label>
                  <input
                    id="dvr-range-end"
                    type="datetime-local"
                    class="w-full rounded border border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background px-3 py-2 text-sm text-foreground"
                    bind:value={dvrRangeEndInput}
                  />
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={dvrChapterLoading}
                    onclick={async () => {
                      try {
                        await loadDvrChapterPage({ autoSelect: true, fallbackExplicit: true });
                      } catch (err) {
                        console.error("Failed to load DVR chapters:", err);
                        error = "Failed to load DVR chapters";
                      }
                    }}
                  >
                    Load Range
                  </Button>
                </div>
                {#if dvrChapters.length > 1}
                  <label
                    for="dvr-chapter-select"
                    class="text-xs uppercase tracking-wider text-muted-foreground"
                  >
                    Chapter
                  </label>
                  <select
                    id="dvr-chapter-select"
                    class="w-full rounded border border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background px-3 py-2 text-sm text-foreground"
                    value={selectedDvrChapterRef?.chapterId || ""}
                    onchange={async (event) => {
                      const value = (event.currentTarget as HTMLSelectElement).value;
                      const chapter = dvrChapters.find(
                        (candidate) => candidate.chapterId === value
                      );
                      if (chapter) {
                        try {
                          await selectDvrChapter(chapter);
                        } catch (err) {
                          console.error("Failed to load DVR chapter:", err);
                          error = "Failed to load DVR chapter";
                        }
                      }
                    }}
                  >
                    {#each dvrChapters as chapter (chapter.chapterId)}
                      <option value={chapter.chapterId}>{formatDvrChapterLabel(chapter)}</option>
                    {/each}
                  </select>
                  {#if dvrNextPageToken}
                    <Button
                      variant="ghost"
                      size="sm"
                      disabled={dvrChapterLoading}
                      onclick={async () => {
                        try {
                          if (dvrListingUsesArtifactTimeline) {
                            await loadDvrArtifactTimelinePage({
                              append: true,
                              pageToken: dvrNextPageToken,
                            });
                          } else {
                            await loadDvrChapterPage({
                              append: true,
                              pageToken: dvrNextPageToken,
                            });
                          }
                        } catch (err) {
                          console.error("Failed to load more DVR chapters:", err);
                          error = "Failed to load more DVR chapters";
                        }
                      }}
                    >
                      Load More
                    </Button>
                  {/if}
                {/if}
                {#if selectedDvrChapterRef}
                  <div class="grid grid-cols-2 gap-3">
                    <div
                      class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]"
                    >
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">
                        Range
                      </div>
                      <div class="text-xs text-foreground">
                        {formatDvrChapterTime(selectedDvrChapterRef.startMs)}
                      </div>
                      <div class="text-xs text-muted-foreground">
                        {formatDvrChapterTime(selectedDvrChapterRef.endMs)}
                      </div>
                    </div>
                    <div
                      class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]"
                    >
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">
                        Segments
                      </div>
                      <div class="font-mono text-sm text-foreground">
                        {selectedDvrChapter?.segmentCount || selectedDvrChapterRef.segmentCount}
                      </div>
                      {#if selectedDvrChapter?.hasGaps || selectedDvrChapterRef.hasGaps}
                        <div class="text-xs text-warning">Has gaps</div>
                      {/if}
                    </div>
                  </div>
                {/if}
              </div>
            </div>
          {/if}

          <!-- Metadata Slab -->
          <div class="slab">
            <div class="slab-header">
              <h3 class="font-medium text-[hsl(var(--tn-fg-dark))]">Stream Metadata</h3>
            </div>
            <div class="slab-body--padded">
              {#if !streamMetadata}
                <div
                  class="flex flex-col items-center justify-center py-8 text-center text-muted-foreground"
                >
                  <LoadingSpinner class="w-6 h-6 mb-2 opacity-50" />
                  <span class="text-xs">Waiting for stream data...</span>
                </div>
              {:else}
                <div class="space-y-4">
                  <!-- Resolution & Codec -->
                  <div class="grid grid-cols-2 gap-4">
                    <div
                      class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]"
                    >
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">
                        Resolution
                      </div>
                      <div class="font-mono text-sm text-foreground">
                        {resolutionLabel || "Unknown"}
                      </div>
                    </div>
                    <div
                      class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]"
                    >
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">
                        Codec
                      </div>
                      <div class="font-mono text-sm text-foreground uppercase">
                        {codecLabel || "Unknown"}
                      </div>
                    </div>
                  </div>

                  <!-- FPS & Bitrate -->
                  <div class="grid grid-cols-2 gap-4">
                    <div
                      class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]"
                    >
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">
                        Frame Rate
                      </div>
                      <div class="font-mono text-sm text-foreground">
                        {fpsLabel || "Unknown"}
                      </div>
                    </div>
                    <div
                      class="p-3 rounded bg-[hsl(var(--tn-bg-dark)/0.3)] border border-[hsl(var(--tn-fg-gutter)/0.2)]"
                    >
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-1">
                        Bitrate
                      </div>
                      <div class="font-mono text-sm text-foreground">
                        {bitrateLabel || "Unknown"}
                      </div>
                    </div>
                  </div>

                  <!-- Advanced Info -->
                  {#if streamMetadata.protocol || streamMetadata.nodeId || streamMetadata.geoDistance !== undefined}
                    <div class="pt-4 border-t border-[hsl(var(--tn-fg-gutter)/0.2)]">
                      <div class="flex justify-between items-center text-xs">
                        <span class="text-muted-foreground">Protocol</span>
                        <span class="font-mono text-foreground uppercase"
                          >{streamMetadata.protocol || "N/A"}</span
                        >
                      </div>
                      {#if streamMetadata.nodeId}
                        <div class="flex justify-between items-center text-xs mt-2">
                          <span class="text-muted-foreground">Node</span>
                          <span class="font-mono text-foreground">{streamMetadata.nodeId}</span>
                        </div>
                      {/if}
                      {#if streamMetadata.geoDistance !== undefined}
                        <div class="flex justify-between items-center text-xs mt-2">
                          <span class="text-muted-foreground">Geo Distance</span>
                          <span class="font-mono text-foreground"
                            >{streamMetadata.geoDistance.toFixed(0)} km</span
                          >
                        </div>
                      {/if}
                    </div>
                  {/if}

                  <!-- Debug Info (Merged) -->
                  {#if playerConfig.options.devMode}
                    <div class="pt-4 border-t border-[hsl(var(--tn-fg-gutter)/0.2)]">
                      <div class="text-[10px] uppercase tracking-wider text-muted-foreground mb-2">
                        Debug Info
                      </div>
                      <div
                        class="text-xs font-mono text-muted-foreground break-all bg-[hsl(var(--tn-bg-dark)/0.5)] p-2 rounded"
                      >
                        <div>ID: {contentId}</div>
                        <div class="mt-1">Type: {contentType}</div>
                      </div>
                    </div>
                  {/if}
                </div>
              {/if}
            </div>
          </div>
        </div>
      </div>
    </div>
  {/if}
</div>
