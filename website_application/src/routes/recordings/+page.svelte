<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import {
    GetDVRRequestsStore,
    GetStreamsStore,
    DvrLifecycleStore
  } from "$houdini";
  import type { DvrLifecycle$result } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import {
    formatBytes,
    formatDuration,
    formatDate,
  } from "$lib/utils/formatters.js";
  import { Input } from "$lib/components/ui/input";
  import { Button } from "$lib/components/ui/button";
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
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import { getIconComponent } from "$lib/iconUtils";
  import { getContentDeliveryUrls } from "$lib/config";
  import PlaybackProtocols from "$lib/components/PlaybackProtocols.svelte";

  // Houdini stores
  const dvrRequestsStore = new GetDVRRequestsStore();
  const streamsStore = new GetStreamsStore();
  const dvrLifecycleSub = new DvrLifecycleStore();

  // Types from Houdini
  type DvrRequestData = NonNullable<NonNullable<NonNullable<typeof $dvrRequestsStore.data>["dvrRecordingsConnection"]>["edges"]>[0]["node"];
  type DvrLifecycleEvent = NonNullable<DvrLifecycle$result["dvrLifecycle"]>;
  type StreamData = NonNullable<NonNullable<typeof $streamsStore.data>["streams"]>[0];

  // Derived state from Houdini stores
  let loading = $derived($dvrRequestsStore.fetching || $streamsStore.fetching);
  let recordingsEdges = $derived($dvrRequestsStore.data?.dvrRecordingsConnection?.edges ?? []);
  let recordings = $derived(recordingsEdges.map(e => e.node));
  let streams = $derived($streamsStore.data?.streams ?? []);

  let error = $state<string | null>(null);
  let statusFilter = $state("all");

  // Track active subscription stream
  let activeSubscriptionStream = $state<string | null>(null);

  // Track DVR progress for real-time updates
  let dvrProgress = $state<Record<string, { stage: string; percent: number; message?: string }>>({});
  const statusFilterLabels: Record<string, string> = {
    all: "All Statuses",
    recording: "Recording",
    completed: "Completed",
    processing: "Processing",
    failed: "Failed",
    paused: "Paused",
  };
  let searchQuery = $state("");

  // Pagination (client-side filtering)
  let currentPage = $state(1);
  let pageSize = 20;

  let filteredRecordings = $derived(
    recordings.filter((recording) => {
      const matchesStatus =
        statusFilter === "all" || recording.status === statusFilter;
      const matchesSearch =
        !searchQuery ||
        recording.internalName
          ?.toLowerCase()
          .includes(searchQuery.toLowerCase()) ||
        recording.dvrHash?.toLowerCase().includes(searchQuery.toLowerCase()) ||
        recording.manifestPath
          ?.toLowerCase()
          .includes(searchQuery.toLowerCase());

      return matchesStatus && matchesSearch;
    }),
  );

  let paginatedRecordings = $derived(
    filteredRecordings.slice(
      (currentPage - 1) * pageSize,
      currentPage * pageSize,
    ),
  );

  let totalPages = $derived(Math.ceil(filteredRecordings.length / pageSize));
  let totalRecordings = $derived(filteredRecordings.length);

  // Server-side pagination state
  let hasMoreOnServer = $derived(
    $dvrRequestsStore.data?.dvrRecordingsConnection?.pageInfo?.hasNextPage ?? false
  );
  let loadingMore = $state(false);

  // Derived stats
  let completedCount = $derived(filteredRecordings.filter((r) => r.status === "completed").length);
  let recordingCount = $derived(filteredRecordings.filter((r) => r.status === "recording").length);
  let failedCount = $derived(filteredRecordings.filter((r) => r.status === "failed").length);

  async function loadRecordings() {
    try {
      error = null;

      // Load streams and DVR requests in parallel
      await Promise.all([
        dvrRequestsStore.fetch({ variables: { first: 100 } }),
        streamsStore.fetch(),
      ]);

      if ($dvrRequestsStore.errors?.length || $streamsStore.errors?.length) {
        // Filter out AbortErrors from Houdini errors if they are exposed there
        // usage of 'any' to bypass strict type checking on error objects for now
        const dvrErrors = $dvrRequestsStore.errors?.filter((e: any) => e.message !== 'Aborted') ?? [];
        const streamErrors = $streamsStore.errors?.filter((e: any) => e.message !== 'Aborted') ?? [];
        
        if (dvrErrors.length || streamErrors.length) {
             console.error("Failed to load data:", dvrErrors, streamErrors);
             error = "Failed to load recordings";
        }
      }

      // Start DVR subscription for the first stream (if any)
      // Note: In a real scenario, you might want to subscribe to all streams
      // but for simplicity, we'll just subscribe to the first one
      // Use stream.id (internal UUID) for subscriptions - this is the canonical identifier
      if (streams.length > 0 && !activeSubscriptionStream) {
        startDvrSubscription(streams[0].id);
      }
    } catch (err: any) {
      // Ignore AbortErrors which happen on navigation/cancellation
      if (err.name === 'AbortError' || err.message === 'aborted' || err.message === 'Aborted') {
        return;
      }
      console.error("Failed to load recordings:", err);
      error = "Failed to load recordings";
    }
  }

  function startDvrSubscription(streamId: string) {
    if (activeSubscriptionStream !== streamId) {
      dvrLifecycleSub.unlisten();
      activeSubscriptionStream = streamId;
      // Use stream.id (internal UUID) for subscriptions - this is the canonical identifier
      dvrLifecycleSub.listen({ stream: streamId });
    }
  }

  // Effect to handle DVR lifecycle subscription updates
  $effect(() => {
    const event = $dvrLifecycleSub.data?.dvrLifecycle;
    if (event) {
      handleDvrEvent(event);
    }
  });

  function handleDvrEvent(event: DvrLifecycleEvent) {
    // Update DVR progress tracking
    const key = event.dvrHash;
    if (key) {
      dvrProgress[key] = {
        stage: event.status,
        percent: 0, // DVR doesn't have percentage
        message: event.error ?? undefined,
      };
      dvrProgress = { ...dvrProgress };
    }

    // Handle different statuses
    const status = event.status.toLowerCase();

    // Check against likely proto enum values or simplified strings
    if (status === "completed" || status === "status_stopped" || status === "stopped") {
      toast.success(`Recording "${event.internalName}" completed!`);
      // Refresh recordings list
      loadRecordings();
    } else if (status === "started" || status === "status_started") {
      toast.success(`Recording started for "${event.internalName}"`);
      loadRecordings();
    } else if (status === "status_failed" || status === "failed" || status === "error") {
      toast.error(`Recording failed: ${event.error || "Unknown error"}`);
    }
  }

  function getStatusColor(status: string | null | undefined): string {
    switch (status?.toLowerCase()) {
      case "completed":
        return "text-success bg-success/10 border-success/20";
      case "recording":
        return "text-warning bg-warning/10 border-warning/20";
      case "processing":
        return "text-primary bg-primary/10 border-primary/20";
      case "failed":
        return "text-destructive bg-destructive/10 border-destructive/20";
      case "paused":
        return "text-muted-foreground bg-muted border-border";
      default:
        return "text-muted-foreground bg-muted border-border";
    }
  }

  function nextPage() {
    if (currentPage < totalPages) currentPage++;
  }

  function prevPage() {
    if (currentPage > 1) currentPage--;
  }

  function goToPage(page: number): void {
    if (page >= 1 && page <= totalPages) {
      currentPage = page;
    }
  }

  async function loadMoreRecordings() {
    if (!hasMoreOnServer || loadingMore) return;

    loadingMore = true;
    try {
      await dvrRequestsStore.loadNextPage();
    } catch (err) {
      console.error("Failed to load more recordings:", err);
    } finally {
      loadingMore = false;
    }
  }

  onMount(() => {
    loadRecordings();
  });

  onDestroy(() => {
    // Clean up DVR subscription
    dvrLifecycleSub.unlisten();
  });

  // Icons
  const FilmIcon = getIconComponent("Film");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const CircleDotIcon = getIconComponent("CircleDot");
  const XCircleIcon = getIconComponent("XCircle");
  const SearchIcon = getIconComponent("Search");
  const FilterIcon = getIconComponent("Filter");
  const DownloadIcon = getIconComponent("Download");
  const PlayIcon = getIconComponent("Play");
  const Share2Icon = getIconComponent("Share2");
  const Trash2Icon = getIconComponent("Trash2");
  const ChevronUpIcon = getIconComponent("ChevronUp");

  // Expanded row tracking
  let expandedRecording = $state<string | null>(null);

  function playRecording(dvrHash: string) {
    goto(`/view?type=dvr&id=${dvrHash}`);
  }
</script>

<svelte:head>
  <title>Recordings - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col overflow-hidden">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 z-10 bg-background">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <FilmIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Recordings</h1>
          <p class="text-sm text-muted-foreground">
            Manage and monitor all stream recordings
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
            icon={FilmIcon}
            iconColor="text-primary"
            value={totalRecordings}
            valueColor="text-primary"
            label="Total Results"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={CheckCircleIcon}
            iconColor="text-success"
            value={completedCount}
            valueColor="text-success"
            label="Completed"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={CircleDotIcon}
            iconColor="text-warning"
            value={recordingCount}
            valueColor="text-warning"
            label="Recording"
          />
        </div>
        <div>
          <DashboardMetricCard
            icon={XCircleIcon}
            iconColor="text-destructive"
            value={failedCount}
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
                  Search Recordings
                </label>
                <div class="relative">
                  <SearchIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                  <Input
                    id="search"
                    type="text"
                    bind:value={searchQuery}
                    placeholder="Search by stream name, hash, or path..."
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
                    {statusFilterLabels[statusFilter] ?? "All Statuses"}
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="all">All Statuses</SelectItem>
                    <SelectItem value="recording">Recording</SelectItem>
                    <SelectItem value="completed">Completed</SelectItem>
                    <SelectItem value="processing">Processing</SelectItem>
                    <SelectItem value="failed">Failed</SelectItem>
                    <SelectItem value="paused">Paused</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
          </div>
        </div>

        <!-- Recordings Table Slab -->
        <div class="slab col-span-full">
          <div class="slab-header">
            <div class="flex items-center gap-2">
              <FilmIcon class="w-4 h-4 text-info" />
              <h3>All Recordings</h3>
            </div>
          </div>
          <div class="slab-body--flush">
            {#if error}
              <div class="p-6">
                <div
                  class="bg-destructive/10 border border-destructive/30 p-6 text-center"
                >
                  <div class="text-destructive mb-2">Error</div>
                  <div class="text-foreground">{error}</div>
                  <Button onclick={loadRecordings} class="mt-4">Retry</Button>
                </div>
              </div>
            {:else if paginatedRecordings.length === 0}
              <div class="flex flex-col items-center justify-center py-16 m-4 border-2 border-dashed border-border/50 rounded-lg bg-muted/5">
                <div class="w-16 h-16 rounded-full bg-muted/30 flex items-center justify-center mb-6">
                  <FilmIcon class="w-8 h-8 text-muted-foreground" />
                </div>
                <h3 class="text-xl font-semibold mb-3">No recordings found</h3>
                <p class="text-muted-foreground mb-8 max-w-sm text-lg text-center">
                  {#if searchQuery || statusFilter !== "all"}
                    Try adjusting your search query or changing the status filters.
                  {:else}
                    Recordings of your live streams will appear here.
                  {/if}
                </p>
                {#if searchQuery || statusFilter !== "all"}
                  <Button
                    variant="outline"
                    size="lg"
                    onclick={() => {
                      searchQuery = "";
                      statusFilter = "all";
                    }}
                  >
                    Clear Filters
                  </Button>
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
                      <TableHead
                        class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >
                        Recording
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
                        Duration
                      </TableHead>
                      <TableHead
                        class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >
                        Size
                      </TableHead>
                      <TableHead
                        class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider"
                      >
                        Created
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody class="divide-y divide-border">
                    {#each paginatedRecordings as recording (recording.dvrHash)}
                      <TableRow
                        class="hover:bg-muted/50 transition-colors cursor-pointer group"
                        onclick={() => recording.status === "completed" && playRecording(recording.dvrHash)}
                      >
                        <!-- Actions Column (Left, Horizontal) -->
                        <TableCell
                          class="px-4 py-2 align-middle"
                          onclick={(e) => e.stopPropagation()}
                        >
                          <div class="flex items-center gap-1">
                            {#if recording.status === "completed" && recording.dvrHash}
                              {@const urls = getContentDeliveryUrls(recording.dvrHash, "dvr")}
                              
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
                                title={expandedRecording === recording.dvrHash ? "Hide Share Info" : "Share Recording"}
                                onclick={() => expandedRecording = expandedRecording === recording.dvrHash ? null : recording.dvrHash}
                              >
                                {#if expandedRecording === recording.dvrHash}
                                  <ChevronUpIcon class="w-3.5 h-3.5" />
                                {:else}
                                  <Share2Icon class="w-3.5 h-3.5" />
                                {/if}
                              </Button>

                              <Button
                                variant="ghost"
                                size="sm"
                                class="h-7 w-7 p-0 text-muted-foreground hover:text-destructive opacity-0 group-hover:opacity-100 transition-opacity focus:opacity-100"
                                title="Delete Recording (Not implemented)"
                                disabled
                              >
                                <Trash2Icon class="w-3.5 h-3.5" />
                              </Button>
                            {:else if recording.status === "recording"}
                              <span class="text-[10px] text-warning animate-pulse px-2">Recording...</span>
                            {:else}
                              <span class="text-[10px] text-muted-foreground px-2">-</span>
                            {/if}
                          </div>
                        </TableCell>

                        <TableCell class="px-4 py-2">
                          <div class="flex flex-col">
                            <div
                              class="text-sm font-medium text-foreground truncate max-w-xs group-hover:text-primary transition-colors"
                              title={recording.manifestPath}
                            >
                              {recording.manifestPath || recording.dvrHash}
                            </div>
                            <div class="text-[10px] text-muted-foreground font-mono">
                              {recording.dvrHash.slice(0, 8)}...
                            </div>
                          </div>
                        </TableCell>

                        <TableCell class="px-4 py-2">
                          <div class="flex flex-col">
                            <div class="text-sm text-foreground">
                              {recording.internalName || "Unknown"}
                            </div>
                            <div class="text-[10px] text-muted-foreground">
                              {recording.storageNodeId || "N/A"}
                            </div>
                          </div>
                        </TableCell>

                        <TableCell class="px-4 py-2">
                          <span
                            class="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border {getStatusColor(recording.status)}"
                          >
                            {recording.status || "Unknown"}
                          </span>
                        </TableCell>

                        <TableCell class="px-4 py-2 text-sm text-foreground">
                          {recording.durationSeconds
                            ? formatDuration(recording.durationSeconds * 1000)
                            : "N/A"}
                        </TableCell>
                        <TableCell class="px-4 py-2 text-sm text-foreground">
                          {recording.sizeBytes
                            ? formatBytes(recording.sizeBytes)
                            : "N/A"}
                        </TableCell>
                        <TableCell class="px-4 py-2 text-sm text-foreground">
                          {recording.createdAt
                            ? formatDate(recording.createdAt)
                            : "N/A"}
                        </TableCell>
                      </TableRow>

                      <!-- Expanded protocols row -->
                      {#if expandedRecording === recording.dvrHash && recording.status === "completed"}
                        <TableRow class="bg-muted/5 border-t-0">
                          <TableCell colspan={7} class="px-4 py-4 cursor-default">
                             <div class="pl-4 border-l-2 border-primary/20" onclick={(e) => e.stopPropagation()}>
                              <PlaybackProtocols
                                contentId={recording.dvrHash}
                                contentType="dvr"
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
              {#if totalPages > 1}
                <div
                  class="bg-muted/30 px-6 py-3 flex items-center justify-between border-t border-border/30"
                >
                  <div class="flex-1 flex justify-between sm:hidden">
                    <button
                      onclick={prevPage}
                      disabled={currentPage === 1}
                      class="relative inline-flex items-center px-4 py-2 border border-border text-sm font-medium rounded-md text-foreground bg-card hover:bg-background disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      Previous
                    </button>
                    <button
                      onclick={nextPage}
                      disabled={currentPage === totalPages}
                      class="ml-3 relative inline-flex items-center px-4 py-2 border border-border text-sm font-medium rounded-md text-foreground bg-card hover:bg-background disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      Next
                    </button>
                  </div>
                  <div
                    class="hidden sm:flex-1 sm:flex sm:items-center sm:justify-between"
                  >
                    <div>
                      <p class="text-sm text-muted-foreground">
                        Showing
                        <span class="font-medium"
                          >{(currentPage - 1) * pageSize + 1}</span
                        >
                        to
                        <span class="font-medium"
                          >{Math.min(currentPage * pageSize, totalRecordings)}</span
                        >
                        of
                        <span class="font-medium">{totalRecordings}</span>
                        results
                      </p>
                    </div>
                    <div>
                      <nav
                        class="relative z-0 inline-flex rounded-md shadow-sm -space-x-px"
                      >
                        <button
                          onclick={prevPage}
                          disabled={currentPage === 1}
                          class="relative inline-flex items-center px-2 py-2 border border-border bg-card text-sm font-medium text-foreground hover:bg-background disabled:opacity-50 disabled:cursor-not-allowed"
                        >
                          ←
                        </button>
                        {#each Array.from( { length: Math.min(7, totalPages) }, (_, i) => {
                            if (totalPages <= 7) return i + 1;
                            if (currentPage <= 4) return i + 1;
                            if (currentPage >= totalPages - 3) return totalPages - 6 + i;
                            return currentPage - 3 + i;
                          }, ) as page (page)}
                          <button
                            onclick={() => goToPage(page)}
                            class="relative inline-flex items-center px-4 py-2 border border-border text-sm font-medium {currentPage ===
                            page
                              ? 'bg-primary text-background border-primary'
                              : 'bg-card text-foreground hover:bg-background'}"
                          >
                            {page}
                          </button>
                        {/each}
                        <button
                          onclick={nextPage}
                          disabled={currentPage === totalPages}
                          class="relative inline-flex items-center px-2 py-2 border border-border bg-card text-sm font-medium text-foreground hover:bg-background disabled:opacity-50 disabled:cursor-not-allowed"
                        >
                          →
                        </button>
                      </nav>
                    </div>
                  </div>
                </div>
              {/if}

              <!-- Server-side Load More -->
              {#if hasMoreOnServer}
                <div class="flex justify-center py-4 border-t border-border/30">
                  <Button
                    variant="outline"
                    onclick={loadMoreRecordings}
                    disabled={loadingMore}
                  >
                    {#if loadingMore}
                      Loading...
                    {:else}
                      Load More Recordings from Server
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