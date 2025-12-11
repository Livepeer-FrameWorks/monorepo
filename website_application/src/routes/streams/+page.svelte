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
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-border shrink-0">
    <div class="flex flex-col sm:flex-row sm:justify-between sm:items-center gap-4">
      <div class="flex items-center gap-3">
        <VideoIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Streams</h1>
          <p class="text-sm text-muted-foreground">
            {totalStreamCount > 0 ? totalStreamCount : streams.length} stream{streams.length !== 1 ? 's' : ''}
            {#if liveStreamCount > 0}
              <span class="text-success">• {liveStreamCount} live</span>
            {/if}
          </p>
        </div>
      </div>

      <Button
        variant="cta"
        class="gap-2"
        onclick={() => (showCreateModal = true)}
      >
        <PlusIcon class="w-4 h-4" />
        Create Stream
      </Button>
    </div>

    <!-- Search and Filters -->
    <div class="flex flex-col sm:flex-row gap-3 mt-4">
      <div class="relative flex-1 max-w-md">
        <SearchIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
        <Input
          type="text"
          placeholder="Search streams..."
          class="pl-9"
          bind:value={searchQuery}
        />
      </div>

      <div class="flex gap-2">
        <Button
          variant={statusFilter === "all" ? "default" : "outline"}
          size="sm"
          class="gap-1.5"
          onclick={() => (statusFilter = "all")}
        >
          <LayoutGridIcon class="w-3.5 h-3.5" />
          All
        </Button>
        <Button
          variant={statusFilter === "live" ? "default" : "outline"}
          size="sm"
          class="gap-1.5"
          onclick={() => (statusFilter = "live")}
        >
          <RadioIcon class="w-3.5 h-3.5" />
          Live
        </Button>
        <Button
          variant={statusFilter === "offline" ? "default" : "outline"}
          size="sm"
          class="gap-1.5"
          onclick={() => (statusFilter = "offline")}
        >
          <CircleOffIcon class="w-3.5 h-3.5" />
          Offline
        </Button>
      </div>
    </div>
  </div>

  <!-- Scrollable Content -->
  <div class="flex-1 overflow-y-auto p-4 sm:p-6 lg:p-8">
    {#if loading}
      <!-- Loading Skeleton -->
      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
        {#each Array.from({ length: 6 }) as _, i (i)}
          <LoadingCard variant="stream" />
        {/each}
      </div>
    {:else if streams.length === 0}
      <!-- No Streams State -->
      <EmptyState
        iconName="Video"
        title="No Streams Found"
        description="Create your first stream to get started with broadcasting"
        actionText="Create Stream"
        onAction={() => (showCreateModal = true)}
      />
    {:else if filteredStreams.length === 0}
      <!-- No Results State -->
      <EmptyState
        iconName="Search"
        title="No Matching Streams"
        description="Try adjusting your search or filter criteria"
        actionText="Clear Filters"
        onAction={() => {
          searchQuery = "";
          statusFilter = "all";
        }}
      />
    {:else}
      <!-- Stream Cards Grid -->
      <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-4">
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
      </div>

      {#if hasMoreStreams}
        <div class="flex justify-center mt-6">
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
