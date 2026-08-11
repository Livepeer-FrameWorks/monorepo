<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { SvelteMap, SvelteSet } from "svelte/reactivity";
  import { get } from "svelte/store";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { page } from "$app/stores";
  import { auth } from "$lib/stores/auth";
  import {
    fragment,
    GetStreamsConnectionStore,
    GetStorageArtifactsConnectionStore,
    GetArtifactEventsConnectionStore,
    GetArtifactStatesConnectionStore,
    GetStorageEventsConnectionStore,
    CreateClipStore,
    DeleteClipStore,
    DeleteDVRStore,
    CreateVodUploadStore,
    CompleteVodUploadStore,
    AbortVodUploadStore,
    DeleteVodAssetStore,
    GetVodUploadStatusStore,
    ClipLifecycleStore,
    DvrLifecycleStore,
    VodLifecycleStore,
    ClipCreationMode,
    StorageArtifactKind,
    StreamCoreFieldsStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import DeleteClipModal from "$lib/components/clips/DeleteClipModal.svelte";
  import DeleteRecordingModal from "$lib/components/recordings/DeleteRecordingModal.svelte";
  import AssetRetentionDialog from "$lib/components/library/AssetRetentionDialog.svelte";
  import type { MediaRetentionTarget$options, StorageArtifactKind$options } from "$houdini";
  import {
    Dialog,
    DialogContent,
    DialogDescription,
    DialogFooter,
    DialogHeader,
    DialogTitle,
  } from "$lib/components/ui/dialog";
  import { Input } from "$lib/components/ui/input";
  import { Textarea } from "$lib/components/ui/textarea";
  import { Progress } from "$lib/components/ui/progress";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import {
    Table,
    TableHeader,
    TableHead,
    TableRow,
    TableBody,
    TableCell,
  } from "$lib/components/ui/table";
  import { getIconComponent } from "$lib/iconUtils";
  import { getContentDeliveryUrls } from "$lib/config";
  import SpriteThumbnail from "$lib/components/shared/SpriteThumbnail.svelte";
  import { formatBytes, formatExpiry, formatTimestamp, isExpired } from "$lib/utils/formatters.js";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import {
    createUploadEngine,
    createIndexedDBSessionStore,
    createMemorySessionStore,
    findRecoverable,
    attachDropZone,
    isVideoFile,
    type UploadEngine,
    type SessionStore,
  } from "$lib/uploads";

  // Type definitions
  // "other" is the fail-closed bucket for an unrecognized kind — never silently coerce an unknown
  // type into a specific one (getTypeLabel renders it "Unknown", retention maps it to no action, and
  // the kind filters only surface it under "all").
  type ArtifactType = "all" | "clips" | "dvr" | "vod" | "other";

  interface ThumbnailAssetsView {
    posterUrl: string;
    spriteVttUrl: string;
    spriteJpgUrl: string;
    assetKey: string;
  }

  interface UnifiedArtifact {
    id: string;
    type: ArtifactType;
    title: string;
    hash: string;
    playbackId: string | null;
    streamId: string | null;
    displayStreamId: string | null;
    status: string;
    description?: string | null;
    errorMessage?: string | null;
    duration: number | null;
    sizeBytes: number | null;
    segmentCount?: number | null;
    createdAt: string | null;
    expiresAt: string | null;
    hasLocalCopy?: boolean | null;
    storageLocation?: string;
    syncStatus?: string | null;
    isSynced?: boolean | null;
    isFinalized?: boolean | null;
    thumbnailAssets: ThumbnailAssetsView | null;
    rawData: StorageArtifactNode;
  }

  // Houdini stores
  const streamsStore = new GetStreamsConnectionStore();
  const artifactsStore = new GetStorageArtifactsConnectionStore();
  const artifactEventsStore = new GetArtifactEventsConnectionStore();
  const artifactStatesStore = new GetArtifactStatesConnectionStore();
  const storageEventsStore = new GetStorageEventsConnectionStore();

  // Mutations
  const createClipMutation = new CreateClipStore();
  const deleteClipMutation = new DeleteClipStore();
  const deleteDvrMutation = new DeleteDVRStore();
  const createVodUploadMutation = new CreateVodUploadStore();
  const completeVodUploadMutation = new CompleteVodUploadStore();
  const abortVodUploadMutation = new AbortVodUploadStore();
  const deleteVodMutation = new DeleteVodAssetStore();
  const vodUploadStatusQuery = new GetVodUploadStatusStore();

  // Subscriptions
  const clipLifecycleSub = new ClipLifecycleStore();
  const dvrLifecycleSub = new DvrLifecycleStore();
  const vodLifecycleSub = new VodLifecycleStore();

  // Fragment stores
  const streamCoreStore = new StreamCoreFieldsStore();

  // Types from stores — the single unified catalog node backs every library row.
  type StorageArtifactNode = NonNullable<
    NonNullable<typeof $artifactsStore.data>["storageArtifactsConnection"]
  >["nodes"][number];

  // Connection metadata (pagination + facet counts) copied off an accepted fetch. Held in
  // local generation-scoped state — not read straight off the Houdini store — so a late,
  // stale response from a superseded filter/search cannot overwrite the counts and
  // "Load more" affordance while the accumulated rows stay correctly guarded.
  type StorageArtifactKindCounts = NonNullable<
    NonNullable<typeof $artifactsStore.data>["storageArtifactsConnection"]
  >["kindCounts"];
  type StorageArtifactConnectionMeta = {
    hasNextPage: boolean;
    totalCount: number;
    // null until the first accepted response; all reads null-guard via kindCounts?.
    kindCounts: StorageArtifactKindCounts | null;
  };

  type LifecycleEventRow = {
    eventKey: string;
    timestamp: string;
    stage: string;
    type: ArtifactType;
    message?: string | null;
    percent?: number | null;
  };

  let isAuthenticated = false;

  // Loading state
  let loading = $derived($streamsStore.fetching || $artifactsStore.fetching);

  // Type filter from URL or state
  let typeFilter = $state<ArtifactType>("all");

  // Offset/limit pagination for the unified connection. Commodore clamps `first`/limit
  // to 100, so we page: each fetch pulls PAGE_SIZE rows at a growing offset and appends
  // to a local accumulator. The store's connection metadata (hasNextPage, totalCount,
  // kindCounts) always reflects the last-fetched window — exactly what the tiles and
  // "Load more" affordance need.
  const PAGE_SIZE = 25;
  // Rows accumulated across pages. Reset to page 0 whenever the kind filter or search
  // changes; appended on "Load more".
  let accumulatedNodes = $state<StorageArtifactNode[]>([]);
  // Connection metadata for the accumulated window, installed atomically with
  // `accumulatedNodes` under the same generation guard (see reloadArtifacts/loadMore).
  let connectionMeta = $state<StorageArtifactConnectionMeta>({
    hasNextPage: false,
    totalCount: 0,
    kindCounts: null,
  });
  // Debounce handle for server-side search; cleared on destroy.
  let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null;
  // Guards the filter/search-driven refetch effects from firing before onMount's first load.
  let filterInitialized = false;
  // Monotonic request-generation counter. Bumped at the start of every reset-reload
  // (kind/search/status change, retention refetch, mutation refresh); each fetch captures
  // the value it started under and drops its result if a newer generation has begun. This
  // keeps overlapping requests from installing stale rows/metadata out of order and stops a
  // late page from a superseded filter being appended by "Load more".
  let requestGeneration = 0;
  // Surfaced when a fetch returns in-band GraphQL errors. Set instead of clearing the
  // accumulator so a partial/failed response never blanks a valid list.
  let loadError = $state<string | null>(null);
  // False until a catalog response has SUCCESSFULLY installed metadata at least once. Guards the
  // stat tiles and the empty-state from rendering fabricated zeros / "No items found" when the very
  // first fetch failed (nothing was actually queried — the counts are unknown, not zero).
  let catalogLoaded = $state(false);

  // Kind filter tabs map to the server-side `kinds` input so the connection returns
  // only the requested artifact kinds (all → omit; vod → VOD + CHAPTER).
  function kindsForFilter(t: ArtifactType): StorageArtifactKind$options[] | undefined {
    if (t === "clips") return [StorageArtifactKind.CLIP];
    if (t === "dvr") return [StorageArtifactKind.DVR];
    if (t === "vod") return [StorageArtifactKind.VOD, StorageArtifactKind.CHAPTER];
    return undefined;
  }

  // UI status bucket → the server-side `status` input value. The connection applies this
  // account-wide before count/facets/pagination, so it is authoritative (no client-side
  // status filter over the loaded window). "all" clears the filter (undefined = all).
  function statusForFilter(s: string): string | undefined {
    if (s === "processing" || s === "ready" || s === "failed") return s;
    return undefined;
  }

  // Fetch a single page at `offset`. Server-side `search` narrows the whole account
  // scope, not just the loaded window.
  function fetchPage(offset: number, opts?: { policy?: "NetworkOnly" }) {
    return artifactsStore.fetch({
      variables: {
        input: {
          first: PAGE_SIZE,
          offset,
          kinds: kindsForFilter(typeFilter),
          search: searchQuery.trim() || undefined,
          status: statusForFilter(statusFilter),
        },
      },
      ...(opts?.policy ? { policy: opts.policy } : {}),
    });
  }

  // Read Houdini's in-band `errors` array off a fetch result. A GraphQL error can arrive
  // alongside a `null`/empty `data`, so callers use this to avoid overwriting a valid list
  // with an empty one.
  function resultErrors(result: unknown): unknown[] | null {
    const errs = (result as { errors?: unknown[] })?.errors;
    return Array.isArray(errs) && errs.length ? errs : null;
  }

  // Reset the accumulator and load page 0. Used on initial load, filter/search/status
  // change, and after mutations/retention edits. Each call bumps the request generation and
  // captures it: if a newer reset started while this fetch was in flight, the stale result
  // is dropped (no accumulator/metadata overwrite). In-band GraphQL errors surface a
  // message but leave the previously accumulated rows intact.
  async function reloadArtifacts(opts?: { policy?: "NetworkOnly" }) {
    const gen = ++requestGeneration;
    let res: Awaited<ReturnType<typeof fetchPage>>;
    try {
      res = await fetchPage(0, opts);
    } catch (error) {
      // Transport failure. Surface a user-visible error but keep the previously
      // accumulated rows and metadata — a failed request must never blank a valid list.
      if (gen !== requestGeneration) return;
      console.error("Failed to load library items:", error);
      loadError = "Some library items could not be loaded. Showing the last successful results.";
      return;
    }
    if (gen !== requestGeneration) return;
    if (resultErrors(res)) {
      loadError = "Some library items could not be loaded. Showing the last successful results.";
      return;
    }
    loadError = null;
    // Install rows and connection metadata together, only now that the generation is
    // confirmed current, so a stale late response cannot overwrite either.
    const connection = res.data?.storageArtifactsConnection;
    accumulatedNodes = connection?.nodes ?? [];
    connectionMeta = {
      hasNextPage: connection?.hasNextPage ?? false,
      totalCount: connection?.totalCount ?? 0,
      kindCounts: connection?.kindCounts ?? null,
    };
    catalogLoaded = true;
  }

  // Initialize from URL params
  $effect(() => {
    const urlType = $page.url.searchParams.get("type") as ArtifactType;
    if (urlType && ["clips", "dvr", "vod"].includes(urlType)) {
      typeFilter = urlType;
    }
  });

  // Refetch the catalog whenever the kind filter changes (server filters by `kinds`).
  // Skips the very first run — onMount does the initial fetch with the resolved filter.
  $effect(() => {
    void typeFilter;
    if (!filterInitialized) return;
    untrack(() => {
      void reloadArtifacts({ policy: "NetworkOnly" });
    });
  });

  // Raw data from stores
  let maskedStreams = $derived(
    $streamsStore.data?.streamsConnection?.edges?.map((e) => e.node) ?? []
  );
  let streams = $derived(maskedStreams.map((node) => get(fragment(node, streamCoreStore))));
  // Rows shown come from the local accumulator (all loaded pages), not just the last
  // store page.
  let artifactNodes = $derived(accumulatedNodes);

  // Artifact states for in-progress operations
  let artifactStates = $derived(
    $artifactStatesStore.data?.analytics?.lifecycle?.artifactStatesConnection?.edges?.map((e) => ({
      cursor: e.cursor,
      ...e.node,
    })) ?? []
  );
  let artifactStatesByPlaybackId = $derived.by(() => {
    const byID = new SvelteMap<string, (typeof artifactStates)[number]>();
    for (const state of artifactStates) {
      if (!state.playbackId) continue;
      const current = byID.get(state.playbackId);
      if (
        !current ||
        new Date(state.completedAt ?? state.startedAt ?? state.requestedAt ?? 0).getTime() >
          new Date(current.completedAt ?? current.startedAt ?? current.requestedAt ?? 0).getTime()
      ) {
        byID.set(state.playbackId, state);
      }
    }
    return byID;
  });

  // Storage events (freeze + relay cache fills) — deduplicate by id to avoid keyed-each errors
  let storageEvents = $derived.by(() => {
    const nodes =
      $storageEventsStore.data?.analytics?.lifecycle?.storageEventsConnection?.edges?.map(
        (e) => e.node
      ) ?? [];
    const seen = new SvelteSet<string>();
    return nodes.filter((n) => {
      if (seen.has(n.id)) return false;
      seen.add(n.id);
      return true;
    });
  });

  // Pagination state — single offset/limit connection.
  let loadingMore = $state(false);
  let hasMoreItems = $derived(connectionMeta.hasNextPage);

  // StorageArtifactKind → the page's coarse ArtifactType. CHAPTER plays back as VOD.
  function kindToType(kind: StorageArtifactNode["kind"]): ArtifactType {
    if (kind === "CLIP") return "clips";
    if (kind === "DVR") return "dvr";
    return "vod"; // VOD + CHAPTER
  }

  // Unified artifacts — one row per catalog node.
  let allArtifacts = $derived.by(() => {
    const unified: UnifiedArtifact[] = [];

    for (const node of artifactNodes) {
      const lifecycle = lifecycleStateForPlaybackId(node.playbackId);
      unified.push({
        id: node.id,
        type: kindToType(node.kind),
        title: node.title,
        hash: node.hash,
        playbackId: node.playbackId ?? null,
        streamId: node.streamId ?? null,
        displayStreamId: node.streamTitle || null,
        status: displayArtifactStage(artifactRowStage(node.status, lifecycle?.stage)),
        // Catalog-projected metadata: description (VOD uploads) + processing-failure detail.
        description: node.description ?? null,
        errorMessage: node.errorMessage ?? null,
        // durationSeconds is the measured length in seconds for any kind; null until finalized.
        duration: node.durationSeconds ?? null,
        // Catalog size is authoritative for a registered row; the feed only fills in a size for a
        // not-yet-catalogued in-progress artifact.
        sizeBytes: node.sizeBytes ?? lifecycle?.sizeBytes ?? null,
        segmentCount: lifecycle?.segmentCount ?? null,
        createdAt: node.createdAt,
        expiresAt: node.expiresAt ?? null,
        hasLocalCopy: node.hasLocalCopy ?? null,
        storageLocation: node.storageLocation ?? undefined,
        syncStatus: node.syncStatus ?? null,
        isSynced: node.isSynced ?? null,
        isFinalized: node.isFinalized ?? null,
        thumbnailAssets: node.thumbnailAssets ?? null,
        rawData: node,
      });
    }

    // Sort by created date, newest first
    return unified.sort((a, b) => {
      const dateA = a.createdAt ? new Date(a.createdAt).getTime() : 0;
      const dateB = b.createdAt ? new Date(b.createdAt).getTime() : 0;
      return dateB - dateA;
    });
  });
  let artifactStatusByPlaybackId = $derived.by(() => {
    const byID = new SvelteMap<string, string>();
    for (const artifact of allArtifacts) {
      if (artifact.playbackId) {
        byID.set(artifact.playbackId, artifact.status);
      }
    }
    return byID;
  });
  let inProgressArtifacts = $derived.by(() => {
    const unresolved = [...artifactStatesByPlaybackId.values()].filter((state) => {
      const rowStatus = state.playbackId ? artifactStatusByPlaybackId.get(state.playbackId) : null;
      if (rowStatus && isTerminalArtifactStage(rowStatus)) return false;
      return !isTerminalArtifactStage(state.stage);
    });
    return unresolved.sort((a, b) => {
      const dateA = new Date(a.startedAt ?? a.requestedAt ?? 0).getTime();
      const dateB = new Date(b.startedAt ?? b.requestedAt ?? 0).getTime();
      return dateB - dateA;
    });
  });

  // Search and filters
  let searchQuery = $state("");
  let statusFilter = $state("all");

  // Debounced server-side search. Resets the accumulator and refetches from offset 0
  // whenever the search box settles (~250ms), so the query narrows the whole account
  // scope rather than only the loaded window. Timer is cleared on destroy.
  $effect(() => {
    void searchQuery;
    if (!filterInitialized) return;
    if (searchDebounceTimer) clearTimeout(searchDebounceTimer);
    searchDebounceTimer = setTimeout(() => {
      void reloadArtifacts({ policy: "NetworkOnly" });
    }, 250);
  });

  // Status is filtered server-side (input.status) account-wide, so a change resets the
  // accumulator and refetches from offset 0 — same reset+generation path as kind/search.
  $effect(() => {
    void statusFilter;
    if (!filterInitialized) return;
    untrack(() => {
      void reloadArtifacts({ policy: "NetworkOnly" });
    });
  });

  let filteredArtifacts = $derived.by(() => {
    let result = allArtifacts;

    // Filter by type. Redundant with the server-side `kinds` input (which already
    // scopes the fetched rows) but harmless — kept as a belt-and-suspenders guard.
    if (typeFilter !== "all") {
      result = result.filter((a) => a.type === typeFilter);
    }

    // Search (input.search) and status (input.status) are both applied server-side
    // account-wide before pagination, so the accumulated rows are already scoped to the
    // query and status bucket — no client-side title/hash or status filter here.

    return result;
  });

  // Stats. `kindCounts` is server-authoritative over the active search/stream scope
  // (it ignores the kind filter), so the tiles show true account totals — not
  // page-local counts. VOD combines VOD + CHAPTER (chapters play back as VOD).
  let kindCounts = $derived(connectionMeta.kindCounts ?? null);
  let totalAll = $derived(kindCounts?.total ?? connectionMeta.totalCount ?? allArtifacts.length);
  let totalClips = $derived(kindCounts?.clip ?? 0);
  let totalDvr = $derived(kindCounts?.dvr ?? 0);
  let totalVod = $derived((kindCounts?.vod ?? 0) + (kindCounts?.chapter ?? 0));
  // Until a catalog response has landed the counts are UNKNOWN, not zero: render "—" so a failed
  // first fetch never shows fabricated zeros.
  let statTotalAll = $derived<string | number>(catalogLoaded ? totalAll : "—");
  let statTotalClips = $derived<string | number>(catalogLoaded ? totalClips : "—");
  let statTotalDvr = $derived<string | number>(catalogLoaded ? totalDvr : "—");
  let statTotalVod = $derived<string | number>(catalogLoaded ? totalVod : "—");

  // Lifecycle events
  let lifecycleRange = $state("7d");
  let lifecycleRangeResolved = $derived(resolveTimeRange(lifecycleRange));
  const lifecycleRangeOptions = TIME_RANGE_OPTIONS.filter((opt) =>
    ["24h", "7d", "30d"].includes(opt.value)
  );
  let lifecycleEvents = $derived(
    $artifactEventsStore.data?.analytics?.lifecycle?.artifactEventsConnection?.edges?.map((e) => ({
      eventKey: e.node.id ?? e.cursor,
      timestamp: e.node.timestamp,
      stage: e.node.stage,
      type: normalizeArtifactType((e.node as { contentType?: string }).contentType),
      message: e.node.message,
      percent: e.node.percent,
    })) ?? []
  );
  let liveLifecycleEvents = $state<LifecycleEventRow[]>([]);
  let mergedLifecycleEvents = $derived.by(() => {
    const seen = new SvelteSet<string>();
    const merged: LifecycleEventRow[] = [];
    for (const event of [...liveLifecycleEvents, ...lifecycleEvents]) {
      const key = `${event.eventKey}-${event.stage}-${event.timestamp}`;
      if (seen.has(key)) continue;
      seen.add(key);
      merged.push(event);
    }
    return merged.sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime());
  });
  let lifecycleDisplayCount = $state(20);

  // Expanded row
  let expandedArtifact = $state<string | null>(null);

  // Delete modals — the selected row's raw catalog node backs each dialog.
  let showDeleteClipModal = $state(false);
  let clipToDelete = $state<StorageArtifactNode | null>(null);
  let deletingClipId = $state("");

  let showDeleteDvrModal = $state(false);
  let dvrToDelete = $state<StorageArtifactNode | null>(null);
  let deletingDvrHash = $state("");

  let showDeleteVodModal = $state(false);
  let vodToDelete = $state<StorageArtifactNode | null>(null);
  let deletingVodId = $state("");

  // Retention editor — one dialog instance driven by the selected artifact.
  let showRetentionDialog = $state(false);
  let retentionTarget = $state<{
    type: MediaRetentionTarget$options;
    id: string;
    name: string;
    expiresAt: string | null;
  } | null>(null);

  function artifactRetentionType(t: ArtifactType): MediaRetentionTarget$options | null {
    switch (t) {
      case "dvr":
        return "DVR";
      case "clips":
        return "CLIP";
      case "vod":
        return "VOD";
      default:
        return null;
    }
  }

  function openRetentionEditor(a: UnifiedArtifact) {
    const tt = artifactRetentionType(a.type);
    if (!tt) return;
    retentionTarget = {
      type: tt,
      id: a.hash || a.id,
      name: a.title || a.hash || a.id,
      expiresAt: a.expiresAt,
    };
    showRetentionDialog = true;
  }

  async function refetchLibraryAfterRetention() {
    // After a retention change the row's expiresAt is stale. Refetch from page 0 to
    // pick up the new expiry (this collapses the list back to the first page).
    await reloadArtifacts({ policy: "NetworkOnly" });
  }

  // Create clip modal
  let showCreateClipModal = $state(false);
  let creatingClip = $state(false);
  let selectedStreamId = $state("");
  let clipMode = $state<"CLIP_NOW" | "DURATION" | "ABSOLUTE">("CLIP_NOW");
  let clipTitle = $state("");
  let clipDescription = $state("");
  let clipDuration = $state(60);
  let clipStartTime = $state(0);
  let clipEndTime = $state(300);

  const durationPresets = [
    { label: "30s", value: 30 },
    { label: "1 min", value: 60 },
    { label: "2 min", value: 120 },
    { label: "5 min", value: 300 },
  ];

  let selectedStreamLabel = $derived(
    !selectedStreamId
      ? "Select a stream"
      : streams.find((s) => s.id === selectedStreamId)?.name || "Select a stream"
  );

  // Upload VOD modal
  let showUploadModal = $state(false);
  let uploadFile = $state<File | null>(null);
  let uploadTitle = $state("");
  let uploadDescription = $state("");
  let uploading = $state(false);
  let uploadProgress = $state(0);
  let uploadStage = $state<
    "idle" | "initializing" | "uploading" | "paused" | "completing" | "processing" | "done"
  >("idle");
  let currentUploadId = $state<string | null>(null);
  let dragActive = $state(false);
  let recoveryOffer = $state<{
    uploadId: string;
    completedParts: number;
    totalParts: number;
  } | null>(null);
  let resumeRequested = $state(false);

  let uploadEngine: UploadEngine | null = null;
  let sessionStore: SessionStore | null = null;
  function getSessionStore(): SessionStore {
    if (!sessionStore) {
      sessionStore =
        typeof globalThis !== "undefined" && "indexedDB" in globalThis
          ? createIndexedDBSessionStore()
          : createMemorySessionStore();
    }
    return sessionStore;
  }

  const unsubscribeAuth = auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadData();
  });

  onDestroy(() => {
    unsubscribeAuth();
    if (searchDebounceTimer) clearTimeout(searchDebounceTimer);
    clipLifecycleSub.unlisten();
    dvrLifecycleSub.unlisten();
    vodLifecycleSub.unlisten();
  });

  async function loadData() {
    try {
      lifecycleRangeResolved = resolveTimeRange(lifecycleRange);

      await Promise.all([
        streamsStore.fetch(),
        reloadArtifacts(),
        artifactEventsStore
          .fetch({
            variables: {
              timeRange: { start: lifecycleRangeResolved.start, end: lifecycleRangeResolved.end },
              first: 25,
            },
          })
          .catch(() => null),
        // Fetch current artifact states for in-progress operations
        artifactStatesStore.fetch({ variables: { first: 25 } }).catch(() => null),
        // Fetch storage events (freeze + relay cache fills)
        storageEventsStore
          .fetch({
            variables: {
              timeRange: { start: lifecycleRangeResolved.start, end: lifecycleRangeResolved.end },
              first: 30,
            },
          })
          .catch(() => null),
      ]);
      // Subsequent filter changes drive their own refetch via the $effect above.
      filterInitialized = true;

      // Start subscriptions for live updates
      if (streams.length > 0) {
        clipLifecycleSub.listen({ streamId: streams[0].id });
        dvrLifecycleSub.listen({ streamId: streams[0].id });
      }
      // VOD lifecycle doesn't filter by stream (uploads are tenant-wide)
      vodLifecycleSub.listen();
    } catch (error) {
      console.error("Failed to load library data:", error);
      toast.error("Failed to load library. Please refresh.");
    }
  }

  async function loadMore() {
    if (loadingMore) return;
    loadingMore = true;
    // Capture the generation this page belongs to. A reset-reload (filter/search/status
    // change) bumps the counter, so if one starts mid-flight we drop this page rather than
    // appending rows from a superseded filter.
    const gen = requestGeneration;

    try {
      // Fetch the NEXT page at offset = rows already loaded, then append. Dedupe by id
      // in case an insert between pages shifted offsets (avoids keyed-each collisions).
      const res = await fetchPage(accumulatedNodes.length, { policy: "NetworkOnly" });
      if (gen !== requestGeneration) return;
      if (resultErrors(res)) {
        toast.error("Failed to load more items.");
        return;
      }
      // Append rows and refresh the connection metadata together, only after the
      // generation is confirmed current, so a superseded page updates neither.
      const connection = res.data?.storageArtifactsConnection;
      const newNodes = connection?.nodes ?? [];
      const seen = new SvelteSet(accumulatedNodes.map((n) => n.id));
      accumulatedNodes = [...accumulatedNodes, ...newNodes.filter((n) => !seen.has(n.id))];
      connectionMeta = {
        hasNextPage: connection?.hasNextPage ?? false,
        totalCount: connection?.totalCount ?? connectionMeta.totalCount,
        kindCounts: connection?.kindCounts ?? connectionMeta.kindCounts,
      };
    } catch (error) {
      console.error("Failed to load more items:", error);
      toast.error("Failed to load more items.");
    } finally {
      loadingMore = false;
    }
  }

  // Create clip
  async function createClip() {
    if (creatingClip) return;

    if (!clipTitle.trim() || !selectedStreamId) {
      toast.warning("Please fill in all required fields");
      return;
    }

    try {
      creatingClip = true;

      const input: Parameters<typeof createClipMutation.mutate>[0]["input"] = {
        streamId: selectedStreamId,
        title: clipTitle.trim(),
        description: clipDescription.trim() || undefined,
      };

      switch (clipMode) {
        case "CLIP_NOW":
          input.mode = ClipCreationMode.CLIP_NOW;
          input.duration = Math.floor(clipDuration);
          break;
        case "DURATION":
          input.mode = ClipCreationMode.DURATION;
          input.startUnix = Math.floor(clipStartTime);
          input.duration = Math.floor(clipDuration);
          break;
        case "ABSOLUTE":
          input.mode = ClipCreationMode.ABSOLUTE;
          input.startUnix = Math.floor(clipStartTime);
          input.stopUnix = Math.floor(clipEndTime);
          break;
      }

      const result = await createClipMutation.mutate({ input });
      const createResult = result.data?.createClip;
      const isError =
        createResult?.__typename === "ValidationError" ||
        createResult?.__typename === "NotFoundError" ||
        createResult?.__typename === "AuthError";

      if (createResult && !isError) {
        toast.success("Clip created successfully!");
        showCreateClipModal = false;
        resetClipForm();
        loadData();
      } else if (createResult) {
        const errorResult = createResult as unknown as { message?: string };
        toast.error(errorResult.message || "Failed to create clip");
      }
    } catch (error) {
      console.error("Failed to create clip:", error);
      toast.error("Failed to create clip. Please try again.");
    } finally {
      creatingClip = false;
    }
  }

  function resetClipForm() {
    clipTitle = "";
    clipDescription = "";
    selectedStreamId = "";
    clipMode = "CLIP_NOW";
    clipDuration = 60;
    clipStartTime = 0;
    clipEndTime = 300;
  }

  // Delete handlers. Every delete is hash-keyed at the service layer (Commodore
  // DeleteClip/DeleteVodAsset/DeleteDVR all take the artifact hash), and the unified
  // node exposes that hash as both `hash` and `deleteId`.
  function confirmDeleteClip(clip: StorageArtifactNode) {
    clipToDelete = clip;
    showDeleteClipModal = true;
  }

  async function deleteClip() {
    if (!clipToDelete) return;
    try {
      deletingClipId = clipToDelete.deleteId;
      const result = await deleteClipMutation.mutate({ id: clipToDelete.deleteId });
      if (result.data?.deleteClip?.__typename === "DeleteSuccess") {
        toast.success("Clip deleted successfully!");
        loadData();
      }
    } catch {
      toast.error("Failed to delete clip.");
    } finally {
      deletingClipId = "";
      showDeleteClipModal = false;
      clipToDelete = null;
    }
  }

  function confirmDeleteDvr(dvr: StorageArtifactNode) {
    dvrToDelete = dvr;
    showDeleteDvrModal = true;
  }

  async function deleteDvr() {
    if (!dvrToDelete) return;
    try {
      deletingDvrHash = dvrToDelete.hash;
      const result = await deleteDvrMutation.mutate({ dvrHash: dvrToDelete.hash });
      if (result.data?.deleteDVR?.__typename === "DeleteSuccess") {
        toast.success("Recording deleted successfully!");
        loadData();
      }
    } catch {
      toast.error("Failed to delete recording.");
    } finally {
      deletingDvrHash = "";
      showDeleteDvrModal = false;
      dvrToDelete = null;
    }
  }

  function confirmDeleteVod(vod: StorageArtifactNode) {
    vodToDelete = vod;
    showDeleteVodModal = true;
  }

  async function deleteVod() {
    if (!vodToDelete) return;
    try {
      deletingVodId = vodToDelete.deleteId;
      const result = await deleteVodMutation.mutate({ id: vodToDelete.deleteId });
      if (result.data?.deleteVodAsset?.__typename === "DeleteSuccess") {
        toast.success("Video deleted successfully!");
        loadData();
      }
    } catch {
      toast.error("Failed to delete video.");
    } finally {
      deletingVodId = "";
      showDeleteVodModal = false;
      vodToDelete = null;
    }
  }

  // Upload VOD
  async function handleFileChosen(file: File) {
    uploadFile = file;
    if (!uploadTitle) {
      uploadTitle = file.name.replace(/\.[^/.]+$/, "");
    }
    // Best-effort recovery offer based on local IndexedDB session.
    try {
      const candidate = await findRecoverable(getSessionStore(), file);
      if (candidate) {
        recoveryOffer = {
          uploadId: candidate.record.uploadId,
          completedParts: candidate.completedParts.length,
          totalParts: candidate.record.totalParts,
        };
      } else {
        recoveryOffer = null;
      }
    } catch {
      recoveryOffer = null;
    }
  }

  function handleFileSelect(event: Event) {
    const target = event.target as HTMLInputElement;
    const file = target.files?.[0];
    if (file) handleFileChosen(file);
  }

  function dropZoneAction(node: HTMLElement) {
    return attachDropZone(node, {
      accept: isVideoFile,
      onEnter: () => (dragActive = true),
      onLeave: () => (dragActive = false),
      onFiles: (files) => {
        if (uploading) return;
        if (files.length > 0) handleFileChosen(files[0]);
      },
    });
  }

  type UploadSessionInputs =
    | {
        kind: "upload";
        uploadId: string;
        partSize: number;
        parts: { partNumber: number; presignedUrl: string }[];
        serverConfirmedParts: { partNumber: number; etag: string }[];
      }
    | {
        kind: "finalized";
        stage: "processing" | "done";
      };

  async function prepareResumeSession(file: File): Promise<UploadSessionInputs | null> {
    if (!resumeRequested || !recoveryOffer) return null;
    const candidate = await findRecoverable(getSessionStore(), file);
    if (!candidate) return null;

    // Verify with the server because the local IndexedDB record may be stale or expired.
    const statusResult = await vodUploadStatusQuery.fetch({
      variables: { uploadId: candidate.record.uploadId },
      policy: "NetworkOnly",
    });
    const data = statusResult.data?.vodUploadStatus;
    if (!data || data.__typename !== "VodUploadStatus") {
      // NotFound / Auth / Validation: drop the local session and fall through to fresh upload.
      await getSessionStore().delete(candidate.record.uploadId);
      toast.info("Previous upload is no longer resumable; starting fresh.");
      return null;
    }
    if (data.state === "EXPIRED") {
      await getSessionStore().delete(candidate.record.uploadId);
      toast.info("Previous upload session expired; starting fresh.");
      return null;
    }
    if (data.state === "PROCESSING" || data.state === "READY") {
      await getSessionStore().delete(candidate.record.uploadId);
      recoveryOffer = null;
      resumeRequested = false;
      toast.info(
        data.state === "READY"
          ? "Previous upload already finished processing."
          : "Previous upload already completed; processing continues in the library."
      );
      return { kind: "finalized", stage: data.state === "READY" ? "done" : "processing" };
    }
    if (data.state === "FAILED" || data.state === "DELETED") {
      await getSessionStore().delete(candidate.record.uploadId);
      toast.info("Previous upload can no longer be resumed; starting fresh.");
      return null;
    }
    return {
      kind: "upload",
      uploadId: candidate.record.uploadId,
      partSize: candidate.record.partSize,
      parts: candidate.record.parts.map((p) => ({
        partNumber: p.partNumber,
        presignedUrl: p.presignedUrl,
      })),
      serverConfirmedParts: data.uploadedParts.map((p) => ({
        partNumber: p.partNumber,
        etag: p.etag,
      })),
    };
  }

  type CompletedUploadParts =
    | { kind: "parts"; parts: { partNumber: number; etag: string }[] }
    | { kind: "finalized"; stage: "processing" | "done" };

  async function reconcileCompletedUploadParts(
    uploadId: string,
    localParts: { partNumber: number; etag: string }[],
    totalParts: number
  ): Promise<CompletedUploadParts> {
    const localComplete =
      localParts.length === totalParts && localParts.every((part) => part.etag.trim() !== "");
    if (localComplete) {
      return { kind: "parts", parts: localParts };
    }

    const statusResult = await vodUploadStatusQuery.fetch({
      variables: { uploadId },
      policy: "NetworkOnly",
    });
    const data = statusResult.data?.vodUploadStatus;
    if (!data || data.__typename !== "VodUploadStatus") {
      throw new Error("Upload reached storage, but the server could not reconcile its parts");
    }
    if (data.state === "PROCESSING" || data.state === "READY") {
      return { kind: "finalized", stage: data.state === "READY" ? "done" : "processing" };
    }

    const uploadedParts = data.uploadedParts.map((p) => ({
      partNumber: p.partNumber,
      etag: p.etag,
    }));
    if (uploadedParts.length !== totalParts || data.missingParts.length > 0) {
      throw new Error("Upload did not reach storage completely; resume the upload to finish it");
    }
    if (uploadedParts.some((part) => part.etag.trim() === "")) {
      throw new Error("Upload reached storage, but part ETags are unavailable");
    }
    return { kind: "parts", parts: uploadedParts };
  }

  async function startUpload() {
    if (!uploadFile) {
      toast.warning("Please select a file to upload");
      return;
    }

    const file = uploadFile;
    uploading = true;
    uploadStage = "initializing";
    uploadProgress = 0;

    let activeUploadId: string | null = null;
    let abortOnFailure = true;
    try {
      let session: UploadSessionInputs | null = await prepareResumeSession(file);
      if (session?.kind === "finalized") {
        uploadStage = session.stage;
        await loadData();
        return;
      }
      if (!session) {
        const initResult = await createVodUploadMutation.mutate({
          input: {
            filename: file.name,
            sizeBytes: file.size,
            contentType: file.type || "video/mp4",
            title: uploadTitle.trim() || undefined,
            description: uploadDescription.trim() || undefined,
          },
        });
        const createResult = initResult.data?.createVodUpload;
        if (createResult?.__typename !== "VodUploadSession") {
          const error = createResult as unknown as { message?: string };
          throw new Error(error?.message || "Failed to initialize upload");
        }
        session = {
          kind: "upload",
          uploadId: createResult.id,
          partSize: createResult.partSize,
          parts: createResult.parts.map((p) => ({
            partNumber: p.partNumber,
            presignedUrl: p.presignedUrl,
          })),
          serverConfirmedParts: [],
        };
      }

      activeUploadId = session.uploadId;
      currentUploadId = activeUploadId;

      const engine = createUploadEngine({
        uploadId: session.uploadId,
        file,
        partSize: session.partSize,
        parts: session.parts,
        store: getSessionStore(),
      });
      uploadEngine = engine;
      if (session.serverConfirmedParts.length > 0) {
        engine.seedCompleted(session.serverConfirmedParts);
      }

      uploadStage = "uploading";

      const transferComplete = new Promise<{ partNumber: number; etag: string }[]>(
        (resolve, reject) => {
          engine.on((event) => {
            switch (event.type) {
              case "progress":
                uploadProgress = event.percent;
                break;
              case "stateChange":
                if (event.state === "paused") uploadStage = "paused";
                else if (event.state === "uploading") uploadStage = "uploading";
                else if (event.state === "failed")
                  reject(new Error("Upload failed; see error event"));
                else if (event.state === "aborted") reject(new Error("Upload aborted"));
                break;
              case "transferComplete":
                resolve(event.parts);
                break;
              case "error":
                // Surfaced via state change; keep latest message in toast on hard failure.
                break;
            }
          });
        }
      );

      engine.start();
      const localCompletedParts = await transferComplete;
      abortOnFailure = false;
      const completed = await reconcileCompletedUploadParts(
        activeUploadId,
        localCompletedParts,
        session.parts.length
      );
      if (completed.kind === "finalized") {
        await getSessionStore().delete(activeUploadId);
        recoveryOffer = null;
        resumeRequested = false;
        uploadStage = completed.stage;
        await loadData();
        return;
      }

      uploadStage = "completing";

      const completeResult = await completeVodUploadMutation.mutate({
        input: { uploadId: activeUploadId, parts: completed.parts },
      });

      if (completeResult.data?.completeVodUpload?.__typename !== "VodAsset") {
        const error = completeResult.data?.completeVodUpload as unknown as { message?: string };
        throw new Error(error?.message || "Failed to complete upload");
      }

      await getSessionStore().delete(activeUploadId);
      recoveryOffer = null;
      resumeRequested = false;
      uploadStage = "processing";
      toast.success("Upload complete — video is being processed.");
      await loadData();
    } catch (error) {
      console.error("Upload failed:", error);
      toast.error(`Upload failed: ${error instanceof Error ? error.message : "Unknown error"}`);
      if (activeUploadId && abortOnFailure) {
        try {
          await abortVodUploadMutation.mutate({ uploadId: activeUploadId });
        } catch {
          // ignore — server will eventually expire the session.
        }
      }
      if (!abortOnFailure && activeUploadId) {
        try {
          const candidate = await findRecoverable(getSessionStore(), file);
          if (candidate?.record.uploadId === activeUploadId) {
            recoveryOffer = {
              uploadId: activeUploadId,
              completedParts: candidate.completedParts.length,
              totalParts: candidate.record.totalParts,
            };
            resumeRequested = true;
          }
        } catch {
          // Recovery is still possible after re-selecting the same file.
        }
      }
      uploadStage = "idle";
    } finally {
      uploading = false;
      uploadEngine = null;
      currentUploadId = null;
      if (abortOnFailure) {
        resumeRequested = false;
      }
    }
  }

  function pauseUpload() {
    uploadEngine?.pause();
  }

  function resumeUpload() {
    uploadEngine?.resume();
  }

  function acceptRecovery() {
    // Marks the next startUpload to resume the existing session: it will query
    // vodUploadStatus to confirm the session is still live, then reuse the cached
    // presigned URLs and skip parts the server has already received.
    resumeRequested = true;
  }

  function dismissRecovery() {
    if (recoveryOffer) {
      void getSessionStore().delete(recoveryOffer.uploadId);
    }
    recoveryOffer = null;
    resumeRequested = false;
  }

  function resetUploadForm() {
    uploadFile = null;
    uploadTitle = "";
    uploadDescription = "";
    uploadProgress = 0;
    uploadStage = "idle";
    currentUploadId = null;
    recoveryOffer = null;
    resumeRequested = false;
  }

  function cancelUpload() {
    if (uploadEngine) {
      uploadEngine.abort();
      uploadEngine = null;
    }
    if (currentUploadId && uploading) {
      abortVodUploadMutation.mutate({ uploadId: currentUploadId });
    }
    resetUploadForm();
    showUploadModal = false;
  }

  // Helpers
  function formatDuration(seconds: number | null): string {
    if (!seconds) return "N/A";
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    return `${minutes}:${remainingSeconds.toString().padStart(2, "0")}`;
  }

  function formatDate(dateString: string | null): string {
    if (!dateString) return "N/A";
    return new Date(dateString).toLocaleDateString();
  }

  function lifecycleStateForPlaybackId(playbackId: string | null | undefined) {
    return playbackId ? artifactStatesByPlaybackId.get(playbackId) : undefined;
  }

  function normalizeArtifactType(type: string | null | undefined): ArtifactType {
    const t = type?.toLowerCase();
    if (t === "clip" || t === "clips") return "clips";
    if (t === "dvr") return "dvr";
    if (t === "vod") return "vod";
    return "other"; // fail closed: never mislabel an unknown kind as clips
  }

  function isTerminalArtifactStage(stage: string | null | undefined): boolean {
    const s = stage?.toLowerCase();
    return [
      "completed",
      "complete",
      "done",
      "ready",
      "synced",
      "deleted",
      "evicted",
      "failed",
      "failed_terminal",
      "error",
      "lost_local",
    ].includes(s || "");
  }

  function artifactRowStage(
    connectionStage: string | null | undefined,
    feedStage: string | null | undefined
  ): string {
    const connection = connectionStage?.toLowerCase();
    const feed = feedStage?.toLowerCase();

    // The durable catalog is the source of truth for a REGISTERED row. The analytics feed has no
    // revision relationship to the catalog, so a stale terminal event (e.g. an old `completed`)
    // must never override the catalog — that could show a catalog-`failed` row as completed, even
    // inside the server-filtered Failed view.
    const registered = !!connection && connection !== "registry" && connection !== "unknown";
    if (registered) {
      // A terminal catalog status wins outright. While the catalog is still in-progress, the feed
      // may only REFINE the live progress display with another non-terminal stage — it may never
      // declare a terminal state the catalog hasn't itself reached.
      if (isTerminalArtifactStage(connection)) return connection!;
      if (feed && !isTerminalArtifactStage(feed)) return feed;
      return connection!;
    }

    // Uncatalogued / placeholder rows: fall back to the feed for progress (or an in-progress
    // placeholder), then to whatever the connection carried.
    return feed || connection || "unknown";
  }

  function displayArtifactStage(stage: string): string {
    const s = stage.toLowerCase();
    if (s === "done" || s === "complete" || s === "synced") return "completed";
    if (s === "failed_terminal" || s === "error" || s === "lost_local") return "failed";
    return s;
  }

  function getStatusColor(status: string): string {
    const s = status.toLowerCase();
    if (["available", "completed", "ready"].includes(s))
      return "text-success bg-success/10 border-success/20";
    if (
      [
        "started",
        "processing",
        "recording",
        "uploading",
        "requested",
        "queued",
        "progress",
      ].includes(s)
    )
      return "text-warning bg-warning/10 border-warning/20";
    if (s === "failed") return "text-destructive bg-destructive/10 border-destructive/20";
    if (s === "deleted") return "text-muted-foreground bg-muted border-border opacity-70";
    return "text-muted-foreground bg-muted border-border";
  }

  // Expired assets now come back in the catalog with status "expired"; render a proper
  // label and never let a past-expiry row read as a live "Ready"/"Completed" state.
  function statusLabel(status: string, expired: boolean): string {
    if (expired || status.toLowerCase() === "expired") return "Expired";
    return status || "Unknown";
  }

  function getTypeColor(type: ArtifactType): string {
    if (type === "clips") return "text-primary bg-primary/10 border-primary/20";
    if (type === "dvr") return "text-info bg-info/10 border-info/20";
    if (type === "vod") return "text-success bg-success/10 border-success/20";
    return "text-muted-foreground bg-muted border-border";
  }

  function getTypeLabel(type: ArtifactType): string {
    if (type === "clips") return "Clip";
    if (type === "dvr") return "DVR";
    if (type === "vod") return "VOD";
    return "Unknown";
  }

  const playableArtifactStages = new Set([
    "available",
    "completed",
    "complete",
    "done",
    "ready",
    "synced",
  ]);
  const blockedArtifactStages = new Set([
    "registry",
    "requested",
    "queued",
    "processing",
    "progress",
    "uploading",
    "pending",
    "unknown",
  ]);
  const failedArtifactStages = new Set([
    "deleted",
    "evicted",
    "failed",
    "failed_terminal",
    "error",
    "lost_local",
  ]);

  function hasPlayableStorage(artifact: UnifiedArtifact): boolean {
    return (
      artifact.hasLocalCopy === true ||
      artifact.isSynced === true ||
      artifact.isFinalized === true ||
      artifact.syncStatus?.toLowerCase() === "synced"
    );
  }

  function hasRollingDvrMedia(artifact: UnifiedArtifact): boolean {
    return (
      artifact.segmentCount != null ||
      (artifact.sizeBytes != null && artifact.sizeBytes > 0) ||
      hasPlayableStorage(artifact)
    );
  }

  function canPlayArtifact(artifact: UnifiedArtifact): boolean {
    // Fail closed on an unknown kind: we can't pick a correct player/protocol for it, and every
    // downstream render (download link, PlaybackProtocols) is gated on this, so an "other" asset is
    // never presented as playable-as-VOD.
    if (artifact.type === "other") return false;
    if (artifact.type === "dvr") {
      if (!artifact.playbackId && !artifact.hash) return false;
    } else if (!artifact.playbackId) return false;

    if (isExpired(artifact.expiresAt)) return false;

    const status = artifact.status.toLowerCase();
    if (failedArtifactStages.has(status)) return false;
    if (artifact.type === "dvr" && (status === "started" || status === "recording")) {
      return hasRollingDvrMedia(artifact);
    }
    if (blockedArtifactStages.has(status) && !hasPlayableStorage(artifact)) return false;
    if (playableArtifactStages.has(status)) return true;
    return hasPlayableStorage(artifact);
  }

  function dvrViewUrl(artifact: UnifiedArtifact) {
    const id = artifact.playbackId || artifact.hash;
    if (!id) return null;
    const params = new URLSearchParams({ type: "dvr", id });
    return `/view?${params.toString()}`;
  }

  function playArtifact(artifact: UnifiedArtifact) {
    if (!canPlayArtifact(artifact)) return;
    if (artifact.type === "dvr") {
      const url = dvrViewUrl(artifact);
      if (!url) return;
      // eslint-disable-next-line svelte/no-navigation-without-resolve
      goto(url);
      return;
    }
    if (artifact.playbackId) {
      const url = `/view?id=${artifact.playbackId}`;
      // eslint-disable-next-line svelte/no-navigation-without-resolve
      if (url) goto(url);
    }
  }

  function handleTypeChange(type: ArtifactType) {
    typeFilter = type;
    const url = new URL(window.location.href);
    if (type === "all") {
      url.searchParams.delete("type");
    } else {
      url.searchParams.set("type", type);
    }
    goto(resolve(`${url.pathname}${url.search}` as "/"), { replaceState: true, noScroll: true });
  }

  // Icons
  const FolderOpenIcon = getIconComponent("FolderOpen");
  const ScissorsIcon = getIconComponent("Scissors");
  const FilmIcon = getIconComponent("Film");
  const VideoIcon = getIconComponent("Video");
  const UploadIcon = getIconComponent("Upload");
  const DownloadIcon = getIconComponent("Download");
  const Share2Icon = getIconComponent("Share2");
  const Trash2Icon = getIconComponent("Trash2");
  const FilterIcon = getIconComponent("Filter");
  const SearchIcon = getIconComponent("Search");
  const ChevronUpIcon = getIconComponent("ChevronUp");
  const SnowflakeIcon = getIconComponent("Snowflake");
  const ZapIcon = getIconComponent("Zap");
  const ActivityIcon = getIconComponent("Activity");
  const CloudUploadIcon = getIconComponent("CloudUpload");
  const FileVideoIcon = getIconComponent("FileVideo");
  const CloudIcon = getIconComponent("Cloud");
  const LoaderIcon = getIconComponent("Loader");
  const BarChart2Icon = getIconComponent("BarChart2");
  const Maximize2Icon = getIconComponent("Maximize2");
</script>

<svelte:head>
  <title>Library - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col overflow-hidden">
  <!-- Fixed Page Header -->
  <div
    class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 z-10 bg-background"
  >
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <FolderOpenIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Library</h1>
          <p class="text-sm text-muted-foreground">
            Clips, recordings, and VOD assets in one place
          </p>
        </div>
      </div>
      <div class="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          class="gap-2 h-8"
          onclick={() => (showCreateClipModal = true)}
          disabled={streams.length === 0}
        >
          <ScissorsIcon class="w-3.5 h-3.5" />
          Create Clip
        </Button>
        <Button
          variant="outline"
          size="sm"
          class="gap-2 h-8"
          onclick={() => (showUploadModal = true)}
        >
          <UploadIcon class="w-3.5 h-3.5" />
          Upload VOD
        </Button>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto bg-background/50">
    {#if loading}
      <GridSeam cols={4} stack="2x2" flush={true} class="min-h-full content-start">
        {#each Array.from({ length: 8 }) as _, i (i)}
          <div class="slab h-full !p-0">
            <div class="slab-header">
              <div class="h-4 bg-muted rounded w-3/4 animate-pulse"></div>
            </div>
            <div class="slab-body--padded">
              <div class="space-y-3">
                <div class="h-4 bg-muted rounded w-full animate-pulse"></div>
                <div class="h-4 bg-muted rounded w-1/2 animate-pulse"></div>
              </div>
            </div>
          </div>
        {/each}
      </GridSeam>
    {:else}
      <div class="page-transition">
        <!-- Stats Bar -->
        <GridSeam
          cols={4}
          stack="2x2"
          surface="panel"
          flush={true}
          class="mb-0 min-h-full content-start"
        >
          <div>
            <DashboardMetricCard
              icon={FolderOpenIcon}
              iconColor="text-primary"
              value={statTotalAll}
              valueColor="text-primary"
              label="Total Items"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={ScissorsIcon}
              iconColor="text-primary"
              value={statTotalClips}
              valueColor="text-primary"
              label="Clips"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={FilmIcon}
              iconColor="text-info"
              value={statTotalDvr}
              valueColor="text-info"
              label="Recordings"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={VideoIcon}
              iconColor="text-success"
              value={statTotalVod}
              valueColor="text-success"
              label="VOD Assets"
            />
          </div>
        </GridSeam>

        <!-- Main Content -->
        <div class="dashboard-grid">
          <!-- Filters Slab -->
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <FilterIcon class="w-4 h-4 text-info" />
                <h3>Filters</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              <div class="grid grid-cols-1 md:grid-cols-3 gap-4">
                <!-- Type Filter -->
                <div>
                  <span class="block text-sm font-medium text-muted-foreground mb-2">Type</span>
                  <div class="flex border border-border rounded-md overflow-hidden">
                    <button
                      type="button"
                      class="flex-1 px-3 py-2 text-xs font-medium transition-colors {typeFilter ===
                      'all'
                        ? 'bg-primary text-primary-foreground'
                        : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
                      onclick={() => handleTypeChange("all")}
                    >
                      All
                    </button>
                    <button
                      type="button"
                      class="flex-1 px-3 py-2 text-xs font-medium transition-colors border-x border-border {typeFilter ===
                      'clips'
                        ? 'bg-primary text-primary-foreground'
                        : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
                      onclick={() => handleTypeChange("clips")}
                    >
                      Clips
                    </button>
                    <button
                      type="button"
                      class="flex-1 px-3 py-2 text-xs font-medium transition-colors border-r border-border {typeFilter ===
                      'dvr'
                        ? 'bg-primary text-primary-foreground'
                        : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
                      onclick={() => handleTypeChange("dvr")}
                    >
                      DVR
                    </button>
                    <button
                      type="button"
                      class="flex-1 px-3 py-2 text-xs font-medium transition-colors {typeFilter ===
                      'vod'
                        ? 'bg-primary text-primary-foreground'
                        : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
                      onclick={() => handleTypeChange("vod")}
                    >
                      VOD
                    </button>
                  </div>
                </div>

                <!-- Search -->
                <div>
                  <label for="search" class="block text-sm font-medium text-muted-foreground mb-2"
                    >Search</label
                  >
                  <div class="relative">
                    <SearchIcon
                      class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground"
                    />
                    <Input
                      id="search"
                      type="text"
                      bind:value={searchQuery}
                      placeholder="Search by title, hash, or stream..."
                      class="w-full pl-10"
                    />
                  </div>
                </div>

                <!-- Status Filter -->
                <div>
                  <label
                    for="status-filter"
                    class="block text-sm font-medium text-muted-foreground mb-2">Status</label
                  >
                  <Select bind:value={statusFilter} type="single">
                    <SelectTrigger id="status-filter" class="w-full">
                      {statusFilter === "all"
                        ? "All Statuses"
                        : statusFilter === "processing"
                          ? "Processing"
                          : statusFilter === "ready"
                            ? "Ready"
                            : "Failed"}
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">All Statuses</SelectItem>
                      <SelectItem value="processing">Processing</SelectItem>
                      <SelectItem value="ready">Ready</SelectItem>
                      <SelectItem value="failed">Failed</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
            </div>
          </div>

          <!-- Assets Table -->
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <FolderOpenIcon class="w-4 h-4 text-info" />
                <h3>Your Library</h3>
                <span class="text-xs text-muted-foreground">({filteredArtifacts.length} items)</span
                >
              </div>
            </div>
            <div class="slab-body--flush">
              {#if loadError}
                <div
                  class="mx-4 mt-4 rounded-md border border-destructive/20 bg-destructive/10 px-3 py-2 text-sm text-destructive"
                  role="alert"
                >
                  {loadError}
                </div>
              {/if}
              {#if filteredArtifacts.length === 0 && !catalogLoaded}
                <!-- Never successfully loaded the catalog: don't assert an empty library. If it was
                     an error, loadError above explains; otherwise this is the initial load. -->
                {#if !loadError}
                  <div class="p-8">
                    <EmptyState
                      iconName="FolderOpen"
                      title="Loading your library…"
                      description="Fetching your clips, recordings, and VOD assets."
                    />
                  </div>
                {/if}
              {:else if filteredArtifacts.length === 0}
                <div class="p-8">
                  <EmptyState
                    iconName="FolderOpen"
                    title="No items found"
                    description={searchQuery || typeFilter !== "all" || statusFilter !== "all"
                      ? "Try adjusting your filters."
                      : "Create clips from streams or upload VOD content."}
                    actionText={searchQuery || typeFilter !== "all" || statusFilter !== "all"
                      ? "Clear Filters"
                      : undefined}
                    onAction={searchQuery || typeFilter !== "all" || statusFilter !== "all"
                      ? () => {
                          searchQuery = "";
                          typeFilter = "all";
                          statusFilter = "all";
                        }
                      : undefined}
                  />
                </div>
              {:else}
                <div class="overflow-x-auto">
                  <Table class="w-full">
                    <TableHeader>
                      <TableRow>
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider w-[120px]"
                          >Actions</TableHead
                        >
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider w-[70px]"
                          >Type</TableHead
                        >
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                          >Title</TableHead
                        >
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                          >Source</TableHead
                        >
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                          >Status</TableHead
                        >
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                          >Duration</TableHead
                        >
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                          >Size</TableHead
                        >
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                          >Created</TableHead
                        >
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                          >Expires</TableHead
                        >
                      </TableRow>
                    </TableHeader>
                    <TableBody class="divide-y divide-border">
                      {#each filteredArtifacts as artifact (artifact.id)}
                        {@const isExpiredArtifact = isExpired(artifact.expiresAt)}
                        {@const isDeleted = artifact.status.toLowerCase() === "deleted"}
                        {@const isChapter = artifact.rawData.kind === "CHAPTER"}
                        <TableRow
                          class="transition-colors group {isDeleted || isExpiredArtifact
                            ? 'opacity-60 bg-muted/30 cursor-not-allowed'
                            : canPlayArtifact(artifact)
                              ? 'hover:bg-muted/50 cursor-pointer'
                              : 'cursor-default'}"
                          onclick={() => canPlayArtifact(artifact) && playArtifact(artifact)}
                        >
                          <!-- Actions -->
                          <TableCell
                            class="px-4 py-2 align-middle"
                            onclick={(e) => e.stopPropagation()}
                          >
                            <div class="flex items-center gap-1">
                              {#if isExpiredArtifact}
                                <span class="text-[10px] text-muted-foreground px-2 italic"
                                  >Expired</span
                                >
                              {:else if isDeleted}
                                <span class="text-[10px] text-muted-foreground px-2 italic"
                                  >Deleted</span
                                >
                              {:else}
                                {#if canPlayArtifact(artifact) && artifact.type !== "dvr" && artifact.playbackId}
                                  {@const urls = getContentDeliveryUrls(
                                    artifact.playbackId,
                                    artifact.type === "clips" ? "clip" : "vod"
                                  )}
                                  <Button
                                    href={urls.primary.mp4}
                                    target="_blank"
                                    rel="noopener noreferrer"
                                    variant="ghost"
                                    size="sm"
                                    class="h-7 w-7 p-0 text-muted-foreground hover:text-primary"
                                    title="Download MP4"
                                  >
                                    <DownloadIcon class="w-3.5 h-3.5" />
                                  </Button>
                                {/if}
                                <!-- Open + analytics are keyed on the artifact hash, not on
                                     playability, so a still-processing or failed asset is still
                                     inspectable. -->
                                {#if artifact.hash}
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    class="h-7 w-7 p-0 text-muted-foreground hover:text-foreground"
                                    title="Open asset"
                                    onclick={(e) => {
                                      e.stopPropagation();
                                      goto(resolve(`/library/${artifact.hash}`));
                                    }}
                                  >
                                    <Maximize2Icon class="w-3.5 h-3.5" />
                                  </Button>
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    class="h-7 w-7 p-0 text-muted-foreground hover:text-foreground"
                                    title="Asset analytics"
                                    onclick={(e) => {
                                      e.stopPropagation();
                                      goto(resolve(`/library/${artifact.hash}/analytics`));
                                    }}
                                  >
                                    <BarChart2Icon class="w-3.5 h-3.5" />
                                  </Button>
                                {/if}
                                {#if !canPlayArtifact(artifact)}
                                  <span class="text-[10px] text-warning animate-pulse px-2"
                                    >Processing...</span
                                  >
                                {/if}
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  class="h-7 w-7 p-0 text-muted-foreground hover:text-foreground"
                                  title={expandedArtifact === artifact.id ? "Hide" : "Details"}
                                  onclick={() =>
                                    (expandedArtifact =
                                      expandedArtifact === artifact.id ? null : artifact.id)}
                                >
                                  {#if expandedArtifact === artifact.id}
                                    <ChevronUpIcon class="w-3.5 h-3.5" />
                                  {:else}
                                    <Share2Icon class="w-3.5 h-3.5" />
                                  {/if}
                                </Button>
                                <!-- A DVR chapter is managed via its parent recording; deleting it
                                     through the VOD path would orphan the recording's ledger, so
                                     the backend rejects it. Withhold the action entirely. -->
                                {#if !isChapter}
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    class="h-7 w-7 p-0 text-muted-foreground hover:text-destructive opacity-0 group-hover:opacity-100 transition-opacity focus:opacity-100"
                                    title="Delete"
                                    onclick={() => {
                                      if (artifact.type === "clips")
                                        confirmDeleteClip(artifact.rawData);
                                      else if (artifact.type === "dvr")
                                        confirmDeleteDvr(artifact.rawData);
                                      else if (artifact.type === "vod")
                                        confirmDeleteVod(artifact.rawData);
                                    }}
                                  >
                                    <Trash2Icon class="w-3.5 h-3.5" />
                                  </Button>
                                {/if}
                              {/if}
                            </div>
                          </TableCell>

                          <!-- Type -->
                          <TableCell class="px-4 py-2">
                            <span
                              class="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border {getTypeColor(
                                artifact.type
                              )}"
                            >
                              {getTypeLabel(artifact.type)}
                            </span>
                          </TableCell>

                          <!-- Title -->
                          <TableCell class="px-4 py-2">
                            <div class="flex items-center gap-3">
                              <SpriteThumbnail
                                assetId={artifact.hash}
                                posterUrl={artifact.thumbnailAssets?.posterUrl ?? null}
                                spriteVttUrl={artifact.thumbnailAssets?.spriteVttUrl ?? undefined}
                                spriteJpgUrl={artifact.thumbnailAssets?.spriteJpgUrl ?? undefined}
                              />
                              <div class="flex flex-col min-w-0">
                                <div
                                  class="text-sm font-medium text-foreground truncate max-w-xs group-hover:text-primary transition-colors"
                                  title={artifact.title}
                                >
                                  {artifact.title}
                                </div>
                                {#if artifact.description}
                                  <div
                                    class="text-[11px] text-muted-foreground truncate max-w-xs"
                                    title={artifact.description}
                                  >
                                    {artifact.description}
                                  </div>
                                {/if}
                                <div class="text-[10px] text-muted-foreground font-mono">
                                  {artifact.hash?.slice(0, 8) || "N/A"}...
                                </div>
                              </div>
                            </div>
                          </TableCell>

                          <!-- Source -->
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {artifact.displayStreamId || "Uploaded"}
                          </TableCell>

                          <!-- Status -->
                          <TableCell class="px-4 py-2">
                            <div class="flex items-center gap-2 flex-wrap">
                              <span
                                class="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border {getStatusColor(
                                  isExpiredArtifact ? 'expired' : artifact.status
                                )}"
                              >
                                {statusLabel(artifact.status, isExpiredArtifact)}
                              </span>
                              <!-- Durable (S3) state: freezing → frozen → synced is a genuine
                                   progression, so these are mutually exclusive. -->
                              {#if artifact.storageLocation === "freezing"}
                                <span
                                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border text-cyan-400 bg-cyan-400/10 border-cyan-400/20"
                                >
                                  <LoaderIcon class="w-3 h-3 animate-spin" />
                                  Freezing...
                                </span>
                              {:else if artifact.isSynced && artifact.hasLocalCopy === false}
                                <span
                                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border text-blue-400 bg-blue-400/10 border-blue-400/20"
                                >
                                  <SnowflakeIcon class="w-3 h-3" />
                                  Frozen
                                </span>
                              {:else if artifact.isSynced}
                                <span
                                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border text-emerald-400 bg-emerald-400/10 border-emerald-400/20"
                                >
                                  <CloudIcon class="w-3 h-3" />
                                  Synced
                                </span>
                              {/if}
                              <!-- A present local node copy is INDEPENDENT of durable state — an asset
                                   can be synced/frozen AND have a local copy at once, so it's a separate badge. -->
                              {#if artifact.hasLocalCopy}
                                <span
                                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border text-amber-400 bg-amber-400/10 border-amber-400/20"
                                >
                                  <ZapIcon class="w-3 h-3" />
                                  Local copy
                                </span>
                              {/if}
                              {#if artifact.isFinalized}
                                <span
                                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border text-violet-400 bg-violet-400/10 border-violet-400/20"
                                >
                                  <ActivityIcon class="w-3 h-3" />
                                  Finalized
                                </span>
                              {/if}
                            </div>
                            {#if artifact.errorMessage && artifact.status === "failed"}
                              <p
                                class="text-[11px] text-destructive mt-1 max-w-xs"
                                title={artifact.errorMessage}
                              >
                                {artifact.errorMessage}
                              </p>
                            {/if}
                          </TableCell>

                          <!-- Duration -->
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {formatDuration(artifact.duration)}
                          </TableCell>

                          <!-- Size -->
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {artifact.sizeBytes ? formatBytes(artifact.sizeBytes) : "N/A"}
                          </TableCell>

                          <!-- Created -->
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {formatDate(artifact.createdAt)}
                          </TableCell>

                          <!-- Expires -->
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {#if isChapter}
                              <!-- A chapter's retention follows its parent recording; it can't be
                                   edited independently, so show the expiry as static text. -->
                              <span>{formatExpiry(artifact.expiresAt)}</span>
                            {:else}
                              <button
                                type="button"
                                class="text-left hover:text-primary hover:underline"
                                title="Edit retention"
                                onclick={(e) => {
                                  e.stopPropagation();
                                  openRetentionEditor(artifact);
                                }}
                              >
                                {formatExpiry(artifact.expiresAt)}
                              </button>
                            {/if}
                          </TableCell>
                        </TableRow>

                        <!-- Expanded Share Row -->
                        {#if expandedArtifact === artifact.id && canPlayArtifact(artifact)}
                          <TableRow class="bg-muted/5 border-t-0">
                            <TableCell
                              colspan={9}
                              class="px-4 py-4 cursor-default"
                              onclick={(e) => e.stopPropagation()}
                            >
                              <div class="pl-4 border-l-2 border-primary/20">
                                {#if artifact.type === "dvr"}
                                  <div class="flex flex-col gap-2">
                                    <p class="text-sm text-muted-foreground">
                                      Open the live DVR window. Finalized chapters appear as VOD
                                      artifacts for historical playback.
                                    </p>
                                    <Button
                                      variant="ghost"
                                      size="sm"
                                      class="w-fit gap-2 border border-border/30"
                                      onclick={() => playArtifact(artifact)}
                                    >
                                      Open DVR
                                    </Button>
                                  </div>
                                {:else if artifact.playbackId}
                                  <PlaybackProtocols
                                    contentId={artifact.playbackId}
                                    contentType={artifact.type === "clips" ? "clip" : "vod"}
                                    showPrimary={true}
                                    showAdditional={true}
                                  />
                                {/if}
                              </div>
                            </TableCell>
                          </TableRow>
                        {/if}
                      {/each}
                    </TableBody>
                  </Table>
                </div>
                {#if hasMoreItems}
                  <div class="slab-actions">
                    <Button
                      variant="ghost"
                      class="w-full"
                      onclick={loadMore}
                      disabled={loadingMore}
                    >
                      {loadingMore ? "Loading..." : "Load More"}
                    </Button>
                  </div>
                {/if}
              {/if}
            </div>
          </div>

          <!-- In Progress Artifacts -->
          {#if inProgressArtifacts.length > 0}
            <div class="slab col-span-full">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <LoaderIcon class="w-4 h-4 text-warning animate-spin" />
                  <h3>In Progress</h3>
                  <span class="text-xs text-muted-foreground"
                    >({inProgressArtifacts.length} active)</span
                  >
                </div>
              </div>
              <div class="slab-body--padded">
                <div class="space-y-3">
                  {#each inProgressArtifacts as artifact (artifact.cursor)}
                    <div
                      class="flex items-center gap-4 p-3 border border-border/30 bg-muted/20 rounded"
                    >
                      <div class="flex-1">
                        <div class="flex items-center gap-2 mb-1">
                          <span
                            class="text-xs px-1.5 py-0.5 rounded font-mono {artifact.contentType ===
                            'clip'
                              ? 'bg-primary/20 text-primary'
                              : artifact.contentType === 'dvr'
                                ? 'bg-success/20 text-success'
                                : 'bg-info/20 text-info'}"
                          >
                            {artifact.contentType?.toUpperCase()}
                          </span>
                          <span class="text-xs px-1.5 py-0.5 rounded bg-warning/20 text-warning">
                            {artifact.stage}
                          </span>
                        </div>
                        <div class="flex items-center gap-2">
                          <Progress value={artifact.progressPercent ?? 0} class="flex-1 h-2" />
                          <span class="text-xs font-mono text-muted-foreground w-12 text-right">
                            {artifact.progressPercent ?? 0}%
                          </span>
                        </div>
                        {#if artifact.errorMessage}
                          <p class="text-xs text-destructive mt-1">{artifact.errorMessage}</p>
                        {/if}
                      </div>
                      {#if artifact.processingNodeId}
                        <span class="text-[10px] text-muted-foreground font-mono">
                          on {artifact.processingNodeId}
                        </span>
                      {/if}
                    </div>
                  {/each}
                </div>
              </div>
            </div>
          {/if}

          <!-- Lifecycle Events -->
          <div class="slab col-span-full">
            <div class="slab-header flex items-center justify-between">
              <div class="flex items-center gap-2">
                <ActivityIcon class="w-4 h-4 text-info" />
                <h3>Artifact Lifecycle</h3>
              </div>
              <Select
                value={lifecycleRange}
                onValueChange={(v) => (lifecycleRange = v)}
                type="single"
              >
                <SelectTrigger class="min-w-[140px]">
                  {lifecycleRangeOptions.find((opt) => opt.value === lifecycleRange)?.label ??
                    "Last 7 Days"}
                </SelectTrigger>
                <SelectContent>
                  {#each lifecycleRangeOptions as option (option.value)}
                    <SelectItem value={option.value}>{option.label}</SelectItem>
                  {/each}
                </SelectContent>
              </Select>
            </div>
            {#if mergedLifecycleEvents.length > 0}
              <div class="slab-body--flush max-h-80 overflow-y-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Time</TableHead>
                      <TableHead>Type</TableHead>
                      <TableHead>Stage</TableHead>
                      <TableHead>Message</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {#each mergedLifecycleEvents.slice(0, lifecycleDisplayCount) as event, i (`${event.timestamp}-${event.eventKey}-${i}`)}
                      <TableRow>
                        <TableCell class="text-xs text-muted-foreground font-mono"
                          >{formatTimestamp(event.timestamp)}</TableCell
                        >
                        <TableCell>
                          <span
                            class="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border {getTypeColor(
                              event.type
                            )}"
                          >
                            {getTypeLabel(event.type)}
                          </span>
                        </TableCell>
                        <TableCell class="text-xs">{event.stage}</TableCell>
                        <TableCell class="text-xs text-muted-foreground"
                          >{event.message ?? "—"}</TableCell
                        >
                      </TableRow>
                    {/each}
                  </TableBody>
                </Table>
              </div>
              {#if mergedLifecycleEvents.length > lifecycleDisplayCount}
                <div class="slab-actions">
                  <Button
                    variant="ghost"
                    class="w-full"
                    onclick={() => (lifecycleDisplayCount += 20)}
                  >
                    Load More Events
                  </Button>
                </div>
              {/if}
            {:else}
              <div class="slab-body--padded text-center">
                <p class="text-muted-foreground py-6">
                  No lifecycle events in {lifecycleRangeResolved.label.toLowerCase()}
                </p>
              </div>
            {/if}
          </div>

          <!-- Storage Activity (Freeze + Relay Cache Fill Events) -->
          {#if storageEvents.length > 0}
            <div class="slab col-span-full">
              <div class="slab-header">
                <div class="flex items-center gap-2">
                  <SnowflakeIcon class="w-4 h-4 text-info" />
                  <h3>Storage Activity</h3>
                  <span class="text-xs text-muted-foreground">({storageEvents.length} events)</span>
                </div>
              </div>
              <div class="slab-body--flush max-h-60 overflow-y-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>Time</TableHead>
                      <TableHead>Action</TableHead>
                      <TableHead>Type</TableHead>
                      <TableHead>Asset</TableHead>
                      <TableHead>Size</TableHead>
                      <TableHead>Node</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {#each storageEvents as event (event.id)}
                      <TableRow>
                        <TableCell class="text-xs text-muted-foreground font-mono">
                          {formatTimestamp(event.timestamp)}
                        </TableCell>
                        <TableCell>
                          <span
                            class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium {event.action ===
                            'synced'
                              ? 'bg-info/20 text-info border border-info/30'
                              : 'bg-warning/20 text-warning border border-warning/30'}"
                          >
                            {#if event.action === "synced"}
                              <SnowflakeIcon class="w-3 h-3" />
                            {:else}
                              <ZapIcon class="w-3 h-3" />
                            {/if}
                            {event.action}
                          </span>
                        </TableCell>
                        <TableCell>
                          <span class="text-xs text-muted-foreground">{event.assetType}</span>
                        </TableCell>
                        <TableCell class="font-mono text-xs">
                          {event.assetHash?.slice(0, 12)}...
                        </TableCell>
                        <TableCell class="text-xs">
                          {formatBytes(event.sizeBytes ?? 0)}
                        </TableCell>
                        <TableCell class="text-xs text-muted-foreground font-mono">
                          {event.nodeId || "-"}
                        </TableCell>
                      </TableRow>
                    {/each}
                  </TableBody>
                </Table>
              </div>
            </div>
          {/if}
        </div>
      </div>
    {/if}
  </div>
</div>

<!-- Delete Clip Modal — adapt the unified node to the modal's {id,title,clipHash} shape. -->
<DeleteClipModal
  open={showDeleteClipModal && !!clipToDelete}
  clip={clipToDelete
    ? { id: clipToDelete.deleteId, title: clipToDelete.title, clipHash: clipToDelete.hash }
    : null}
  deleting={!!deletingClipId}
  onConfirm={deleteClip}
  onCancel={() => {
    showDeleteClipModal = false;
    clipToDelete = null;
  }}
/>

<!-- Delete DVR Modal — streamTitle drives the modal's source label. -->
<DeleteRecordingModal
  open={showDeleteDvrModal && !!dvrToDelete}
  recording={dvrToDelete
    ? {
        dvrHash: dvrToDelete.hash,
        streamId: dvrToDelete.streamId,
        sourceStreamId: dvrToDelete.streamTitle,
      }
    : null}
  deleting={!!deletingDvrHash}
  onConfirm={deleteDvr}
  onCancel={() => {
    showDeleteDvrModal = false;
    dvrToDelete = null;
  }}
/>

<!-- Delete VOD Modal -->
<Dialog
  open={showDeleteVodModal}
  onOpenChange={(v) => {
    if (!deletingVodId) {
      showDeleteVodModal = v;
      if (!v) vodToDelete = null;
    }
  }}
>
  <DialogContent
    class="max-w-sm rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-destructive"
        >Delete Video</DialogTitle
      >
      <DialogDescription class="text-xs text-muted-foreground/70"
        >This action cannot be undone.</DialogDescription
      >
    </DialogHeader>
    <div class="slab-body--padded">
      <p class="text-sm text-foreground">
        Are you sure you want to delete <strong
          >{vodToDelete?.title || vodToDelete?.secondaryLabel || "this video"}</strong
        >?
      </p>
    </div>
    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)]"
        onclick={() => {
          showDeleteVodModal = false;
          vodToDelete = null;
        }}
        disabled={!!deletingVodId}>Cancel</Button
      >
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 text-destructive"
        onclick={deleteVod}
        disabled={!!deletingVodId}>{deletingVodId ? "Deleting..." : "Delete"}</Button
      >
    </DialogFooter>
  </DialogContent>
</Dialog>

{#if retentionTarget}
  <AssetRetentionDialog
    bind:open={showRetentionDialog}
    assetType={retentionTarget.type}
    assetId={retentionTarget.id}
    assetName={retentionTarget.name}
    currentExpiresAt={retentionTarget.expiresAt}
    onClose={() => {
      showRetentionDialog = false;
      retentionTarget = null;
    }}
    onSaved={refetchLibraryAfterRetention}
  />
{/if}

<!-- Create Clip Modal -->
<Dialog open={showCreateClipModal} onOpenChange={(v) => (showCreateClipModal = v)}>
  <DialogContent
    class="max-w-md rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground"
        >Create New Clip</DialogTitle
      >
      <DialogDescription class="text-xs text-muted-foreground/70"
        >Choose a stream and time range.</DialogDescription
      >
    </DialogHeader>

    <form
      id="create-clip-form"
      class="slab-body--padded space-y-4"
      onsubmit={(e) => {
        e.preventDefault();
        createClip();
      }}
    >
      <!-- Mode Tabs -->
      <div class="space-y-2">
        <span class="block text-sm font-medium text-muted-foreground mb-2">Clipping Mode</span>
        <div class="flex border border-border rounded-md overflow-hidden">
          <button
            type="button"
            class="flex-1 px-3 py-2 text-sm font-medium transition-colors {clipMode === 'CLIP_NOW'
              ? 'bg-primary text-primary-foreground'
              : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
            onclick={() => (clipMode = "CLIP_NOW")}>Clip Now</button
          >
          <button
            type="button"
            class="flex-1 px-3 py-2 text-sm font-medium transition-colors border-x border-border {clipMode ===
            'DURATION'
              ? 'bg-primary text-primary-foreground'
              : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
            onclick={() => (clipMode = "DURATION")}>Duration</button
          >
          <button
            type="button"
            class="flex-1 px-3 py-2 text-sm font-medium transition-colors {clipMode === 'ABSOLUTE'
              ? 'bg-primary text-primary-foreground'
              : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
            onclick={() => (clipMode = "ABSOLUTE")}>Timestamps</button
          >
        </div>
      </div>

      <div class="space-y-2">
        <label for="stream-select" class="block text-sm font-medium text-muted-foreground mb-2"
          >Stream</label
        >
        <Select bind:value={selectedStreamId} type="single">
          <SelectTrigger id="stream-select" class="w-full">
            <span class={selectedStreamId ? "" : "text-muted-foreground"}
              >{selectedStreamLabel}</span
            >
          </SelectTrigger>
          <SelectContent>
            {#if streams.length === 0}
              <SelectItem value="" disabled>No streams available</SelectItem>
            {:else}
              {#each streams as stream (stream.id ?? stream.name)}
                <SelectItem value={stream.id}>{stream.name}</SelectItem>
              {/each}
            {/if}
          </SelectContent>
        </Select>
      </div>

      <div class="space-y-2">
        <label for="clip-title" class="block text-sm font-medium text-muted-foreground mb-2"
          >Title</label
        >
        <Input
          id="clip-title"
          type="text"
          bind:value={clipTitle}
          placeholder="Enter clip title"
          required
        />
      </div>

      <div class="space-y-2">
        <label for="clip-description" class="block text-sm font-medium text-muted-foreground mb-2"
          >Description (optional)</label
        >
        <Textarea
          id="clip-description"
          bind:value={clipDescription}
          placeholder="Enter clip description"
          rows={2}
        />
      </div>

      {#if clipMode === "CLIP_NOW"}
        <div class="space-y-2">
          <span class="block text-sm font-medium text-muted-foreground mb-2">Duration</span>
          <div class="flex gap-2">
            {#each durationPresets as preset (preset.value)}
              <button
                type="button"
                class="flex-1 px-3 py-2 text-sm font-medium rounded border transition-colors {clipDuration ===
                preset.value
                  ? 'bg-primary text-primary-foreground border-primary'
                  : 'bg-muted/30 text-muted-foreground border-border hover:bg-muted/50'}"
                onclick={() => (clipDuration = preset.value)}>{preset.label}</button
              >
            {/each}
          </div>
          <p class="text-xs text-muted-foreground/70">
            Captures the last {formatDuration(clipDuration)} from the live stream
          </p>
        </div>
      {:else if clipMode === "DURATION"}
        <div class="grid grid-cols-2 gap-4">
          <div class="space-y-2">
            <label for="start-time" class="block text-sm font-medium text-muted-foreground"
              >Start Time (unix)</label
            >
            <Input id="start-time" type="number" bind:value={clipStartTime} min="0" required />
          </div>
          <div class="space-y-2">
            <label for="duration-input" class="block text-sm font-medium text-muted-foreground"
              >Duration (seconds)</label
            >
            <Input id="duration-input" type="number" bind:value={clipDuration} min="1" required />
          </div>
        </div>
      {:else}
        <div class="grid grid-cols-2 gap-4">
          <div class="space-y-2">
            <label for="start-time" class="block text-sm font-medium text-muted-foreground"
              >Start Time (unix)</label
            >
            <Input id="start-time" type="number" bind:value={clipStartTime} min="0" required />
          </div>
          <div class="space-y-2">
            <label for="end-time" class="block text-sm font-medium text-muted-foreground"
              >End Time (unix)</label
            >
            <Input id="end-time" type="number" bind:value={clipEndTime} min="1" required />
          </div>
        </div>
      {/if}
    </form>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)]"
        onclick={() => (showCreateClipModal = false)}
        disabled={creatingClip}>Cancel</Button
      >
      <Button
        type="submit"
        variant="ghost"
        class="rounded-none h-12 flex-1 text-primary"
        disabled={creatingClip || !selectedStreamId}
        form="create-clip-form">{creatingClip ? "Creating..." : "Create Clip"}</Button
      >
    </DialogFooter>
  </DialogContent>
</Dialog>

<!-- Upload VOD Modal -->
<Dialog
  open={showUploadModal}
  onOpenChange={(v) => {
    if (!uploading) {
      showUploadModal = v;
      if (!v) resetUploadForm();
    }
  }}
>
  <DialogContent
    class="max-w-md rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden"
  >
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground"
        >Upload Video</DialogTitle
      >
      <DialogDescription class="text-xs text-muted-foreground/70"
        >Upload a video file to your library.</DialogDescription
      >
    </DialogHeader>

    <form
      id="upload-form"
      class="slab-body--padded space-y-4"
      onsubmit={(e) => {
        e.preventDefault();
        startUpload();
      }}
    >
      <div class="space-y-2">
        <label for="file-input" class="block text-sm font-medium text-muted-foreground mb-2"
          >Video File</label
        >
        <div
          use:dropZoneAction
          class="relative border-2 border-dashed rounded-lg p-6 text-center transition-colors {dragActive
            ? 'border-primary bg-primary/5'
            : 'border-border hover:border-primary/50'}"
        >
          {#if uploadFile}
            <div class="flex items-center justify-center gap-3">
              <FileVideoIcon class="w-8 h-8 text-primary" />
              <div class="text-left">
                <p class="text-sm font-medium text-foreground">{uploadFile.name}</p>
                <p class="text-xs text-muted-foreground">{formatBytes(uploadFile.size)}</p>
              </div>
            </div>
          {:else}
            <CloudUploadIcon class="w-12 h-12 text-muted-foreground mx-auto mb-2" />
            <p class="text-sm text-muted-foreground mb-2">Click to select or drag and drop</p>
            <p class="text-xs text-muted-foreground/70">MP4, WebM, MOV up to 2GB</p>
          {/if}
          <input
            id="file-input"
            type="file"
            accept="video/*"
            class="absolute inset-0 w-full h-full opacity-0 cursor-pointer"
            onchange={handleFileSelect}
            disabled={uploading}
          />
        </div>
        {#if recoveryOffer && !uploading}
          <div
            class="rounded-md border border-warning/30 bg-warning/5 px-3 py-2 text-xs text-foreground flex items-center justify-between gap-2"
          >
            <span>
              {#if resumeRequested}
                Will resume previous upload ({recoveryOffer.completedParts}/{recoveryOffer.totalParts}
                parts already uploaded). Server will reconcile remaining parts.
              {:else}
                Resume previous upload? {recoveryOffer.completedParts}/{recoveryOffer.totalParts} parts
                already uploaded.
              {/if}
            </span>
            <div class="flex gap-2">
              {#if !resumeRequested}
                <button type="button" class="underline text-primary" onclick={acceptRecovery}
                  >Resume</button
                >
              {/if}
              <button
                type="button"
                class="underline text-muted-foreground"
                onclick={dismissRecovery}>Discard</button
              >
            </div>
          </div>
        {/if}
      </div>

      <div class="space-y-2">
        <label for="upload-title" class="block text-sm font-medium text-muted-foreground mb-2"
          >Title</label
        >
        <Input
          id="upload-title"
          type="text"
          bind:value={uploadTitle}
          placeholder="Enter video title"
          disabled={uploading}
        />
      </div>

      <div class="space-y-2">
        <label for="upload-description" class="block text-sm font-medium text-muted-foreground mb-2"
          >Description (optional)</label
        >
        <Textarea
          id="upload-description"
          bind:value={uploadDescription}
          placeholder="Enter video description"
          rows={2}
          disabled={uploading}
        />
      </div>

      {#if uploading || uploadStage === "processing" || uploadStage === "done"}
        <div class="space-y-3">
          <!-- Transfer phase -->
          <div class="space-y-1">
            <div class="flex justify-between text-xs">
              <span class="font-medium uppercase tracking-wide text-muted-foreground">Transfer</span
              >
              <span class="text-muted-foreground"
                >{#if uploadStage === "initializing"}Initializing…{:else if uploadStage === "uploading"}{uploadProgress}%{:else if uploadStage === "paused"}Paused
                  at {uploadProgress}%{:else}{uploadProgress}%{/if}</span
              >
            </div>
            <Progress value={uploadProgress} max={100} class="h-2" />
            {#if uploadStage === "uploading" || uploadStage === "paused"}
              <div class="flex justify-end gap-2 pt-1">
                {#if uploadStage === "uploading"}
                  <button
                    type="button"
                    class="text-xs underline text-muted-foreground"
                    onclick={pauseUpload}>Pause</button
                  >
                {:else}
                  <button
                    type="button"
                    class="text-xs underline text-primary"
                    onclick={resumeUpload}>Resume</button
                  >
                {/if}
              </div>
            {/if}
          </div>

          <!-- Processing phase (post-transfer) -->
          {#if uploadStage === "completing" || uploadStage === "processing" || uploadStage === "done"}
            <div class="space-y-1 border-t border-border pt-3">
              <div class="flex justify-between text-xs">
                <span class="font-medium uppercase tracking-wide text-muted-foreground"
                  >Processing</span
                >
                <span class="text-muted-foreground">
                  {#if uploadStage === "completing"}Finalizing…{:else if uploadStage === "processing"}Analyzing
                    video…{:else}Ready{/if}
                </span>
              </div>
              <p class="text-xs text-muted-foreground/70">
                Server is extracting metadata and preparing playback. You can safely close this
                dialog; progress is shown in the library.
              </p>
            </div>
          {/if}
        </div>
      {/if}
    </form>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)]"
        onclick={cancelUpload}
        disabled={uploading && uploadStage === "completing"}
        >{#if uploading}Cancel{:else if uploadStage === "processing" || uploadStage === "done"}Close{:else}Close{/if}</Button
      >
      <Button
        type="submit"
        variant="ghost"
        class="rounded-none h-12 flex-1 text-primary"
        disabled={uploading ||
          !uploadFile ||
          uploadStage === "processing" ||
          uploadStage === "done"}
        form="upload-form"
        >{#if uploadStage === "uploading"}Uploading…{:else if uploadStage === "paused"}Paused{:else if uploadStage === "completing"}Finalizing…{:else if uploadStage === "processing"}Processing…{:else if uploadStage === "done"}Done{:else}Upload{/if}</Button
      >
    </DialogFooter>
  </DialogContent>
</Dialog>
