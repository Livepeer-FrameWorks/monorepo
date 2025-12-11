<script lang="ts">
  import { preventDefault } from "svelte/legacy";

  import { onMount, onDestroy } from "svelte";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    GetStreamsStore,
    GetClipsConnectionStore,
    CreateClipStore,
    ClipLifecycleStore
  } from "$houdini";
  import type { ClipLifecycle$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
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

  let selectedStreamLabel = $derived(
    !selectedStreamId
      ? "Select a stream"
      : streams.find((stream) => stream.id === selectedStreamId)?.name || "Select a stream"
  );

  let clipTitle = $state("");
  let clipDescription = $state("");
  let startTime = $state(0);
  let endTime = $state(300); // 5 minutes default

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

    if (endTime <= startTime) {
      toast.warning("End time must be after start time");
      return;
    }

    try {
      creatingClip = true;

      // Find stream for subscription - use stream.id (internal UUID) as the canonical identifier
      const selectedStream = streams.find(s => s.id === selectedStreamId);
      if (selectedStream) {
        startClipSubscription(selectedStream.id);
      }

      const result = await createClipMutation.mutate({
        input: {
          stream: selectedStreamId,
          title: clipTitle.trim(),
          description: clipDescription.trim() || undefined,
          startTime: Math.floor(startTime),
          endTime: Math.floor(endTime),
        },
      });

      // Check for errors in the union type response using __typename
      const createResult = result.data?.createClip;
      if (createResult?.__typename === "Clip") {
        // Success - Houdini's @list directive will auto-update the list
        toast.success("Clip created successfully!");
      } else if (createResult) {
        // Error response - access message from the error types
        // Houdini types error variants with a "non-exhaustive" pattern, so we cast
        const errorResult = createResult as unknown as { message?: string };
        toast.error(errorResult.message || "Failed to create clip");
      }

      // Reset form
      showCreateModal = false;
      clipTitle = "";
      clipDescription = "";
      selectedStreamId = "";
      startTime = 0;
      endTime = 300;
    } catch (error) {
      console.error("Failed to create clip:", error);
      toast.error("Failed to create clip. Please try again.");
    } finally {
      creatingClip = false;
    }
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

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-border shrink-0">
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
          <Button onclick={() => (showCreateModal = true)}>
            <PlusIcon class="w-4 h-4 mr-2" />
            Create Clip
          </Button>
        {/if}
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto">
  {#if loading}
    <div class="px-4 sm:px-6 lg:px-8 py-6">
      <div class="flex items-center justify-center min-h-64">
        <div class="loading-spinner w-8 h-8"></div>
      </div>
    </div>
  {:else}
    <div class="page-transition">

      <!-- Stats Bar -->
      <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
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
              <EmptyState
                title="No clips yet"
                description="Create your first clip from a stream to get started"
                actionText={streams.length > 0 ? "Create Your First Clip" : ""}
                onAction={() => (showCreateModal = true)}
                showAction={streams.length > 0}
              >
                <ScissorsIcon class="w-6 h-6 text-muted-foreground mx-auto mb-4" />
                {#if streams.length === 0}
                  <p class="text-muted-foreground text-sm mt-2">
                    You need at least one stream to create clips
                  </p>
                {/if}
              </EmptyState>
            {:else}
              <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
                {#each clips as clip (clip.id)}
                  <ClipCard
                    {clip}
                    streamName={clip.streamName}
                    onPlay={() => openClip(clip)}
                  />
                {/each}
              </div>

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
          <div class="slab-actions">
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
  <DialogContent class="max-w-lg">
    <form class="space-y-6" onsubmit={preventDefault(createClip)}>
      <DialogHeader>
        <DialogTitle>Create New Clip</DialogTitle>
        <DialogDescription>
          Choose a stream and time range to generate a clip.
        </DialogDescription>
      </DialogHeader>

      <div class="space-y-4">
        <div class="space-y-2">
          <label
            for="stream-select"
            class="text-sm font-medium text-muted-foreground"
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
            class="text-sm font-medium text-muted-foreground"
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
            class="text-sm font-medium text-muted-foreground"
          >
            Description (optional)
          </label>
          <Textarea
            id="clip-description"
            bind:value={clipDescription}
            placeholder="Enter clip description"
            rows={3}
          />
        </div>

        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <label
              for="start-time"
              class="text-sm font-medium text-muted-foreground"
            >
              Start Time (seconds)
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
              class="text-sm font-medium text-muted-foreground"
            >
              End Time (seconds)
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

        <p class="text-sm text-muted-foreground">
          Duration: {formatDuration(Math.max(0, endTime - startTime))}
        </p>
      </div>

      <DialogFooter class="gap-2">
        <Button
          type="button"
          variant="outline"
          onclick={() => (showCreateModal = false)}
          disabled={creatingClip}
        >
          Cancel
        </Button>
        <Button type="submit" disabled={creatingClip || !selectedStreamId}>
          {creatingClip ? "Creating..." : "Create Clip"}
        </Button>
      </DialogFooter>
    </form>
  </DialogContent>
</Dialog>

<!-- Clip Player Modal -->
<ClipModal clip={selectedClip} onClose={closeClip} />
