<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { SvelteSet } from "svelte/reactivity";
  import { get } from "svelte/store";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { page } from "$app/stores";
  import { auth } from "$lib/stores/auth";
  import {
    fragment,
    GetStreamsConnectionStore,
    GetClipsConnectionStore,
    GetDVRRequestsStore,
    GetVodAssetsConnectionStore,
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
    ClipLifecycleStore,
    DvrLifecycleStore,
    VodLifecycleStore,
    ClipCreationMode,
    StreamCoreFieldsStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import DeleteClipModal from "$lib/components/clips/DeleteClipModal.svelte";
  import DeleteRecordingModal from "$lib/components/recordings/DeleteRecordingModal.svelte";
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
  import { getContentDeliveryUrls, getShareUrl } from "$lib/config";
  import { formatBytes, formatExpiry, formatTimestamp, isExpired } from "$lib/utils/formatters.js";
  import { resolveTimeRange, TIME_RANGE_OPTIONS } from "$lib/utils/time-range";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  // Type definitions
  type ArtifactType = "all" | "clips" | "dvr" | "vod";

  interface UnifiedArtifact {
    id: string;
    type: ArtifactType;
    title: string;
    hash: string;
    playbackId: string | null;
    streamId: string | null;
    displayStreamId: string | null;
    status: string;
    duration: number | null;
    sizeBytes: number | null;
    createdAt: string | null;
    expiresAt: string | null;
    isFrozen?: boolean;
    storageLocation?: string;
    rawData: ClipData | DvrData | VodData;
  }

  // Houdini stores
  const streamsStore = new GetStreamsConnectionStore();
  const clipsStore = new GetClipsConnectionStore();
  const dvrStore = new GetDVRRequestsStore();
  const vodStore = new GetVodAssetsConnectionStore();
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

  // Subscriptions
  const clipLifecycleSub = new ClipLifecycleStore();
  const dvrLifecycleSub = new DvrLifecycleStore();
  const vodLifecycleSub = new VodLifecycleStore();

  // Fragment stores
  const streamCoreStore = new StreamCoreFieldsStore();

  // Types from stores
  type ClipData = NonNullable<
    NonNullable<NonNullable<typeof $clipsStore.data>["clipsConnection"]>["edges"]
  >[0]["node"];
  type DvrData = NonNullable<
    NonNullable<NonNullable<typeof $dvrStore.data>["dvrRecordingsConnection"]>["edges"]
  >[0]["node"];
  type VodData = NonNullable<
    NonNullable<NonNullable<typeof $vodStore.data>["vodAssetsConnection"]>["edges"]
  >[0]["node"];

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
  let loading = $derived(
    $streamsStore.fetching || $clipsStore.fetching || $dvrStore.fetching || $vodStore.fetching
  );

  // Type filter from URL or state
  let typeFilter = $state<ArtifactType>("all");

  // Initialize from URL params
  $effect(() => {
    const urlType = $page.url.searchParams.get("type") as ArtifactType;
    if (urlType && ["clips", "dvr", "vod"].includes(urlType)) {
      typeFilter = urlType;
    }
  });

  // Raw data from stores
  let maskedStreams = $derived(
    $streamsStore.data?.streamsConnection?.edges?.map((e) => e.node) ?? []
  );
  let streams = $derived(maskedStreams.map((node) => get(fragment(node, streamCoreStore))));
  let clips = $derived($clipsStore.data?.clipsConnection?.edges?.map((e) => e.node) ?? []);
  let dvrRecordings = $derived(
    $dvrStore.data?.dvrRecordingsConnection?.edges?.map((e) => e.node) ?? []
  );
  let vodAssets = $derived($vodStore.data?.vodAssetsConnection?.edges?.map((e) => e.node) ?? []);

  // Artifact states for in-progress operations
  let artifactStates = $derived(
    $artifactStatesStore.data?.analytics?.lifecycle?.artifactStatesConnection?.edges?.map((e) => ({
      cursor: e.cursor,
      ...e.node,
    })) ?? []
  );
  let inProgressArtifacts = $derived(
    artifactStates.filter((s) => s.stage !== "completed" && s.stage !== "error")
  );

  // Storage events (freeze/defrost)
  let storageEvents = $derived(
    $storageEventsStore.data?.analytics?.lifecycle?.storageEventsConnection?.edges?.map(
      (e) => e.node
    ) ?? []
  );

  // Pagination state
  let clipsPageInfo = $derived($clipsStore.data?.clipsConnection?.pageInfo);
  let dvrPageInfo = $derived($dvrStore.data?.dvrRecordingsConnection?.pageInfo);
  let vodPageInfo = $derived($vodStore.data?.vodAssetsConnection?.pageInfo);
  let loadingMore = $state(false);

  let hasMoreItems = $derived.by(() => {
    if (typeFilter === "clips") return clipsPageInfo?.hasNextPage ?? false;
    if (typeFilter === "dvr") return dvrPageInfo?.hasNextPage ?? false;
    if (typeFilter === "vod") return vodPageInfo?.hasNextPage ?? false;
    // For 'all', show load more if any type has more
    return (
      (clipsPageInfo?.hasNextPage || dvrPageInfo?.hasNextPage || vodPageInfo?.hasNextPage) ?? false
    );
  });

  // Unified artifacts
  let allArtifacts = $derived.by(() => {
    const unified: UnifiedArtifact[] = [];

    const toDisplayStreamId = (
      stream: { streamId?: string | null } | null | undefined,
      fallback: string | null
    ): string | null => {
      return stream?.streamId ?? fallback ?? null;
    };

    // Add clips
    for (const clip of clips) {
      unified.push({
        id: clip.id,
        type: "clips",
        title: clip.title || clip.clipHash || "Untitled Clip",
        hash: clip.clipHash || "",
        playbackId: clip.playbackId,
        streamId: clip.streamId,
        displayStreamId: toDisplayStreamId(clip.stream, clip.streamId),
        status: clip.status || "unknown",
        duration: clip.duration,
        sizeBytes: clip.sizeBytes,
        createdAt: clip.createdAt,
        expiresAt: clip.expiresAt,
        rawData: clip,
      });
    }

    // Add DVR recordings
    for (const dvr of dvrRecordings) {
      unified.push({
        id: dvr.dvrHash,
        type: "dvr",
        title: dvr.manifestPath || dvr.dvrHash,
        hash: dvr.dvrHash,
        playbackId: dvr.playbackId,
        streamId: dvr.streamId,
        displayStreamId: toDisplayStreamId(dvr.stream, dvr.streamId),
        status: dvr.status || "unknown",
        duration: dvr.durationSeconds,
        sizeBytes: dvr.sizeBytes,
        createdAt: dvr.createdAt,
        expiresAt: dvr.expiresAt,
        isFrozen: dvr.isFrozen,
        storageLocation: dvr.storageLocation ?? undefined,
        rawData: dvr,
      });
    }

    // Add VOD assets
    for (const vod of vodAssets) {
      unified.push({
        id: vod.id,
        type: "vod",
        title: vod.title || vod.filename || "Untitled Video",
        hash: vod.artifactHash || "",
        playbackId: vod.playbackId,
        streamId: null,
        displayStreamId: null,
        status: vod.status || "unknown",
        duration: vod.durationMs ? Math.floor(vod.durationMs / 1000) : null,
        sizeBytes: vod.sizeBytes,
        createdAt: vod.createdAt,
        expiresAt: vod.expiresAt,
        rawData: vod,
      });
    }

    // Sort by created date, newest first
    return unified.sort((a, b) => {
      const dateA = a.createdAt ? new Date(a.createdAt).getTime() : 0;
      const dateB = b.createdAt ? new Date(b.createdAt).getTime() : 0;
      return dateB - dateA;
    });
  });

  // Search and filters
  let searchQuery = $state("");
  let statusFilter = $state("all");

  let filteredArtifacts = $derived.by(() => {
    let result = allArtifacts;

    // Filter by type
    if (typeFilter !== "all") {
      result = result.filter((a) => a.type === typeFilter);
    }

    // Filter by search
    if (searchQuery.trim()) {
      const query = searchQuery.toLowerCase();
      result = result.filter(
        (a) =>
          a.title.toLowerCase().includes(query) ||
          a.hash.toLowerCase().includes(query) ||
          a.playbackId?.toLowerCase().includes(query) ||
          a.displayStreamId?.toLowerCase().includes(query) ||
          a.streamId?.toLowerCase().includes(query)
      );
    }

    // Filter by status
    if (statusFilter !== "all") {
      result = result.filter((a) => {
        const s = a.status.toLowerCase();
        if (statusFilter === "processing")
          return ["processing", "recording", "uploading", "requested"].includes(s);
        if (statusFilter === "ready") return ["available", "completed", "ready"].includes(s);
        if (statusFilter === "failed") return s === "failed";
        return true;
      });
    }

    return result;
  });

  // Stats
  let totalClips = $derived(clips.length);
  let totalDvr = $derived(dvrRecordings.length);
  let totalVod = $derived(vodAssets.length);
  let totalAll = $derived(allArtifacts.length);

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
      type: ((e.node as { contentType?: string }).contentType as ArtifactType) || "clips",
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

  // Delete modals
  let showDeleteClipModal = $state(false);
  let clipToDelete = $state<ClipData | null>(null);
  let deletingClipId = $state("");

  let showDeleteDvrModal = $state(false);
  let dvrToDelete = $state<DvrData | null>(null);
  let deletingDvrHash = $state("");

  let showDeleteVodModal = $state(false);
  let vodToDelete = $state<VodData | null>(null);
  let deletingVodId = $state("");

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
  let uploadStage = $state<"idle" | "initializing" | "uploading" | "completing" | "done">("idle");
  let currentUploadId = $state<string | null>(null);

  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadData();
  });

  onDestroy(() => {
    clipLifecycleSub.unlisten();
    dvrLifecycleSub.unlisten();
    vodLifecycleSub.unlisten();
  });

  async function loadData() {
    try {
      lifecycleRangeResolved = resolveTimeRange(lifecycleRange);

      await Promise.all([
        streamsStore.fetch(),
        clipsStore.fetch({ variables: { first: 100 } }),
        dvrStore.fetch({ variables: { first: 100 } }),
        vodStore.fetch({ variables: { first: 100 } }),
        artifactEventsStore
          .fetch({
            variables: {
              timeRange: { start: lifecycleRangeResolved.start, end: lifecycleRangeResolved.end },
              first: 50,
            },
          })
          .catch(() => null),
        // Fetch current artifact states for in-progress operations
        artifactStatesStore.fetch({ variables: { first: 50 } }).catch(() => null),
        // Fetch storage events (freeze/defrost)
        storageEventsStore
          .fetch({
            variables: {
              timeRange: { start: lifecycleRangeResolved.start, end: lifecycleRangeResolved.end },
              first: 30,
            },
          })
          .catch(() => null),
      ]);

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

    try {
      const promises: Promise<unknown>[] = [];

      // Load more based on current filter or load all if viewing all
      if (typeFilter === "clips" || typeFilter === "all") {
        if (clipsPageInfo?.hasNextPage && clipsPageInfo.endCursor) {
          promises.push(
            clipsStore.fetch({ variables: { first: 50, after: clipsPageInfo.endCursor } })
          );
        }
      }
      if (typeFilter === "dvr" || typeFilter === "all") {
        if (dvrPageInfo?.hasNextPage && dvrPageInfo.endCursor) {
          promises.push(dvrStore.fetch({ variables: { first: 50, after: dvrPageInfo.endCursor } }));
        }
      }
      if (typeFilter === "vod" || typeFilter === "all") {
        if (vodPageInfo?.hasNextPage && vodPageInfo.endCursor) {
          promises.push(vodStore.fetch({ variables: { first: 50, after: vodPageInfo.endCursor } }));
        }
      }

      await Promise.all(promises);
    } catch (error) {
      console.error("Failed to load more items:", error);
      toast.error("Failed to load more items.");
    } finally {
      loadingMore = false;
    }
  }

  // Create clip
  async function createClip() {
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

  // Delete handlers
  function confirmDeleteClip(clip: ClipData) {
    clipToDelete = clip;
    showDeleteClipModal = true;
  }

  async function deleteClip() {
    if (!clipToDelete) return;
    try {
      deletingClipId = clipToDelete.id;
      const result = await deleteClipMutation.mutate({ id: clipToDelete.id });
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

  function confirmDeleteDvr(dvr: DvrData) {
    dvrToDelete = dvr;
    showDeleteDvrModal = true;
  }

  async function deleteDvr() {
    if (!dvrToDelete) return;
    try {
      deletingDvrHash = dvrToDelete.dvrHash;
      const result = await deleteDvrMutation.mutate({ dvrHash: dvrToDelete.dvrHash });
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

  function confirmDeleteVod(vod: VodData) {
    vodToDelete = vod;
    showDeleteVodModal = true;
  }

  async function deleteVod() {
    if (!vodToDelete) return;
    try {
      deletingVodId = vodToDelete.id;
      const result = await deleteVodMutation.mutate({ id: vodToDelete.id });
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
  function handleFileSelect(event: Event) {
    const target = event.target as HTMLInputElement;
    const file = target.files?.[0];
    if (file) {
      uploadFile = file;
      if (!uploadTitle) {
        uploadTitle = file.name.replace(/\.[^/.]+$/, "");
      }
    }
  }

  async function startUpload() {
    if (!uploadFile) {
      toast.warning("Please select a file to upload");
      return;
    }

    try {
      uploading = true;
      uploadStage = "initializing";
      uploadProgress = 0;

      const initResult = await createVodUploadMutation.mutate({
        input: {
          filename: uploadFile.name,
          sizeBytes: uploadFile.size,
          contentType: uploadFile.type || "video/mp4",
          title: uploadTitle.trim() || undefined,
          description: uploadDescription.trim() || undefined,
        },
      });

      const createResult = initResult.data?.createVodUpload;
      if (createResult?.__typename !== "VodUploadSession") {
        const error = createResult as unknown as { message?: string };
        throw new Error(error.message || "Failed to initialize upload");
      }

      currentUploadId = createResult.id;
      const parts = createResult.parts;
      const partSize = createResult.partSize;

      uploadStage = "uploading";

      const completedParts: { partNumber: number; etag: string }[] = [];
      const totalParts = parts.length;

      for (let i = 0; i < parts.length; i++) {
        const part = parts[i];
        const start = i * partSize;
        const end = Math.min(start + partSize, uploadFile.size);
        const chunk = uploadFile.slice(start, end);

        const response = await fetch(part.presignedUrl, {
          method: "PUT",
          body: chunk,
          headers: { "Content-Type": uploadFile.type || "application/octet-stream" },
        });

        if (!response.ok) {
          throw new Error(`Failed to upload part ${part.partNumber}`);
        }

        const etag = response.headers.get("ETag")?.replace(/"/g, "") || "";
        completedParts.push({ partNumber: part.partNumber, etag });
        uploadProgress = Math.round(((i + 1) / totalParts) * 100);
      }

      uploadStage = "completing";

      const completeResult = await completeVodUploadMutation.mutate({
        input: { uploadId: currentUploadId, parts: completedParts },
      });

      if (completeResult.data?.completeVodUpload?.__typename !== "VodAsset") {
        const error = completeResult.data?.completeVodUpload as unknown as { message?: string };
        throw new Error(error?.message || "Failed to complete upload");
      }

      uploadStage = "done";
      toast.success("Video uploaded successfully!");
      await loadData();
      resetUploadForm();
      showUploadModal = false;
    } catch (error) {
      console.error("Upload failed:", error);
      toast.error(`Upload failed: ${error instanceof Error ? error.message : "Unknown error"}`);
      if (currentUploadId) {
        try {
          await abortVodUploadMutation.mutate({ uploadId: currentUploadId });
        } catch {}
      }
    } finally {
      uploading = false;
      currentUploadId = null;
      uploadStage = "idle";
    }
  }

  function resetUploadForm() {
    uploadFile = null;
    uploadTitle = "";
    uploadDescription = "";
    uploadProgress = 0;
    uploadStage = "idle";
    currentUploadId = null;
  }

  function cancelUpload() {
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

  function getStatusColor(status: string): string {
    const s = status.toLowerCase();
    if (["available", "completed", "ready"].includes(s))
      return "text-success bg-success/10 border-success/20";
    if (["processing", "recording", "uploading", "requested"].includes(s))
      return "text-warning bg-warning/10 border-warning/20";
    if (s === "failed") return "text-destructive bg-destructive/10 border-destructive/20";
    if (s === "deleted") return "text-muted-foreground bg-muted border-border opacity-70";
    return "text-muted-foreground bg-muted border-border";
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

  function canPlayArtifact(artifact: UnifiedArtifact): boolean {
    if (!artifact.playbackId) return false;
    if (isExpired(artifact.expiresAt)) return false;
    if (artifact.status.toLowerCase() === "failed") return false;
    if (artifact.status.toLowerCase() === "deleted") return false;
    if (
      ["processing", "recording", "uploading", "requested"].includes(artifact.status.toLowerCase())
    )
      return false;
    return true;
  }

  function playArtifact(artifact: UnifiedArtifact) {
    if (artifact.playbackId) {
      const url = getShareUrl(artifact.playbackId);
      if (url) goto(resolve(url));
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
    goto(resolve(url.toString()), { replaceState: true, noScroll: true });
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
  const ActivityIcon = getIconComponent("Activity");
  const CloudUploadIcon = getIconComponent("CloudUpload");
  const FileVideoIcon = getIconComponent("FileVideo");
  const CloudIcon = getIconComponent("Cloud");
  const LoaderIcon = getIconComponent("Loader");
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
              value={totalAll}
              valueColor="text-primary"
              label="Total Items"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={ScissorsIcon}
              iconColor="text-primary"
              value={totalClips}
              valueColor="text-primary"
              label="Clips"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={FilmIcon}
              iconColor="text-info"
              value={totalDvr}
              valueColor="text-info"
              label="Recordings"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={VideoIcon}
              iconColor="text-success"
              value={totalVod}
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
                  <label class="block text-sm font-medium text-muted-foreground mb-2">Type</label>
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
              {#if filteredArtifacts.length === 0}
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
                              {:else if canPlayArtifact(artifact) && artifact.playbackId}
                                {@const urls = getContentDeliveryUrls(
                                  artifact.playbackId,
                                  artifact.type === "clips"
                                    ? "clip"
                                    : artifact.type === "dvr"
                                      ? "dvr"
                                      : "vod"
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
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  class="h-7 w-7 p-0 text-muted-foreground hover:text-foreground"
                                  title={expandedArtifact === artifact.id ? "Hide" : "Share"}
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
                              {:else}
                                <span class="text-[10px] text-warning animate-pulse px-2"
                                  >Processing...</span
                                >
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
                            <div class="flex flex-col">
                              <div
                                class="text-sm font-medium text-foreground truncate max-w-xs group-hover:text-primary transition-colors"
                                title={artifact.title}
                              >
                                {artifact.title}
                              </div>
                              <div class="text-[10px] text-muted-foreground font-mono">
                                {artifact.hash?.slice(0, 8) || "N/A"}...
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
                                  artifact.status
                                )}"
                              >
                                {artifact.status || "Unknown"}
                              </span>
                              {#if artifact.storageLocation === "freezing"}
                                <span
                                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border text-cyan-400 bg-cyan-400/10 border-cyan-400/20"
                                >
                                  <LoaderIcon class="w-3 h-3 animate-spin" />
                                  Freezing...
                                </span>
                              {:else if artifact.storageLocation === "defrosting"}
                                <span
                                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border text-orange-400 bg-orange-400/10 border-orange-400/20"
                                >
                                  <LoaderIcon class="w-3 h-3 animate-spin" />
                                  Defrosting...
                                </span>
                              {:else if artifact.isFrozen}
                                <span
                                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border text-blue-400 bg-blue-400/10 border-blue-400/20"
                                >
                                  <SnowflakeIcon class="w-3 h-3" />
                                  Frozen
                                </span>
                              {:else if artifact.storageLocation === "cloud" && artifact.type === "dvr"}
                                <span
                                  class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-medium border text-emerald-400 bg-emerald-400/10 border-emerald-400/20"
                                >
                                  <CloudIcon class="w-3 h-3" />
                                  Cloud
                                </span>
                              {/if}
                            </div>
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
                            {formatExpiry(artifact.expiresAt)}
                          </TableCell>
                        </TableRow>

                        <!-- Expanded Share Row -->
                        {#if expandedArtifact === artifact.id && canPlayArtifact(artifact) && artifact.playbackId}
                          <TableRow class="bg-muted/5 border-t-0">
                            <TableCell
                              colspan={9}
                              class="px-4 py-4 cursor-default"
                              onclick={(e) => e.stopPropagation()}
                            >
                              <div class="pl-4 border-l-2 border-primary/20">
                                <PlaybackProtocols
                                  contentId={artifact.playbackId}
                                  contentType={artifact.type === "clips"
                                    ? "clip"
                                    : artifact.type === "dvr"
                                      ? "dvr"
                                      : "vod"}
                                  showPrimary={true}
                                  showAdditional={true}
                                />
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

          <!-- Storage Activity (Freeze/Defrost Events) -->
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
                            'freeze'
                              ? 'bg-info/20 text-info border border-info/30'
                              : 'bg-warning/20 text-warning border border-warning/30'}"
                          >
                            {event.action === "freeze" ? "❄️" : "🔥"}
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

<!-- Delete Clip Modal -->
<DeleteClipModal
  open={showDeleteClipModal && !!clipToDelete}
  clip={clipToDelete}
  deleting={!!deletingClipId}
  onConfirm={deleteClip}
  onCancel={() => {
    showDeleteClipModal = false;
    clipToDelete = null;
  }}
/>

<!-- Delete DVR Modal -->
<DeleteRecordingModal
  open={showDeleteDvrModal && !!dvrToDelete}
  recording={dvrToDelete}
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
          >{vodToDelete?.title || vodToDelete?.filename || "this video"}</strong
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
          class="relative border-2 border-dashed border-border rounded-lg p-6 text-center hover:border-primary/50 transition-colors"
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

      {#if uploading}
        <div class="space-y-2">
          <div class="flex justify-between text-xs text-muted-foreground">
            <span>
              {#if uploadStage === "initializing"}Initializing...{:else if uploadStage === "uploading"}Uploading...
                {uploadProgress}%{:else if uploadStage === "completing"}Completing...{:else if uploadStage === "done"}Complete!{/if}
            </span>
            <span>{uploadProgress}%</span>
          </div>
          <Progress value={uploadProgress} max={100} class="h-2" />
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
        >{uploading ? "Cancel" : "Close"}</Button
      >
      <Button
        type="submit"
        variant="ghost"
        class="rounded-none h-12 flex-1 text-primary"
        disabled={uploading || !uploadFile}
        form="upload-form">{uploading ? "Uploading..." : "Upload"}</Button
      >
    </DialogFooter>
  </DialogContent>
</Dialog>
