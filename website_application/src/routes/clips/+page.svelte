<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { resolve, base } from "$app/paths"; // Ensure base is imported for correct path resolution
  import { goto } from "$app/navigation";
  import { auth } from "$lib/stores/auth";
  import {
    GetStreamsStore,
    GetClipsConnectionStore,
    CreateClipStore,
    ClipLifecycleStore,
    ClipCreationMode
  } from "$houdini";
  import type { ClipLifecycle$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte"; // Still needed for skeleton fallback? No, replacing with slab-skeletons.
  import EmptyState from "$lib/components/EmptyState.svelte";
  import ClipModal from "$lib/components/ClipModal.svelte";
  import { Button } from "$lib/components/ui/button";
  import { ClipCard } from "$lib/components/cards";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
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
  import { getIconComponent } from "$lib/iconUtils";

  // Houdini stores
  const streamsStore = new GetStreamsStore();
  const clipsConnectionStore = new GetClipsConnectionStore();
  const createClipMutation = new CreateClipStore();
  const clipLifecycleSub = new ClipLifecycleStore();

  // Types from Houdini
  type StreamData = NonNullable<NonNullable<typeof $streamsStore.data>["streams"]>[0];
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
  let streams = $derived($streamsStore.data?.streams ?? []);
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

  // Clip viewing
  let selectedClip = $state<ClipData | null>(null);

  // Active stream for clip lifecycle subscription
  let activeClipStream = $state<string | null>(null);

  // Track clip progress for real-time updates
  let clipProgress = $state<Record<string, { stage: string; percent: number; message?: string }>>({});

  // Derived stats
  let processingClips = $derived(clips.filter(c => c.status === "Processing" || c.status === "processing").length);
  let completedClips = $derived(clips.filter(c => c.status === "Available" || c.status === "completed").length);
  let failedClips = $derived(clips.filter(c => c.status === "Failed" || c.status === "failed").length);

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
      if (createResult?.__typename === "Clip") {
        // Success - Houdini's @list directive will auto-update the list
        const modeLabel = clipMode === 'CLIP_NOW' ? ' (from live)' : '';
        toast.success(`Clip created successfully${modeLabel}!`);
      } else if (createResult) {
        // Error response - access message from the error types
        // Houdini types error variants with a "non-exhaustive" pattern, so we cast
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

  function formatDuration(seconds: number) {
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    return `${minutes}:${remainingSeconds.toString().padStart(2, "0")}`;
  }

  function openClip(clip: ClipData) {
    selectedClip = clip;
  }

  function closeClip() {
    selectedClip = null;
  }

  // Icons
  const ScissorsIcon = getIconComponent("Scissors");
  const FilmIcon = getIconComponent("Film");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const LoaderIcon = getIconComponent("Loader");
  const XCircleIcon = getIconComponent("XCircle");
  const PlusIcon = getIconComponent("Plus");
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
      <div class="flex items-center gap-3">
        {#if streamsError}
          <Button
            variant="destructive"
            onclick={loadData}
            title="Failed to load streams. Click to retry."
          >
            Retry Loading
          </Button>
        {:else if streams.length === 0}
          <Button
            variant="outline"
            disabled
            title="No streams available. Create a stream first to make clips."
            class="cursor-not-allowed opacity-60"
          >
            <PlusIcon class="w-4 h-4 mr-2" />
            Create Clip
          </Button>
        {:else}
          <Button
            variant="default"
            class="gap-2"
            onclick={() => (showCreateModal = true)}
          >
            <PlusIcon class="w-4 h-4" />
            Create Clip
          </Button>
        {/if}
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
        <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="min-h-full content-start">
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
          <!-- Clips Grid Slab -->
          <div class="slab col-span-full">
            <div class="slab-header">
              <div class="flex items-center gap-2">
                <ScissorsIcon class="w-4 h-4 text-accent-purple" />
                <h3>Your Clips</h3>
              </div>
            </div>
            <div class="slab-body--padded">
              {#if clips.length === 0}
                <div class="flex flex-col items-center justify-center py-16 m-4 border-2 border-dashed border-border/50 rounded-lg bg-muted/5">
                  <div class="w-16 h-16 rounded-full bg-muted/30 flex items-center justify-center mb-6">
                    <ScissorsIcon class="w-8 h-8 text-muted-foreground" />
                  </div>
                  <h3 class="text-xl font-semibold mb-3">No clips yet</h3>
                  <p class="text-muted-foreground mb-8 max-w-sm text-lg text-center">
                    Create your first clip from a stream to get started
                  </p>
                  {#if streams.length > 0}
                    <Button variant="ghost" size="lg" class="h-14 px-8" onclick={() => (showCreateModal = true)}>
                      <PlusIcon class="w-5 h-5 mr-2" />
                      Create Your First Clip
                    </Button>
                  {:else}
                    <p class="text-muted-foreground text-sm mt-2">
                      You need at least one stream to create clips
                    </p>
                  {/if}
                </div>
              {:else}
                <GridSeam cols={3} stack="md" flush={true} class="min-h-full content-start">
                  {#each clips as clip (clip.id)}
                    <ClipCard
                      {clip}
                      streamName={clip.streamName}
                      onPlay={() => openClip(clip)}
                      class="h-auto"
                    />
                  {/each}
                </GridSeam>

                {#if hasMoreClips}
                  <div class="flex justify-center pt-6">
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
            <div class="slab-actions slab-actions--row">
              <Button href={resolve("/recordings")} variant="ghost" class="gap-2">
                <FilmIcon class="w-4 h-4" />
                View Recordings
              </Button>
            </div>
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

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
        <label class="block text-sm font-medium text-muted-foreground mb-2">
          Clipping Mode
        </label>
        <div class="flex border border-border rounded-md overflow-hidden">
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
          <label class="block text-sm font-medium text-muted-foreground mb-2">
            Duration
          </label>
          <div class="flex gap-2">
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
            Captures the last {formatDuration(duration)} from the live stream
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
          <span class="font-medium">Clip Length:</span> {formatDuration(duration)}
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
          <span class="font-medium">Duration:</span> {formatDuration(Math.max(0, endTime - startTime))}
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

<!-- Clip Player Modal -->
<ClipModal clip={selectedClip} onClose={closeClip} />
