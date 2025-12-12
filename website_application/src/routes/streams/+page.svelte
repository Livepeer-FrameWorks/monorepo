<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    GetStreamsConnectionStore,
    GetCurrentStreamHealthStore,
    CreateStreamStore,
    DeleteStreamStore,
    StreamEventsStore,
    StreamStatus,
    StreamEventType
  } from "$houdini";
  import type { StreamEvents$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import LoadingCard from "$lib/components/LoadingCard.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import StreamCard from "$lib/components/stream-details/StreamCard.svelte";
  import CreateStreamModal from "$lib/components/stream-details/CreateStreamModal.svelte";
  import DeleteStreamModal from "$lib/components/stream-details/DeleteStreamModal.svelte";
  import GridSeam from "$lib/components/layout/GridSeam.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";

  // Houdini stores
  const streamsConnectionStore = new GetStreamsConnectionStore();
  const createStreamMutation = new CreateStreamStore();
  const deleteStreamMutation = new DeleteStreamStore();
  const streamEventsSub = new StreamEventsStore();

  // Types from Houdini
  type StreamData = NonNullable<NonNullable<NonNullable<typeof $streamsConnectionStore.data>["streamsConnection"]>["edges"]>[0]["node"];
  type StreamEventData = NonNullable<StreamEvents$result["streamEvents"]>;

  let isAuthenticated = false;

  // Derived state from Houdini stores
  let streamsEdges = $derived($streamsConnectionStore.data?.streamsConnection?.edges ?? []);
  let streams = $state<StreamData[]>([]);
  let loading = $derived($streamsConnectionStore.fetching);
  let hasMoreStreams = $derived($streamsConnectionStore.data?.streamsConnection?.pageInfo?.hasNextPage ?? false);
  let totalStreamCount = $derived($streamsConnectionStore.data?.streamsConnection?.totalCount ?? 0);

  let loadingMoreStreams = $state(false);

  // Stream health data for all streams
  let streamHealthData = $state(new Map());

  // Search/filter
  let searchQuery = $state("");
  let statusFilter = $state<"all" | "live" | "offline">("all");

  // Stream creation
  let creatingStream = $state(false);
  let showCreateModal = $state(false);
  let newStreamTitle = $state("");
  let newStreamDescription = $state("");
  let newStreamRecord = $state(false);

  // Stream deletion
  let deletingStreamId = $state("");
  let showDeleteModal = $state(false);
  let streamToDelete = $state<StreamData | null>(null);

  // Filtered streams
  let filteredStreams = $derived.by(() => {
    let result = streams;

    // Filter by search query
    if (searchQuery.trim()) {
      const query = searchQuery.toLowerCase();
      result = result.filter(s =>
        s.name.toLowerCase().includes(query) ||
        s.description?.toLowerCase().includes(query)
      );
    }

    // Filter by status
    if (statusFilter === "live") {
      result = result.filter(s => s.metrics?.isLive);
    } else if (statusFilter === "offline") {
      result = result.filter(s => !s.metrics?.isLive);
    }

    return result;
  });

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  // Effect to sync derived streams edges to local state
  $effect(() => {
    if (streamsEdges.length > 0) {
      streams = streamsEdges.map(e => e.node);
    }
  });

  // Effect to handle stream events subscription errors
  $effect(() => {
    const errors = $streamEventsSub.errors;
    if (errors?.length) {
      console.warn("Stream events subscription error:", errors);
    }
  });

  // Effect to handle stream events subscription
  // Use untrack to prevent effect loops when mutating state
  $effect(() => {
    const event = $streamEventsSub.data?.streamEvents;
    if (event) {
      untrack(() => handleStreamEvent(event));
    }
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadStreams();

    // Start stream events subscription (for all streams, no filter)
    streamEventsSub.listen({});
  });

  // Cleanup on unmount
  onDestroy(() => {
    streamEventsSub.unlisten();
  });

  function handleStreamEvent(event: StreamEventData) {
    // Find and update the stream in our list
    const streamIndex = streams.findIndex(s => s.name === event.stream || s.id === event.stream);

    if (streamIndex >= 0) {
      // Update stream status based on event
      const updatedStream = { ...streams[streamIndex] };

      if (event.status && updatedStream.metrics) {
        updatedStream.metrics = {
          ...updatedStream.metrics,
          status: event.status as typeof updatedStream.metrics.status,
          isLive: event.status === "LIVE",
        };
      } else if (event.status) {
        // Initialize metrics if missing
        updatedStream.metrics = {
          status: event.status,
          isLive: event.status === "LIVE",
          currentViewers: 0,
          peakViewers: 0,
          totalViews: 0,
          duration: 0,
        } as typeof updatedStream.metrics;
      }

      streams = [
        ...streams.slice(0, streamIndex),
        updatedStream,
        ...streams.slice(streamIndex + 1),
      ];

      // Show toast for significant events
      if (event.type === StreamEventType.STREAM_START || event.status === StreamStatus.LIVE) {
        toast.success(`Stream "${event.stream}" is now live!`);
      } else if (event.type === StreamEventType.STREAM_END || event.status === StreamStatus.OFFLINE) {
        toast.info(`Stream "${event.stream}" went offline`);
      }
    }
  }

  async function loadStreams() {
    try {
      await streamsConnectionStore.fetch({
        variables: { first: 50 },
        policy: "NetworkOnly",
      });

      // Load health data for all streams
      await loadStreamsHealthData();
    } catch (error) {
      console.error("Failed to load streams:", error);
    }
  }

  async function loadMoreStreams() {
    if (!hasMoreStreams || loadingMoreStreams) return;

    try {
      loadingMoreStreams = true;
      await streamsConnectionStore.loadNextPage();

      // Load health data for new streams
      await loadStreamsHealthData();
    } catch (error) {
      console.error("Failed to load more streams:", error);
    } finally {
      loadingMoreStreams = false;
    }
  }

  // Load health data for all streams
  async function loadStreamsHealthData() {
    const healthPromises = streams.map(async (stream) => {
      try {
        const healthStore = new GetCurrentStreamHealthStore();
        // Use stream.id (internal UUID) for analytics queries - this is the canonical identifier
        const result = await healthStore.fetch({ variables: { stream: stream.id } });
        const health = result.data?.currentStreamHealth;
        if (health) {
          streamHealthData.set(stream.id, health);
        }
      } catch (error) {
        console.warn(
          `Failed to load health data for stream ${stream.id}:`,
          error,
        );
      }
    });

    await Promise.allSettled(healthPromises);
    // Trigger reactive update
    streamHealthData = streamHealthData;
  }

  // Create new stream
  async function createStream() {
    if (!newStreamTitle.trim()) {
      toast.warning("Please enter a stream title");
      return;
    }

    try {
      creatingStream = true;
      const result = await createStreamMutation.mutate({
        input: {
          name: newStreamTitle.trim(),
          description: newStreamDescription.trim() || undefined,
          record: newStreamRecord,
        },
      });

      const createResult = result.data?.createStream;
      if (createResult?.__typename === "Stream") {
        // Add new stream to list
        const newStream = createResult as StreamData;
        streams = [...streams, newStream];

        // Close modal and reset form
        showCreateModal = false;
        newStreamTitle = "";
        newStreamDescription = "";
        newStreamRecord = false;

        toast.success("Stream created successfully!");

        // Refresh list to keep pagination/pageInfo in sync (non-blocking)
        try {
          await streamsConnectionStore.fetch({
            variables: { first: 50 },
            policy: "NetworkOnly",
          });
        } catch (refreshErr) {
          console.warn("Failed to refresh streams after create:", refreshErr);
        }

        // Navigate to the new stream's detail page
        goto(resolve(`/streams/${newStream.id}`));
      } else if (createResult) {
        const errorResult = createResult as unknown as { message?: string };
        toast.error(errorResult.message || "Failed to create stream");
      }
    } catch (error) {
      console.error("Failed to create stream:", error);
      toast.error("Failed to create stream. Please try again.");
    } finally {
      creatingStream = false;
    }
  }

  // Delete stream
  async function deleteStream() {
    if (!streamToDelete) return;
    const idToDelete = streamToDelete.id;

    try {
      deletingStreamId = idToDelete;
      const result = await deleteStreamMutation.mutate({ id: idToDelete });

      const deleteResult = result.data?.deleteStream;
      if (deleteResult?.__typename === "DeleteSuccess") {
        // Remove stream from list
        streams = streams.filter((s) => s.id !== idToDelete);

        // Refresh list and total counts from server
        streamsConnectionStore.fetch({ policy: "NetworkOnly", variables: { first: 50 } });

        // Close modal
        showDeleteModal = false;
        streamToDelete = null;

        toast.success("Stream deleted successfully!");

        // Refresh list and total counts from server (non-blocking)
        try {
          await streamsConnectionStore.fetch({
            variables: { first: 50 },
            policy: "NetworkOnly",
          });
        } catch (refreshErr) {
          console.warn("Failed to refresh streams after delete:", refreshErr);
        }
      } else if (deleteResult) {
        const errorResult = deleteResult as unknown as { message?: string };
        toast.error(errorResult.message || "Failed to delete stream");
      }
    } catch (error) {
      console.error("Failed to delete stream:", error);
      toast.error("Failed to delete stream. Please try again.");
    } finally {
      deletingStreamId = "";
    }
  }

  // Navigate to stream detail page
  function navigateToStream(stream: StreamData) {
    goto(resolve(`/streams/${stream.id}`));
  }

  // Show delete confirmation
  function confirmDeleteStream(stream: StreamData) {
    streamToDelete = stream;
    showDeleteModal = true;
  }

  // Count live streams
  let liveStreamCount = $derived(streams.filter(s => s.metrics?.isLive).length);

  const PlusIcon = getIconComponent("Plus");
  const VideoIcon = getIconComponent("Video");
  const SearchIcon = getIconComponent("Search");
  const RadioIcon = getIconComponent("Radio");
  const CircleOffIcon = getIconComponent("CircleOff");
  const LayoutGridIcon = getIconComponent("LayoutGrid");
</script>

<svelte:head>
  <title>Streams - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 z-10 bg-background">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <VideoIcon class="w-5 h-5 text-primary" />
        <div>
          <div class="flex items-center gap-3">
            <h1 class="text-xl font-bold text-foreground">Streams</h1>
            <span class="text-xs text-muted-foreground font-mono bg-muted px-1.5 py-0.5 rounded">
              {totalStreamCount > 0 ? totalStreamCount : streams.length}
            </span>
            {#if liveStreamCount > 0}
              <span class="text-xs text-success font-medium flex items-center gap-1.5">
                <div class="w-1.5 h-1.5 rounded-full bg-success animate-pulse"></div>
                {liveStreamCount} LIVE
              </span>
            {/if}
          </div>
          <p class="text-sm text-muted-foreground">
            Manage your live streams and broadcasts
          </p>
        </div>
      </div>
      <Button
        variant="default"
        class="gap-2"
        onclick={() => (showCreateModal = true)}
      >
        <PlusIcon class="w-4 h-4" />
        Create Stream
      </Button>
    </div>
  </div>

  <!-- Filters Toolbar -->
  <div class="w-full border-b border-[hsl(var(--tn-fg-gutter)/0.3)] bg-muted/20 shrink-0">
    <div class="py-3 px-6 flex gap-4 items-center">
      <div class="relative flex-1 max-w-md">
        <SearchIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
        <Input
          type="text"
          placeholder="Search streams..."
          class="pl-9 bg-background/50 border-border/50 focus:bg-background transition-colors h-9"
          bind:value={searchQuery}
        />
      </div>
      <div class="flex items-center gap-2 ml-auto">
        <Button
          variant="ghost"
          size="sm"
          class={statusFilter === 'all' ? 'text-primary bg-primary/5' : 'text-muted-foreground'}
          onclick={() => statusFilter = 'all'}
        >
          <LayoutGridIcon class="w-4 h-4 mr-2" /> All
        </Button>
        <Button
          variant="ghost"
          size="sm"
          class={statusFilter === 'live' ? 'text-primary bg-primary/5' : 'text-muted-foreground'}
          onclick={() => statusFilter = 'live'}
        >
          <RadioIcon class="w-4 h-4 mr-2" /> Live
        </Button>
        <Button
          variant="ghost"
          size="sm"
          class={statusFilter === 'offline' ? 'text-primary bg-primary/5' : 'text-muted-foreground'}
          onclick={() => statusFilter = 'offline'}
        >
          <CircleOffIcon class="w-4 h-4 mr-2" /> Offline
        </Button>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto min-h-0 bg-background/50">
    {#if loading}
      <!-- Loading Skeleton -->
      <GridSeam cols={3} stack="md" flush={true} class="min-h-full content-start">
        {#each Array.from({ length: 6 }) as _, i (i)}
          <div class="slab h-full !p-0">
             <div class="slab-body--padded">
               <div class="space-y-3 animate-pulse">
                 <div class="h-4 bg-muted rounded w-3/4"></div>
                 <div class="h-32 bg-muted rounded"></div>
               </div>
             </div>
          </div>
        {/each}
      </GridSeam>
    {:else if streams.length === 0}
      <!-- No Streams State -->
      <div class="h-full flex items-center justify-center p-8">
        <div class="slab w-full max-w-lg border border-border/50 shadow-xl">
          <div class="slab-body--padded flex flex-col items-center text-center py-12">
            <div class="w-12 h-12 rounded-full bg-primary/10 flex items-center justify-center mb-4">
              <VideoIcon class="w-6 h-6 text-primary" />
            </div>
            <h3 class="text-lg font-semibold mb-2">No Streams Found</h3>
            <p class="text-muted-foreground mb-6 max-w-xs">
              Create your first stream to get started with broadcasting to the world.
            </p>
            <Button variant="default" onclick={() => (showCreateModal = true)}>
              <PlusIcon class="w-4 h-4 mr-2" />
              Create Stream
            </Button>
          </div>
        </div>
      </div>
    {:else if filteredStreams.length === 0}
      <!-- No Results State -->
      <div class="h-full flex items-center justify-center p-8">
        <div class="slab w-full max-w-lg border border-border/50">
          <div class="slab-body--padded flex flex-col items-center text-center py-12">
            <div class="w-12 h-12 rounded-full bg-muted flex items-center justify-center mb-4">
              <SearchIcon class="w-6 h-6 text-muted-foreground" />
            </div>
            <h3 class="text-lg font-semibold mb-2">No Matching Streams</h3>
            <p class="text-muted-foreground mb-6 max-w-xs">
              Try adjusting your search query or changing the status filters.
            </p>
            <Button 
              variant="outline" 
              onclick={() => {
                searchQuery = "";
                statusFilter = "all";
              }}
            >
              Clear Filters
            </Button>
          </div>
        </div>
      </div>
    {:else}
      <!-- Stream Cards Grid -->
      <GridSeam cols={3} stack="md" flush={true} class="min-h-full content-start">
        {#each filteredStreams as stream (stream.id)}
          <StreamCard
            {stream}
            selected={false}
            healthData={streamHealthData.get(stream.id) || null}
            onSelect={() => navigateToStream(stream)}
            onDelete={() => confirmDeleteStream(stream)}
            deleting={deletingStreamId === stream.id}
          />
        {/each}
      </GridSeam>

      {#if hasMoreStreams}
        <div class="flex justify-center py-8">
          <Button
            variant="outline"
            onclick={loadMoreStreams}
            disabled={loadingMoreStreams}
          >
            {#if loadingMoreStreams}
              Loading...
            {:else}
              Load More Streams
            {/if}
          </Button>
        </div>
      {/if}
    {/if}
  </div>
</div>

<CreateStreamModal
  open={showCreateModal}
  bind:title={newStreamTitle}
  bind:description={newStreamDescription}
  bind:record={newStreamRecord}
  creating={creatingStream}
  onSubmit={createStream}
  onCancel={() => {
    showCreateModal = false;
    newStreamTitle = "";
    newStreamDescription = "";
    newStreamRecord = false;
  }}
/>

<!-- Delete Stream Modal -->
<DeleteStreamModal
  open={showDeleteModal && !!streamToDelete}
  stream={streamToDelete}
  deleting={!!deletingStreamId}
  onConfirm={deleteStream}
  onCancel={() => {
    showDeleteModal = false;
    streamToDelete = null;
  }}
/>
