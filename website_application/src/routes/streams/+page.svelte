<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import { get } from "svelte/store";
  import { goto } from "$app/navigation";
  import { resolve } from "$app/paths";
  import { auth } from "$lib/stores/auth";
  import {
    fragment,
    GetStreamsConnectionStore,
    CreateStreamStore,
    DeleteStreamStore,
    StreamEventsStore,
    StreamCoreFieldsStore,
    StreamMetricsFieldsStore,
    PageInfoFieldsStore
  } from "$houdini";
  import type { StreamEvents$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import CreateStreamModal from "$lib/components/stream-details/CreateStreamModal.svelte";
  import DeleteStreamModal from "$lib/components/stream-details/DeleteStreamModal.svelte";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import BufferStateIndicator from "$lib/components/health/BufferStateIndicator.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { Button } from "$lib/components/ui/button";
  import { Input } from "$lib/components/ui/input";
  import {
    Table,
    TableHeader,
    TableHead,
    TableRow,
    TableBody,
    TableCell,
  } from "$lib/components/ui/table";
  import {
    Select,
    SelectTrigger,
    SelectContent,
    SelectItem,
  } from "$lib/components/ui/select";
  import { formatDuration } from "$lib/utils/formatters.js";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";

  // Houdini stores
  const streamsConnectionStore = new GetStreamsConnectionStore();
  const createStreamMutation = new CreateStreamStore();
  const deleteStreamMutation = new DeleteStreamStore();
  const streamEventsSub = new StreamEventsStore();

  // Fragment stores for unmasking nested data
  const streamCoreStore = new StreamCoreFieldsStore();
  const metricsStore = new StreamMetricsFieldsStore();
  const pageInfoStore = new PageInfoFieldsStore();

  // Types from Houdini
  type StreamEventData = NonNullable<StreamEvents$result["streamEvents"]>;
  // StreamData type will be inferred from unmaskedStreams after unmasking

  let isAuthenticated = false;

  // Derived state from Houdini stores
  let streamsEdges = $derived($streamsConnectionStore.data?.streamsConnection?.edges ?? []);

  // Get masked nodes from edges - guard against null nodes
  let maskedNodes = $derived(
    streamsEdges
      .map(e => e.node)
      .filter((node): node is NonNullable<typeof node> => node != null)
  );

  // Unmask streams with fragment() and get() pattern
  // Fragment stores can return undefined during store transitions, so guard robustly
  let unmaskedStreams = $derived(
    maskedNodes
      .map(node => {
        try {
          const core = get(fragment(node, streamCoreStore));
          // Guard: must have core object with required fields
          if (!core || typeof core !== 'object' || !core.id || !core.name) {
            return null;
          }
          const metrics = node.metrics ? get(fragment(node.metrics, metricsStore)) : null;
          return { ...core, metrics };
        } catch {
          // Fragment access can throw during store updates
          return null;
        }
      })
      .filter((s): s is NonNullable<typeof s> => s !== null)
  );

  // Local state for streams (can be updated by events)
  let streams = $state<typeof unmaskedStreams>([]);
  let loading = $derived($streamsConnectionStore.fetching);
  // Unmask pageInfo to access hasNextPage
  let pageInfo = $derived.by(() => {
    const masked = $streamsConnectionStore.data?.streamsConnection?.pageInfo;
    return masked ? get(fragment(masked, pageInfoStore)) : null;
  });
  let hasMoreStreams = $derived(pageInfo?.hasNextPage ?? false);
  let totalStreamCount = $derived($streamsConnectionStore.data?.streamsConnection?.totalCount ?? 0);

  let loadingMoreStreams = $state(false);

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
  let streamToDelete = $state<(typeof unmaskedStreams)[number] | null>(null);

  // Expanded row tracking
  let expandedStreamId = $state<string | null>(null);

  // Filtered streams
  let filteredStreams = $derived.by(() => {
    // Filter out any streams with missing required fields
    let result = streams.filter(s => s && s.id && s.name);

    // Filter by search query
    if (searchQuery.trim()) {
      const query = searchQuery.toLowerCase();
      result = result.filter(s =>
        s.name.toLowerCase().includes(query) ||
        s.description?.toLowerCase().includes(query) ||
        s.id.toLowerCase().includes(query)
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

  // Stats - guard against undefined entries
  let liveStreamCount = $derived(streams.filter(s => s?.metrics?.isLive).length);
  let offlineStreamCount = $derived(streams.length - liveStreamCount);
  let totalViewers = $derived(streams.reduce((acc, s) => acc + (s?.metrics?.currentViewers || 0), 0));

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  // Effect to sync unmasked streams to local state
  $effect(() => {
    if (unmaskedStreams.length > 0) {
      streams = unmaskedStreams;
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
    // Extract stream name from the appropriate payload
    const streamName = event.lifecycleUpdate?.internalName || event.endEvent?.streamName;
    if (!streamName) return;

    // Find and update the stream in our list (safely handle potentially undefined fields)
    const streamIndex = streams.findIndex(s => s?.name === streamName || s?.id === streamName);

    if (streamIndex >= 0) {
      // Update stream status based on event
      const updatedStream = { ...streams[streamIndex] };

      // Handle lifecycle update (stream going live/offline)
      if (event.lifecycleUpdate) {
        const isLive = event.lifecycleUpdate.status === "live";
        const status = isLive ? "LIVE" : "OFFLINE";

        if (updatedStream.metrics) {
          updatedStream.metrics = {
            ...updatedStream.metrics,
            status: status as typeof updatedStream.metrics.status,
            isLive,
            currentViewers: event.lifecycleUpdate.totalViewers ?? updatedStream.metrics.currentViewers,
          };
        } else {
          // Initialize metrics when missing
          updatedStream.metrics = {
            status,
            isLive,
            currentViewers: event.lifecycleUpdate.totalViewers ?? 0,
            peakViewers: 0,
            totalViews: 0,
            duration: 0,
          } as NonNullable<typeof updatedStream.metrics>;
        }
      }

      // Handle stream end event
      if (event.endEvent) {
        if (updatedStream.metrics) {
          updatedStream.metrics = {
            ...updatedStream.metrics,
            status: "OFFLINE" as typeof updatedStream.metrics.status,
            isLive: false,
          };
        }
      }

      streams = [
        ...streams.slice(0, streamIndex),
        updatedStream,
        ...streams.slice(streamIndex + 1),
      ];

      // Show toast for significant events
      const isStreamLive = event.eventType === "EVENT_TYPE_STREAM_LIFECYCLE_UPDATE" &&
                           event.lifecycleUpdate?.status === "live";
      const isStreamEnd = event.eventType === "EVENT_TYPE_STREAM_END" ||
                          (event.lifecycleUpdate?.status === "offline");

      if (isStreamLive) {
        toast.success(`Stream "${streamName}" is now live!`);
      } else if (isStreamEnd) {
        toast.info(`Stream "${streamName}" went offline`);
      }
    }
  }

  async function loadStreams() {
    try {
      await streamsConnectionStore.fetch({
        variables: { first: 50 },
        policy: "NetworkOnly",
      });
    } catch (error) {
      console.error("Failed to load streams:", error);
    }
  }

  async function loadMoreStreams() {
    if (!hasMoreStreams || loadingMoreStreams) return;

    try {
      loadingMoreStreams = true;
      await streamsConnectionStore.loadNextPage();
    } catch (error) {
      console.error("Failed to load more streams:", error);
    } finally {
      loadingMoreStreams = false;
    }
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
      // Houdini types unions with error types only in the discriminator, so check for non-error
      const isError = createResult?.__typename === "ValidationError" ||
                      createResult?.__typename === "AuthError";
      if (createResult && !isError) {
        // Add new stream to list (cast through unknown since mutation result has union type)
        const newStream = createResult as unknown as (typeof unmaskedStreams)[number];
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
  function navigateToStream(streamId: string) {
    goto(resolve(`/streams/${streamId}`));
  }
  
  // Navigate to watch page
  function watchStream(streamId: string) {
    goto(`/view?type=live&id=${streamId}`);
  }

  // Show delete confirmation
  function confirmDeleteStream(stream: (typeof unmaskedStreams)[number]) {
    streamToDelete = stream;
    showDeleteModal = true;
  }

  function getStatusColor(status: string | null | undefined): string {
    switch (status?.toLowerCase()) {
      case "live":
        return "text-success bg-success/10 border-success/20";
      case "offline":
      case "idle":
        return "text-muted-foreground bg-muted border-border";
      default:
        return "text-muted-foreground bg-muted border-border";
    }
  }

  // Icons
  const PlusIcon = getIconComponent("Plus");
  const VideoIcon = getIconComponent("Video");
  const SearchIcon = getIconComponent("Search");
  const FilterIcon = getIconComponent("Filter");
  const SignalIcon = getIconComponent("Signal");
  const WifiOffIcon = getIconComponent("WifiOff");
  const UsersIcon = getIconComponent("Users");
  const Trash2Icon = getIconComponent("Trash2");
  const PlayIcon = getIconComponent("Play");
  const Share2Icon = getIconComponent("Share2");
  const ChevronDownIcon = getIconComponent("ChevronDown");
  const ChevronUpIcon = getIconComponent("ChevronUp");
</script>

<svelte:head>
  <title>Streams - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col overflow-hidden">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 z-10 bg-background">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <VideoIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Streams</h1>
          <p class="text-sm text-muted-foreground">
            Manage your live streams and broadcasts
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
        </div>
      {/each}
    </GridSeam>
  {:else}
    <div class="page-transition">

      <!-- Stats Bar -->
      <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0 min-h-full content-start">
        <div>
          <DashboardMetricCard
            icon={VideoIcon}
            iconColor="text-primary"
            value={totalStreamCount}
            valueColor="text-primary"
            label="Total Streams"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={SignalIcon}
            iconColor="text-success"
            value={liveStreamCount}
            valueColor="text-success"
            label="Live Now"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={WifiOffIcon}
            iconColor="text-muted-foreground"
            value={offlineStreamCount}
            valueColor="text-muted-foreground"
            label="Offline"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={UsersIcon}
            iconColor="text-info"
            value={totalViewers}
            valueColor="text-info"
            label="Total Viewers"
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
                  Search Streams
                </label>
                <div class="relative">
                  <SearchIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                  <Input
                    id="search"
                    type="text"
                    bind:value={searchQuery}
                    placeholder="Search by name, ID, or description..."
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
                    {statusFilter === 'all' ? 'All Statuses' : statusFilter === 'live' ? 'Live' : 'Offline'}
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">All Statuses</SelectItem>
                    <SelectItem value="live">Live</SelectItem>
                    <SelectItem value="offline">Offline</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
          </div>
        </div>

        <!-- Streams Table Slab -->
        <div class="slab col-span-full">
          <div class="slab-header flex justify-between items-center">
            <div class="flex items-center gap-2">
              <VideoIcon class="w-4 h-4 text-info" />
              <h3>All Streams</h3>
            </div>
            <Button
              variant="outline"
              size="sm"
              class="gap-2 h-8"
              onclick={() => (showCreateModal = true)}
            >
              <PlusIcon class="w-3.5 h-3.5" />
              Create Stream
            </Button>
          </div>
          <div class="slab-body--flush">
            {#if filteredStreams.length === 0}
              {#if searchQuery || statusFilter !== "all"}
                <div class="p-8">
                  <EmptyState
                    iconName="Video"
                    title="No streams found"
                    description="Try adjusting your search query or changing the status filters."
                    actionText="Clear Filters"
                    onAction={() => {
                      searchQuery = "";
                      statusFilter = "all";
                    }}
                  />
                </div>
              {:else}
                <div class="p-8">
                  <EmptyState
                    iconName="Video"
                    title="No streams found"
                    description="Create your first stream to get started with broadcasting."
                    actionText="Create Stream"
                    onAction={() => (showCreateModal = true)}
                  />
                </div>
              {/if}
            {:else}
              <div class="overflow-x-auto">
                <Table class="w-full">
                  <TableHeader>
                    <TableRow>
                      <TableHead
                        class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider w-[120px]"
                      >
                        Actions
                      </TableHead>
                      <TableHead
                        class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >
                        Stream
                      </TableHead>
                      <TableHead
                        class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >
                        Status
                      </TableHead>
                      <TableHead
                        class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >
                        Health
                      </TableHead>
                      <TableHead
                        class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >
                        Viewers
                      </TableHead>
                      <TableHead
                        class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >
                        Duration
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody class="divide-y divide-border">
                    {#each filteredStreams as stream (stream.id)}
                      <TableRow
                        class="hover:bg-muted/50 transition-colors cursor-pointer group"
                        onclick={() => navigateToStream(stream.id)}
                      >
                        <!-- Actions Column (Left, Horizontal) -->
                        <TableCell
                          class="px-4 py-2 align-middle"
                          onclick={(e) => e.stopPropagation()}
                        >
                          <div class="flex items-center gap-1">
                            <Button
                              variant="ghost"
                              size="sm"
                              class="h-7 w-7 p-0 text-muted-foreground hover:text-primary disabled:opacity-30"
                              title="Watch Stream"
                              disabled={!stream.metrics?.isLive}
                              onclick={(e) => {
                                e.stopPropagation();
                                watchStream(stream.playbackId);
                              }}
                            >
                              <PlayIcon class="w-3.5 h-3.5" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              class="h-7 w-7 p-0 text-muted-foreground hover:text-foreground"
                              title={expandedStreamId === stream.id ? "Hide Share Info" : "Share Stream"}
                              onclick={(e) => {
                                e.stopPropagation();
                                expandedStreamId = expandedStreamId === stream.id ? null : stream.id;
                              }}
                            >
                              {#if expandedStreamId === stream.id}
                                <ChevronUpIcon class="w-3.5 h-3.5" />
                              {:else}
                                <Share2Icon class="w-3.5 h-3.5" />
                              {/if}
                            </Button>
                            <Button
                              variant="ghost"
                              size="sm"
                              class="h-7 w-7 p-0 text-muted-foreground hover:text-destructive opacity-0 group-hover:opacity-100 transition-opacity focus:opacity-100"
                              title="Delete Stream"
                              onclick={(e) => {
                                e.stopPropagation();
                                confirmDeleteStream(stream);
                              }}
                            >
                              <Trash2Icon class="w-3.5 h-3.5" />
                            </Button>
                          </div>
                        </TableCell>
                        
                        <TableCell class="px-4 py-2">
                          <div class="flex flex-col">
                            <div
                              class="text-sm font-medium text-foreground truncate max-w-xs group-hover:text-primary transition-colors"
                              title={stream.name}
                            >
                              {stream.name}
                            </div>
                            <div class="text-[10px] text-muted-foreground font-mono">
                              {stream.id.slice(0, 8)}...
                            </div>
                          </div>
                        </TableCell>
                        
                        <TableCell class="px-4 py-2">
                          {@const status = stream.metrics?.status || "OFFLINE"}
                          <span
                            class="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border {getStatusColor(status)}"
                          >
                            {#if stream.metrics?.isLive}
                              <span class="w-1.5 h-1.5 rounded-full bg-success animate-pulse mr-1.5"></span>
                            {/if}
                            {status}
                          </span>
                        </TableCell>

                        <TableCell class="px-4 py-2">
                           {#if stream.metrics?.isLive}
                            <div class="flex flex-col gap-0.5">
                              <div class="flex items-center gap-1.5">
                                <BufferStateIndicator
                                  bufferState={stream.metrics?.bufferState || undefined}
                                  compact
                                />
                                <span class="text-xs font-medium capitalize text-foreground">
                                  {(stream.metrics?.bufferState || "unknown").toLowerCase()}
                                </span>
                              </div>
                              {#if stream.metrics?.qualityTier}
                                <span class="text-[10px] px-1 py-0 bg-accent/10 text-accent border border-accent/20 rounded-sm w-fit">
                                  {stream.metrics.qualityTier}
                                </span>
                              {/if}
                            </div>
                           {:else}
                             <span class="text-xs text-muted-foreground">-</span>
                           {/if}
                        </TableCell>

                        <TableCell class="px-4 py-2 text-sm text-foreground">
                          {stream.metrics?.currentViewers || 0}
                        </TableCell>
                        
                        <TableCell class="px-4 py-2 text-sm text-foreground">
                           {#if stream.metrics?.isLive && stream.metrics?.duration}
                              {formatDuration(stream.metrics.duration * 1000)}
                           {:else}
                              <span class="text-muted-foreground">-</span>
                           {/if}
                        </TableCell>
                      </TableRow>

                      <!-- Expanded Share Row -->
                      {#if expandedStreamId === stream.id}
                        <TableRow class="bg-muted/5 hover:bg-muted/5 border-t-0">
                          <TableCell colspan={6} class="px-4 py-4 cursor-default" onclick={(e) => e.stopPropagation()}>
                            <div class="pl-4 border-l-2 border-primary/20">
                              <PlaybackProtocols
                                contentId={stream.id}
                                contentType="live"
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

              <!-- Pagination -->
              {#if hasMoreStreams}
                <div class="flex justify-center py-4 border-t border-border/30">
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
      </div>
    </div>
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