<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { get } from "svelte/store";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    fragment,
    GetStreamsConnectionStore,
    GetClipsConnectionStore,
    CreateClipStore,
    DeleteClipStore,
    ClipLifecycleStore,
    ClipCreationMode,
    StreamCoreFieldsStore
  } from "$houdini";
  import type { ClipLifecycle$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import DeleteClipModal from "$lib/components/clips/DeleteClipModal.svelte";
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
  import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
  } from "$lib/components/ui/select";
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
  import { formatBytes, formatExpiry } from "$lib/utils/formatters.js";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  // Houdini stores
  const streamsStore = new GetStreamsConnectionStore();
  const clipsConnectionStore = new GetClipsConnectionStore();
  const createClipMutation = new CreateClipStore();
  const deleteClipMutation = new DeleteClipStore();
  const clipLifecycleSub = new ClipLifecycleStore();

  // Fragment stores for unmasking
  const streamCoreStore = new StreamCoreFieldsStore();

  // Types from Houdini
  type StreamData = NonNullable<NonNullable<NonNullable<typeof $streamsStore.data>["streamsConnection"]>["edges"]>[0]["node"];
  type ClipData = NonNullable<NonNullable<NonNullable<typeof $clipsConnectionStore.data>["clipsConnection"]>["edges"]>[0]["node"];
  type ClipLifecycleEvent = NonNullable<ClipLifecycle$result["clipLifecycle"]>;

  // Clip creation modes
  type ClipModeType = 'CLIP_NOW' | 'DURATION' | 'ABSOLUTE';

  // Duration presets for Clip Now mode
  const durationPresets = [
    { label: '30s', value: 30 },
    { label: '1 min', value: 60 },
    { label: '2 min', value: 120 },
    { label: '5 min', value: 300 },
  ];

  let isAuthenticated = false;

  // Derived state from Houdini stores
  let loading = $derived($streamsStore.fetching || $clipsConnectionStore.fetching);
  let maskedStreams = $derived($streamsStore.data?.streamsConnection?.edges?.map(e => e.node) ?? []);
  // Unmask stream core fields to access id/name properties
  let streams = $derived(
    maskedStreams.map(node => get(fragment(node, streamCoreStore)))
  );
  let clipsEdges = $derived($clipsConnectionStore.data?.clipsConnection?.edges ?? []);
  let clips = $derived(clipsEdges.map(e => e.node));
  let hasMoreClips = $derived($clipsConnectionStore.data?.clipsConnection?.pageInfo?.hasNextPage ?? false);
  let totalClipsCount = $derived($clipsConnectionStore.data?.clipsConnection?.totalCount ?? 0);

  let streamsError = $state(false);

  // Pagination state
  let loadingMoreClips = $state(false);

  // Clip creation
  let showCreateModal = $state(false);
  let creatingClip = $state(false);
  let selectedStreamId = $state("");
  let clipMode = $state<ClipModeType>('CLIP_NOW');

  let selectedStreamLabel = $derived(
    !selectedStreamId
      ? "Select a stream"
      : streams.find((stream) => stream.id === selectedStreamId)?.name || "Select a stream"
  );

  let clipTitle = $state("");
  let clipDescription = $state("");
  let duration = $state(60);  // Default 60 seconds for Clip Now
  let startTime = $state(0);
  let endTime = $state(300); // 5 minutes default for Absolute mode

  // Clip deletion
  let deletingClipId = $state("");
  let showDeleteModal = $state(false);
  let clipToDelete = $state<ClipData | null>(null);

  // Track active stream for clip lifecycle subscription
  let activeClipStream = $state<string | null>(null);

  // Track clip progress for real-time updates
  let clipProgress = $state<Record<string, { stage: string; percent: number; message?: string }>>({});

  // Derived stats
  let processingClips = $derived(clips.filter(c => c.status === "Processing" || c.status === "processing").length);
  let completedClips = $derived(clips.filter(c => c.status === "Available" || c.status === "completed").length);
  let failedClips = $derived(clips.filter(c => c.status === "Failed" || c.status === "failed").length);

  // Expanded row tracking
  let expandedClip = $state<string | null>(null);

  // Filter for search query
  let searchQuery = $state("");
  let statusFilter = $state("all");

  let filteredClips = $derived.by(() => {
    let result = clips;

    // Filter by search query
    if (searchQuery.trim()) {
      const query = searchQuery.toLowerCase();
      result = result.filter(
        (clip) =>
          clip.title?.toLowerCase().includes(query) ||
          clip.clipHash?.toLowerCase().includes(query) ||
          clip.streamName?.toLowerCase().includes(query) ||
          clip.description?.toLowerCase().includes(query),
      );
    }

    // Filter by status
    if (statusFilter !== "all") {
      result = result.filter((clip) => {
        const s = clip.status?.toLowerCase() || "";
        if (statusFilter === "processing") return s === "processing" || s === "requested";
        if (statusFilter === "completed") return s === "available" || s === "completed" || s === "ready";
        if (statusFilter === "failed") return s === "failed";
        return true;
      });
    }

    return result;
  });

  // Subscribe to auth store
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
  });

  // Effect to handle clip lifecycle subscription updates
  $effect(() => {
    const event = $clipLifecycleSub.data?.clipLifecycle;
    if (event) {
      handleClipEvent(event);
    }
  });

  // Start subscription when stream is selected for clip creation
  function startClipSubscription(streamId: string) {
    if (activeClipStream !== streamId) {
      clipLifecycleSub.unlisten();
      activeClipStream = streamId;
      // Use stream.id (internal UUID) for subscriptions - this is the canonical identifier
      clipLifecycleSub.listen({ stream: streamId });
    }
  }

  // Clip stages map (matching protobuf enum)
  const ClipStage = {
    REQUESTED: 1,
    QUEUED: 2,
    PROGRESS: 3,
    DONE: 4,
    FAILED: 5,
    DELETED: 6,
  };

  function handleClipEvent(event: ClipLifecycleEvent) {
    // Update clip progress tracking
    if (event.requestId) {
      clipProgress[event.requestId] = {
        stage: event.stage.toString(),
        percent: event.progressPercent ?? 0,
        message: event.error ?? undefined,
      };
      clipProgress = { ...clipProgress };
    }

    // Handle different stages
    if (event.stage === ClipStage.DONE) {
      toast.success(`Clip "${event.requestId}" is ready!`);
      // Refresh clips list to show the new clip
      loadData();
    } else if (event.stage === ClipStage.FAILED) {
      toast.error(`Clip creation failed: ${event.error || "Unknown error"}`);
    } else if (event.stage === ClipStage.PROGRESS && event.progressPercent) {
      // Progress update - could show in UI
      console.log(`Clip ${event.requestId} progress: ${event.progressPercent}%`);
    }
  }

  async function loadData() {
    try {
      streamsError = false;

      // Load streams and clips in parallel
      await Promise.all([
        streamsStore.fetch(),
        clipsConnectionStore.fetch({ variables: { first: 50 } }),
      ]);

      if ($streamsStore.errors?.length || $clipsConnectionStore.errors?.length) {
        console.error("Failed to load data:", $streamsStore.errors, $clipsConnectionStore.errors);
        streamsError = true;
        toast.error("Failed to load clips data. Please refresh the page.");
      }
    } catch (error) {
      console.error("Failed to load data:", error);
      streamsError = true;
      toast.error("Failed to load clips data. Please refresh the page.");
    }
  }

  async function loadMoreClips() {
    if (!hasMoreClips || loadingMoreClips) return;

    try {
      loadingMoreClips = true;
      const endCursor = $clipsConnectionStore.data?.clipsConnection?.pageInfo?.endCursor;
      await clipsConnectionStore.fetch({
        variables: {
          first: 50,
          after: endCursor ?? undefined,
        },
      });
    } catch (error) {
      console.error("Failed to load more clips:", error);
      toast.error("Failed to load more clips.");
    } finally {
      loadingMoreClips = false;
    }
  }

  async function createClip() {
    if (!clipTitle.trim() || !selectedStreamId) {
      toast.warning("Please fill in all required fields");
      return;
    }

    // Validate based on mode
    if (clipMode === 'ABSOLUTE' && endTime <= startTime) {
      toast.warning("End time must be after start time");
      return;
    }

    if ((clipMode === 'CLIP_NOW' || clipMode === 'DURATION') && duration <= 0) {
      toast.warning("Duration must be greater than 0");
      return;
    }

    try {
      creatingClip = true;

      // Find stream for subscription - use stream.id (internal UUID) as the canonical identifier
      const selectedStream = streams.find(s => s.id === selectedStreamId);
      if (selectedStream) {
        startClipSubscription(selectedStream.id);
      }

      // Build input based on mode
      const input: Parameters<typeof createClipMutation.mutate>[0]['input'] = {
        stream: selectedStreamId,
        title: clipTitle.trim(),
        description: clipDescription.trim() || undefined,
      };

      switch (clipMode) {
        case 'CLIP_NOW':
          // Clip Now: Just duration, backend calculates relative to live
          input.mode = ClipCreationMode.CLIP_NOW;
          input.duration = Math.floor(duration);
          break;

        case 'DURATION':
          // Duration mode: Start time + duration
          input.mode = ClipCreationMode.DURATION;
          input.startUnix = Math.floor(startTime);
          input.duration = Math.floor(duration);
          break;

        case 'ABSOLUTE':
          // Absolute mode: Start and end unix timestamps
          input.mode = ClipCreationMode.ABSOLUTE;
          input.startUnix = Math.floor(startTime);
          input.stopUnix = Math.floor(endTime);
          break;
      }

      const result = await createClipMutation.mutate({ input });

      // Check for errors in the union type response using __typename
      const createResult = result.data?.createClip;
      // Houdini types unions with error types only in the discriminator, so check for non-error
      const isError = createResult?.__typename === "ValidationError" ||
                      createResult?.__typename === "NotFoundError" ||
                      createResult?.__typename === "AuthError";
      if (createResult && !isError) {
        // Success - Houdini's @list directive will auto-update the list
        const modeLabel = clipMode === 'CLIP_NOW' ? ' (from live)' : '';
        toast.success(`Clip created successfully${modeLabel}!`);
      } else if (createResult) {
        // Error response - access message from the error types
        const errorResult = createResult as unknown as { message?: string };
        toast.error(errorResult.message || "Failed to create clip");
      }

      // Reset form
      showCreateModal = false;
      resetClipForm();
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
    clipMode = 'CLIP_NOW';
    duration = 60;
    startTime = 0;
    endTime = 300;
  }

  // Delete clip
  async function deleteClip() {
    if (!clipToDelete) return;
    const idToDelete = clipToDelete.id;

    try {
      deletingClipId = idToDelete;
      const result = await deleteClipMutation.mutate({ id: idToDelete });

      const deleteResult = result.data?.deleteClip;
      if (deleteResult?.__typename === "DeleteSuccess") {
        // Refresh list to remove clip
        clipsConnectionStore.fetch({ policy: "NetworkOnly", variables: { first: 50 } });

        // Close modal
        showDeleteModal = false;
        clipToDelete = null;

        toast.success("Clip deleted successfully!");
      } else if (deleteResult) {
        const errorResult = deleteResult as unknown as { message?: string };
        toast.error(errorResult.message || "Failed to delete clip");
      }
    } catch (error) {
      console.error("Failed to delete clip:", error);
      toast.error("Failed to delete clip. Please try again.");
    } finally {
      deletingClipId = "";
    }
  }

  function confirmDeleteClip(clip: ClipData) {
    clipToDelete = clip;
    showDeleteModal = true;
  }

  function formatDurationSeconds(seconds: number) {
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    return `${minutes}:${remainingSeconds.toString().padStart(2, "0")}`;
  }

  function formatDate(dateString: string | Date) {
    return new Date(dateString).toLocaleDateString();
  }

  function playClip(clipHash: string) {
    goto(`/view?type=clip&id=${clipHash}`);
  }

  function getStatusColor(status: string | null | undefined): string {
    switch (status?.toLowerCase()) {
      case "available":
      case "completed":
      case "ready":
        return "text-success bg-success/10 border-success/20";
      case "processing":
      case "requested":
        return "text-warning bg-warning/10 border-warning/20";
      case "failed":
        return "text-destructive bg-destructive/10 border-destructive/20";
      case "deleted":
        return "text-muted-foreground bg-muted border-border opacity-70";
      default:
        return "text-muted-foreground bg-muted border-border";
    }
  }

  function isClipReady(status: string | null | undefined): boolean {
    const s = status?.toLowerCase();
    // Status may be null when lifecycle data isn't populated from Periscope
    // Consider ready if status indicates completion or if status is not set (assume ready)
    return s === "available" || s === "completed" || s === "ready" || s === undefined || s === null || s === "";
  }

  function isClipProcessing(status: string | null | undefined): boolean {
    const s = status?.toLowerCase();
    return s === "processing" || s === "requested" || s === "queued";
  }

  function isClipFailed(status: string | null | undefined): boolean {
    const s = status?.toLowerCase();
    return s === "failed" || s === "error";
  }

  function isClipDeleted(status: string | null | undefined): boolean {
    return status?.toLowerCase() === "deleted";
  }

  // Check if clip can be played - has hash and isn't actively processing or failed
  function canPlayClip(clip: ClipData): boolean {
    if (!clip.clipHash) return false;
    if (isClipFailed(clip.status)) return false;
    if (isClipProcessing(clip.status)) return false;
    if (isClipDeleted(clip.status)) return false;
    return true; // Has hash and not in a bad state
  }

  // Icons
  const ScissorsIcon = getIconComponent("Scissors");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const LoaderIcon = getIconComponent("Loader");
  const XCircleIcon = getIconComponent("XCircle");
  const PlusIcon = getIconComponent("Plus");
  const PlayIcon = getIconComponent("Play");
  const DownloadIcon = getIconComponent("Download");
  const Share2Icon = getIconComponent("Share2");
  const Trash2Icon = getIconComponent("Trash2");
  const FilterIcon = getIconComponent("Filter");
  const SearchIcon = getIconComponent("Search");
  const ChevronUpIcon = getIconComponent("ChevronUp");
  const SnowflakeIcon = getIconComponent("Snowflake");
</script>

<svelte:head>
  <title>Clips - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col overflow-hidden">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 z-10 bg-background">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <ScissorsIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Clips</h1>
          <p class="text-sm text-muted-foreground">
            Create and manage video clips from your streams
          </p>
        </div>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto bg-background/50">
    {#if loading}
      <!-- Loading Skeleton -->
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
            <div class="slab-actions">
              <div class="h-10 bg-muted/50 rounded-none w-full animate-pulse"></div>
            </div>
          </div>
        {/each}
      </GridSeam>
    {:else}
      <div class="page-transition">
        <!-- Stats Bar -->
        <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0 min-h-full content-start">
          <div>
            <DashboardMetricCard
              icon={ScissorsIcon}
              iconColor="text-primary"
              value={totalClipsCount}
              valueColor="text-primary"
              label="Total Clips"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={LoaderIcon}
              iconColor="text-warning"
              value={processingClips}
              valueColor="text-warning"
              label="Processing"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={CheckCircleIcon}
              iconColor="text-success"
              value={completedClips}
              valueColor="text-success"
              label="Completed"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={XCircleIcon}
              iconColor="text-destructive"
              value={failedClips}
              valueColor="text-destructive"
              label="Failed"
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
              <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
                <!-- Search -->
                <div>
                  <label
                    for="search"
                    class="block text-sm font-medium text-muted-foreground mb-2"
                  >
                    Search Clips
                  </label>
                  <div class="relative">
                    <SearchIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                    <Input
                      id="search"
                      type="text"
                      bind:value={searchQuery}
                      placeholder="Search by title, hash, or stream name..."
                      class="w-full pl-10"
                    />
                  </div>
                </div>

                <!-- Status Filter -->
                <div>
                  <label
                    for="status-filter"
                    class="block text-sm font-medium text-muted-foreground mb-2"
                  >
                    Status
                  </label>
                  <Select bind:value={statusFilter} type="single">
                    <SelectTrigger id="status-filter" class="w-full">
                      {#if statusFilter === 'all'}All Statuses{:else if statusFilter === 'processing'}Processing{:else if statusFilter === 'completed'}Completed{:else if statusFilter === 'failed'}Failed{:else}All Statuses{/if}
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="all">All Statuses</SelectItem>
                      <SelectItem value="processing">Processing</SelectItem>
                      <SelectItem value="completed">Completed</SelectItem>
                      <SelectItem value="failed">Failed</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
              </div>
            </div>
          </div>

          <!-- Clips Table Slab -->
          <div class="slab col-span-full">
            <div class="slab-header flex justify-between items-center">
              <div class="flex items-center gap-2">
                <ScissorsIcon class="w-4 h-4 text-info" />
                <h3>Your Clips</h3>
              </div>
              {#if streamsError}
                <Button
                  variant="destructive"
                  size="sm"
                  class="gap-2 h-8"
                  onclick={loadData}
                  title="Failed to load streams. Click to retry."
                >
                  Retry Loading
                </Button>
              {:else if streams.length === 0}
                <Button
                  variant="outline"
                  size="sm"
                  class="gap-2 h-8 cursor-not-allowed opacity-60"
                  disabled
                  title="No streams available. Create a stream first to make clips."
                >
                  <PlusIcon class="w-3.5 h-3.5" />
                  Create Clip
                </Button>
              {:else}
                <Button
                  variant="outline"
                  size="sm"
                  class="gap-2 h-8"
                  onclick={() => (showCreateModal = true)}
                >
                  <PlusIcon class="w-3.5 h-3.5" />
                  Create Clip
                </Button>
              {/if}
            </div>
            <div class="slab-body--flush">
              {#if filteredClips.length === 0}
                <div class="p-8">
                  {#if searchQuery}
                    <EmptyState
                      iconName="Scissors"
                      title="No clips found"
                      description="Try adjusting your search query."
                      actionText="Clear Search"
                      onAction={() => (searchQuery = "")}
                    />
                  {:else if streams.length > 0}
                    <EmptyState
                      iconName="Scissors"
                      title="No clips yet"
                      description="Create your first clip from a stream to get started."
                      actionText="Create Clip"
                      onAction={() => (showCreateModal = true)}
                    />
                  {:else}
                    <EmptyState
                      iconName="Scissors"
                      title="No clips yet"
                      description="You need at least one stream to create clips."
                      actionText="Create Stream"
                      onAction={() => goto(resolve("/streams"))}
                    />
                  {/if}
                </div>
              {:else}
                <div class="overflow-x-auto">
                  <Table class="w-full">
                    <TableHeader>
                      <TableRow>
                        <TableHead
                          class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider w-[140px]"
                        >
                          Actions
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Clip
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Stream
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Status
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Duration
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Size
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Created
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Expires
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody class="divide-y divide-border">
                      {#each filteredClips as clip (clip.id)}
                        {@const isDeleted = isClipDeleted(clip.status)}
                        <TableRow
                          class="transition-colors group {isDeleted ? 'opacity-60 bg-muted/30 cursor-not-allowed' : 'hover:bg-muted/50 cursor-pointer'}"
                          onclick={() => !isDeleted && canPlayClip(clip) && playClip(clip.clipHash || "")}
                        >
                          <!-- Actions Column (Left, Horizontal) -->
                          <TableCell
                            class="px-4 py-2 align-middle"
                            onclick={(e) => e.stopPropagation()}
                          >
                            <div class="flex items-center gap-1">
                              {#if isDeleted}
                                <span class="text-[10px] text-muted-foreground px-2 italic">Deleted</span>
                              {:else if canPlayClip(clip)}
                                {@const urls = getContentDeliveryUrls(clip.clipHash, "clip")}
                                
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
                                  title={expandedClip === clip.id ? "Hide Share Info" : "Share Clip"}
                                  onclick={() => expandedClip = expandedClip === clip.id ? null : clip.id}
                                >
                                  {#if expandedClip === clip.id}
                                    <ChevronUpIcon class="w-3.5 h-3.5" />
                                  {:else}
                                    <Share2Icon class="w-3.5 h-3.5" />
                                  {/if}
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  class="h-7 w-7 p-0 text-muted-foreground hover:text-destructive opacity-0 group-hover:opacity-100 transition-opacity focus:opacity-100"
                                  title="Delete Clip"
                                  onclick={() => confirmDeleteClip(clip)}
                                >
                                  <Trash2Icon class="w-3.5 h-3.5" />
                                </Button>
                              {:else if clip.status === "Processing" || clip.status === "processing"}
                                <span class="text-[10px] text-warning animate-pulse px-2">Processing...</span>
                              {:else}
                                <span class="text-[10px] text-muted-foreground px-2">-</span>
                              {/if}
                            </div>
                          </TableCell>

                          <TableCell class="px-4 py-2">
                            <div class="flex flex-col">
                              <div class="text-sm font-medium text-foreground truncate max-w-xs group-hover:text-primary transition-colors" title={clip.title || clip.clipHash || ""}>
                                {clip.title || clip.clipHash || "Untitled"}
                              </div>
                              {#if clip.description}
                                <div class="text-[10px] text-muted-foreground truncate max-w-xs" title={clip.description}>
                                  {clip.description}
                                </div>
                              {/if}
                              <div class="text-[10px] text-muted-foreground font-mono">
                                {clip.clipHash?.slice(0, 8) || "N/A"}...
                              </div>
                            </div>
                          </TableCell>
                          <TableCell class="px-4 py-2">
                            <div class="text-sm text-foreground">
                              {clip.streamName || "Unknown"}
                            </div>
                          </TableCell>
                          <TableCell class="px-4 py-2">
                            <div class="flex flex-col gap-1.5 min-w-[120px]">
                              <div class="flex items-center gap-2">
                                <span class="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border {getStatusColor(clip.status)}">
                                  {clip.status || "Unknown"}
                                </span>
                              </div>
                              
                              {#if (clip.status === "Processing" || clip.status === "processing" || clip.status === "requested" || clipProgress[clip.id]) && !isClipReady(clip.status) && clip.status !== 'failed'}
                                {@const progress = clipProgress[clip.id]?.percent ?? 0}
                                <div class="w-full bg-muted rounded-full h-1.5 overflow-hidden">
                                  <div 
                                    class="bg-primary h-full transition-all duration-300 ease-out" 
                                    style="width: {progress}%"
                                  ></div>
                                </div>
                                {#if progress > 0}
                                  <span class="text-[10px] text-muted-foreground">{progress}%</span>
                                {/if}
                              {/if}
                            </div>
                          </TableCell>
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {clip.duration ? formatDurationSeconds(clip.duration) : "N/A"}
                          </TableCell>
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {clip.sizeBytes ? formatBytes(clip.sizeBytes) : "N/A"}
                          </TableCell>
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {clip.createdAt ? formatDate(clip.createdAt) : "N/A"}
                          </TableCell>
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {formatExpiry(clip.expiresAt)}
                          </TableCell>
                        </TableRow>

                        <!-- Expanded Share Row -->
                        {#if expandedClip === clip.id && isClipReady(clip.status) && clip.clipHash}
                          <TableRow class="bg-muted/5 border-t-0">
                            <TableCell colspan={8} class="px-4 py-4 cursor-default" onclick={(e) => e.stopPropagation()}>
                              <div class="pl-4 border-l-2 border-primary/20">
                                <PlaybackProtocols
                                  contentId={clip.clipHash}
                                  contentType="clip"
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

                {#if hasMoreClips}
                  <div class="flex justify-center py-4 border-t border-border/30">
                    <Button
                      variant="outline"
                      onclick={loadMoreClips}
                      disabled={loadingMoreClips}
                    >
                      {#if loadingMoreClips}
                        Loading...
                      {:else}
                        Load More Clips
                      {/if}
                    </Button>
                  </div>
                {/if}
              {/if}
            </div>
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

<DeleteClipModal
  open={showDeleteModal && !!clipToDelete}
  clip={clipToDelete}
  deleting={!!deletingClipId}
  onConfirm={deleteClip}
  onCancel={() => {
    showDeleteModal = false;
    clipToDelete = null;
  }}
/>

<!-- Create Clip Modal -->
<Dialog
  open={showCreateModal}
  onOpenChange={(value) => (showCreateModal = value)}
>
  <DialogContent class="max-w-md rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden">
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground">Create New Clip</DialogTitle>
      <DialogDescription class="text-xs text-muted-foreground/70">
        Choose a stream and time range to generate a clip.
      </DialogDescription>
    </DialogHeader>

    <form id="create-clip-form" class="slab-body--padded space-y-4" onsubmit={() => { /* preventDefault(createClip) */ createClip(); }}>
      <!-- Mode Tabs -->
      <div class="space-y-2">
        <span id="clipping-mode-label" class="block text-sm font-medium text-muted-foreground mb-2">
          Clipping Mode
        </span>
        <div role="group" aria-labelledby="clipping-mode-label" class="flex border border-border rounded-md overflow-hidden">
          <button
            type="button"
            class="flex-1 px-3 py-2 text-sm font-medium transition-colors {clipMode === 'CLIP_NOW' ? 'bg-primary text-primary-foreground' : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
            onclick={() => clipMode = 'CLIP_NOW'}
          >
            Clip Now
          </button>
          <button
            type="button"
            class="flex-1 px-3 py-2 text-sm font-medium transition-colors border-x border-border {clipMode === 'DURATION' ? 'bg-primary text-primary-foreground' : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
            onclick={() => clipMode = 'DURATION'}
          >
            Duration
          </button>
          <button
            type="button"
            class="flex-1 px-3 py-2 text-sm font-medium transition-colors {clipMode === 'ABSOLUTE' ? 'bg-primary text-primary-foreground' : 'bg-muted/30 text-muted-foreground hover:bg-muted/50'}"
            onclick={() => clipMode = 'ABSOLUTE'}
          >
            Timestamps
          </button>
        </div>
      </div>

      <div class="space-y-2">
        <label
          for="stream-select"
          class="block text-sm font-medium text-muted-foreground mb-2"
        >
          Stream
        </label>
        <Select bind:value={selectedStreamId} type="single">
          <SelectTrigger id="stream-select" class="w-full">
            <span class={selectedStreamId ? "" : "text-muted-foreground"}>
              {selectedStreamLabel}
            </span>
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
        <label
          for="clip-title"
          class="block text-sm font-medium text-muted-foreground mb-2"
        >
          Title
        </label>
        <Input
          id="clip-title"
          type="text"
          bind:value={clipTitle}
          placeholder="Enter clip title"
          required
        />
      </div>

      <div class="space-y-2">
        <label
          for="clip-description"
          class="block text-sm font-medium text-muted-foreground mb-2"
        >
          Description (optional)
        </label>
        <Textarea
          id="clip-description"
          bind:value={clipDescription}
          placeholder="Enter clip description"
          rows={2}
        />
      </div>

      <!-- Conditional timing fields based on mode -->
      {#if clipMode === 'CLIP_NOW'}
        <!-- Clip Now: Duration presets -->
        <div class="space-y-2">
          <span id="duration-label" class="block text-sm font-medium text-muted-foreground mb-2">
            Duration
          </span>
          <div role="group" aria-labelledby="duration-label" class="flex gap-2">
            {#each durationPresets as preset (preset.value)}
              <button
                type="button"
                class="flex-1 px-3 py-2 text-sm font-medium rounded border transition-colors {duration === preset.value ? 'bg-primary text-primary-foreground border-primary' : 'bg-muted/30 text-muted-foreground border-border hover:bg-muted/50'}"
                onclick={() => duration = preset.value}
              >
                {preset.label}
              </button>
            {/each}
          </div>
          <p class="text-xs text-muted-foreground/70">
            Captures the last {formatDurationSeconds(duration)} from the live stream
          </p>
        </div>
      {:else if clipMode === 'DURATION'}
        <!-- Duration mode: Start time + duration -->
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <label
              for="start-time"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              Start Time (unix)
            </label>
            <Input
              id="start-time"
              type="number"
              bind:value={startTime}
              min="0"
              required
            />
          </div>

          <div class="space-y-2">
            <label
              for="duration-input"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              Duration (seconds)
            </label>
            <Input
              id="duration-input"
              type="number"
              bind:value={duration}
              min="1"
              required
            />
          </div>
        </div>
        <p class="text-xs text-muted-foreground/70 bg-muted/30 p-2 rounded border border-border/50">
          <span class="font-medium">Clip Length:</span> {formatDurationSeconds(duration)}
        </p>
      {:else}
        <!-- Absolute mode: Start and end timestamps -->
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <label
              for="start-time"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              Start Time (unix)
            </label>
            <Input
              id="start-time"
              type="number"
              bind:value={startTime}
              min="0"
              required
            />
          </div>

          <div class="space-y-2">
            <label
              for="end-time"
              class="block text-sm font-medium text-muted-foreground mb-2"
            >
              End Time (unix)
            </label>
            <Input
              id="end-time"
              type="number"
              bind:value={endTime}
              min="1"
              required
            />
          </div>
        </div>

        <p class="text-xs text-muted-foreground/70 bg-muted/30 p-2 rounded border border-border/50">
          <span class="font-medium">Duration:</span> {formatDurationSeconds(Math.max(0, endTime - startTime))}
        </p>
      {/if}
    </form>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
        onclick={() => (showCreateModal = false)}
        disabled={creatingClip}
      >
        Cancel
      </Button>
      <Button
        type="submit"
        variant="ghost"
        class="rounded-none h-12 flex-1 hover:bg-muted/10 text-primary hover:text-primary/80"
        disabled={creatingClip || !selectedStreamId}
        form="create-clip-form"
      >
        {creatingClip ? "Creating..." : "Create Clip"}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>