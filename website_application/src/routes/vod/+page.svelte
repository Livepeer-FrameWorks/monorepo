<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { goto } from "$app/navigation";
  import { auth } from "$lib/stores/auth";
  import {
    GetVodAssetsConnectionStore,
    CreateVodUploadStore,
    CompleteVodUploadStore,
    AbortVodUploadStore,
    DeleteVodAssetStore,
    VodLifecycleStore,
  } from "$houdini";
  import { toast } from "$lib/stores/toast.js";
  import { Button } from "$lib/components/ui/button";
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
  import { Progress } from "$lib/components/ui/progress";
  import {
    Table,
    TableHeader,
    TableHead,
    TableRow,
    TableBody,
    TableCell,
  } from "$lib/components/ui/table";
  import { getIconComponent } from "$lib/iconUtils";
  import { formatBytes, formatExpiry } from "$lib/utils/formatters.js";
  import EmptyState from "$lib/components/EmptyState.svelte";

  // Houdini stores
  const vodAssetsStore = new GetVodAssetsConnectionStore();
  const createVodUploadMutation = new CreateVodUploadStore();
  const completeVodUploadMutation = new CompleteVodUploadStore();
  const abortVodUploadMutation = new AbortVodUploadStore();
  const deleteVodAssetMutation = new DeleteVodAssetStore();
  const vodLifecycleStore = new VodLifecycleStore();

  // Types
  type VodAssetData = NonNullable<NonNullable<NonNullable<typeof $vodAssetsStore.data>["vodAssetsConnection"]>["edges"]>[0]["node"];

  let isAuthenticated = false;

  // Derived state from Houdini stores
  let loading = $derived($vodAssetsStore.fetching);
  let vodAssets = $derived($vodAssetsStore.data?.vodAssetsConnection?.edges?.map(e => e.node) ?? []);
  let hasMoreAssets = $derived($vodAssetsStore.data?.vodAssetsConnection?.pageInfo?.hasNextPage ?? false);
  let totalAssetsCount = $derived($vodAssetsStore.data?.vodAssetsConnection?.totalCount ?? 0);

  // Pagination state
  let loadingMoreAssets = $state(false);

  // Upload state
  let showUploadModal = $state(false);
  let uploadFile = $state<File | null>(null);
  let uploadTitle = $state("");
  let uploadDescription = $state("");
  let uploading = $state(false);
  let uploadProgress = $state(0);
  let uploadStage = $state<"idle" | "initializing" | "uploading" | "completing" | "done">("idle");
  let currentUploadId = $state<string | null>(null);

  // Delete state
  let showDeleteModal = $state(false);
  let assetToDelete = $state<VodAssetData | null>(null);
  let deleting = $state(false);

  // Derived stats
  let uploadingAssets = $derived(vodAssets.filter(a => a.status === "UPLOADING").length);
  let processingAssets = $derived(vodAssets.filter(a => a.status === "PROCESSING").length);
  let readyAssets = $derived(vodAssets.filter(a => a.status === "READY").length);
  let failedAssets = $derived(vodAssets.filter(a => a.status === "FAILED").length);

  // Search filter
  let searchQuery = $state("");
  let statusFilter = $state("all");

  let filteredAssets = $derived.by(() => {
    let result = vodAssets;

    // Filter by search query
    if (searchQuery.trim()) {
      const query = searchQuery.toLowerCase();
      result = result.filter(
        (asset) =>
          asset.title?.toLowerCase().includes(query) ||
          asset.artifactHash?.toLowerCase().includes(query) ||
          asset.filename?.toLowerCase().includes(query) ||
          asset.description?.toLowerCase().includes(query)
      );
    }

    // Filter by status
    if (statusFilter !== "all") {
      result = result.filter((asset) => {
        const s = asset.status?.toLowerCase() || "";
        if (statusFilter === "uploading") return s === "uploading";
        if (statusFilter === "processing") return s === "processing";
        if (statusFilter === "ready") return s === "ready";
        if (statusFilter === "failed") return s === "failed";
        return true;
      });
    }

    return result;
  });

  // Subscription cleanup
  let vodLifecycleUnsubscribe: (() => void) | null = null;

  // Subscribe to auth store
  auth.subscribe((authState) => {
    isAuthenticated = authState.isAuthenticated;
  });

  onMount(async () => {
    if (!isAuthenticated) {
      await auth.checkAuth();
    }
    await loadData();

    // Start VOD lifecycle subscription for real-time updates
    vodLifecycleStore.listen();

    // Subscribe to lifecycle events
    vodLifecycleUnsubscribe = vodLifecycleStore.subscribe((result) => {
      if (result.data?.vodLifecycle) {
        handleVodLifecycleEvent(result.data.vodLifecycle);
      }
    });
  });

  onDestroy(() => {
    // Cleanup subscription
    if (vodLifecycleUnsubscribe) {
      vodLifecycleUnsubscribe();
    }
  });

  // Handle real-time VOD lifecycle events
  function handleVodLifecycleEvent(event: {
    status: number;
    vodHash: string;
    filename?: string | null;
    error?: string | null;
  }) {
    // Map status int to string (matches VodLifecycleData.Status enum)
    const statusMap: Record<number, string> = {
      1: "UPLOADING",    // STATUS_REQUESTED
      2: "UPLOADING",    // STATUS_UPLOADING
      3: "PROCESSING",   // STATUS_PROCESSING
      4: "READY",        // STATUS_COMPLETED
      5: "FAILED",       // STATUS_FAILED
      6: "DELETED",      // STATUS_DELETED
    };
    const newStatus = statusMap[event.status] || "PROCESSING";

    // Find and update the matching asset in our list
    const currentEdges = $vodAssetsStore.data?.vodAssetsConnection?.edges;
    if (currentEdges) {
      const assetIndex = currentEdges.findIndex(
        (e) => e.node.artifactHash === event.vodHash
      );

      if (assetIndex !== -1) {
        // Asset exists - status changed
        const oldStatus = currentEdges[assetIndex].node.status;
        if (oldStatus !== newStatus) {
          // Refresh to get updated data
          loadData();

          // Show toast for significant status changes
          if (newStatus === "READY") {
            toast.success(`Video "${event.filename || event.vodHash.slice(0, 8)}" is ready`);
          } else if (newStatus === "FAILED") {
            toast.error(`Video "${event.filename || event.vodHash.slice(0, 8)}" failed: ${event.error || "Unknown error"}`);
          }
        }
      } else if (newStatus !== "DELETED") {
        // New asset appeared (maybe uploaded from another session)
        loadData();
      }
    }
  }

  async function loadData() {
    try {
      await vodAssetsStore.fetch({ variables: { first: 50 } });
      if ($vodAssetsStore.errors?.length) {
        console.error("Failed to load VOD assets:", $vodAssetsStore.errors);
        toast.error("Failed to load VOD assets. Please refresh the page.");
      }
    } catch (error) {
      console.error("Failed to load data:", error);
      toast.error("Failed to load VOD assets. Please refresh the page.");
    }
  }

  async function loadMoreAssets() {
    if (!hasMoreAssets || loadingMoreAssets) return;

    try {
      loadingMoreAssets = true;
      const endCursor = $vodAssetsStore.data?.vodAssetsConnection?.pageInfo?.endCursor;
      await vodAssetsStore.fetch({
        variables: {
          first: 50,
          after: endCursor ?? undefined,
        },
      });
    } catch (error) {
      console.error("Failed to load more assets:", error);
      toast.error("Failed to load more assets.");
    } finally {
      loadingMoreAssets = false;
    }
  }

  // File input handling
  function handleFileSelect(event: Event) {
    const target = event.target as HTMLInputElement;
    const file = target.files?.[0];
    if (file) {
      uploadFile = file;
      // Use filename as default title if not set
      if (!uploadTitle) {
        uploadTitle = file.name.replace(/\.[^/.]+$/, ""); // Remove extension
      }
    }
  }

  // Upload flow
  async function startUpload() {
    if (!uploadFile) {
      toast.warning("Please select a file to upload");
      return;
    }

    try {
      uploading = true;
      uploadStage = "initializing";
      uploadProgress = 0;

      // Step 1: Initialize multipart upload
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

      // Step 2: Upload each part directly to S3
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
          headers: {
            "Content-Type": uploadFile.type || "application/octet-stream",
          },
        });

        if (!response.ok) {
          throw new Error(`Failed to upload part ${part.partNumber}: ${response.statusText}`);
        }

        // Get ETag from response headers
        const etag = response.headers.get("ETag")?.replace(/"/g, "") || "";
        completedParts.push({
          partNumber: part.partNumber,
          etag: etag,
        });

        // Update progress
        uploadProgress = Math.round(((i + 1) / totalParts) * 100);
      }

      uploadStage = "completing";

      // Step 3: Complete the multipart upload
      const completeResult = await completeVodUploadMutation.mutate({
        input: {
          uploadId: currentUploadId,
          parts: completedParts,
        },
      });

      const completeData = completeResult.data?.completeVodUpload;
      if (completeData?.__typename !== "VodAsset") {
        const error = completeData as unknown as { message?: string };
        throw new Error(error.message || "Failed to complete upload");
      }

      uploadStage = "done";
      toast.success("Video uploaded successfully!");

      // Refresh the list
      await loadData();

      // Reset form
      resetUploadForm();
      showUploadModal = false;

    } catch (error) {
      console.error("Upload failed:", error);
      toast.error(`Upload failed: ${error instanceof Error ? error.message : "Unknown error"}`);

      // Try to abort the upload if it was started
      if (currentUploadId) {
        try {
          await abortVodUploadMutation.mutate({ uploadId: currentUploadId });
        } catch (abortError) {
          console.error("Failed to abort upload:", abortError);
        }
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

  // Delete handling
  function confirmDelete(asset: VodAssetData) {
    assetToDelete = asset;
    showDeleteModal = true;
  }

  async function deleteAsset() {
    if (!assetToDelete?.id) return;

    try {
      deleting = true;
      const result = await deleteVodAssetMutation.mutate({ id: assetToDelete.id });

      const deleteResult = result.data?.deleteVodAsset;
      if (deleteResult?.__typename === "DeleteSuccess") {
        toast.success("Video deleted successfully!");
        await loadData();
      } else if (deleteResult) {
        const error = deleteResult as unknown as { message?: string };
        toast.error(error.message || "Failed to delete video");
      }
    } catch (error) {
      console.error("Failed to delete asset:", error);
      toast.error("Failed to delete video. Please try again.");
    } finally {
      deleting = false;
      showDeleteModal = false;
      assetToDelete = null;
    }
  }

  function formatDuration(ms: number | null | undefined): string {
    if (!ms) return "N/A";
    const seconds = Math.floor(ms / 1000);
    const minutes = Math.floor(seconds / 60);
    const hours = Math.floor(minutes / 60);
    if (hours > 0) {
      return `${hours}:${(minutes % 60).toString().padStart(2, "0")}:${(seconds % 60).toString().padStart(2, "0")}`;
    }
    return `${minutes}:${(seconds % 60).toString().padStart(2, "0")}`;
  }

  function formatDate(dateString: string | Date | undefined): string {
    if (!dateString) return "N/A";
    return new Date(dateString).toLocaleDateString();
  }

  function getStatusColor(status: string | null | undefined): string {
    switch (status?.toUpperCase()) {
      case "READY":
        return "text-success bg-success/10 border-success/20";
      case "UPLOADING":
      case "PROCESSING":
        return "text-warning bg-warning/10 border-warning/20";
      case "FAILED":
        return "text-destructive bg-destructive/10 border-destructive/20";
      case "DELETED":
        return "text-muted-foreground bg-muted border-border opacity-70";
      default:
        return "text-muted-foreground bg-muted border-border";
    }
  }

  function playAsset(artifactHash: string) {
    goto(`/view?type=vod&id=${artifactHash}`);
  }

  // Icons
  const UploadIcon = getIconComponent("Upload");
  const VideoIcon = getIconComponent("Video");
  const CheckCircleIcon = getIconComponent("CheckCircle");
  const LoaderIcon = getIconComponent("Loader");
  const XCircleIcon = getIconComponent("XCircle");
  const PlayIcon = getIconComponent("Play");
  const Trash2Icon = getIconComponent("Trash2");
  const FilterIcon = getIconComponent("Filter");
  const SearchIcon = getIconComponent("Search");
  const FileVideoIcon = getIconComponent("FileVideo");
  const CloudUploadIcon = getIconComponent("CloudUpload");
</script>

<svelte:head>
  <title>VOD Library - FrameWorks</title>
</svelte:head>

<div class="h-full flex flex-col overflow-hidden">
  <!-- Fixed Page Header -->
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0 z-10 bg-background">
    <div class="flex justify-between items-center">
      <div class="flex items-center gap-3">
        <VideoIcon class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">VOD Library</h1>
          <p class="text-sm text-muted-foreground">
            Upload and manage video-on-demand assets
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
              icon={VideoIcon}
              iconColor="text-primary"
              value={totalAssetsCount}
              valueColor="text-primary"
              label="Total Assets"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={LoaderIcon}
              iconColor="text-warning"
              value={uploadingAssets + processingAssets}
              valueColor="text-warning"
              label="Processing"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={CheckCircleIcon}
              iconColor="text-success"
              value={readyAssets}
              valueColor="text-success"
              label="Ready"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={XCircleIcon}
              iconColor="text-destructive"
              value={failedAssets}
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
                    Search Assets
                  </label>
                  <div class="relative">
                    <SearchIcon class="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                    <Input
                      id="search"
                      type="text"
                      bind:value={searchQuery}
                      placeholder="Search by title, filename, or hash..."
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
                  <select
                    id="status-filter"
                    bind:value={statusFilter}
                    class="w-full h-10 px-3 rounded-md border border-input bg-background text-sm"
                  >
                    <option value="all">All Statuses</option>
                    <option value="uploading">Uploading</option>
                    <option value="processing">Processing</option>
                    <option value="ready">Ready</option>
                    <option value="failed">Failed</option>
                  </select>
                </div>
              </div>
            </div>
          </div>

          <!-- Assets Table Slab -->
          <div class="slab col-span-full">
            <div class="slab-header flex justify-between items-center">
              <div class="flex items-center gap-2">
                <FileVideoIcon class="w-4 h-4 text-info" />
                <h3>Your Videos</h3>
              </div>
              <Button
                variant="outline"
                size="sm"
                class="gap-2 h-8"
                onclick={() => (showUploadModal = true)}
              >
                <UploadIcon class="w-3.5 h-3.5" />
                Upload Video
              </Button>
            </div>
            <div class="slab-body--flush">
              {#if filteredAssets.length === 0}
                <div class="p-8">
                  {#if searchQuery}
                    <EmptyState
                      iconName="FileVideo"
                      title="No videos found"
                      description="Try adjusting your search query."
                      actionText="Clear Search"
                      onAction={() => (searchQuery = "")}
                    />
                  {:else}
                    <EmptyState
                      iconName="CloudUpload"
                      title="No videos found"
                      description="Upload your first video to get started."
                      actionText="Upload Video"
                      onAction={() => (showUploadModal = true)}
                    />
                  {/if}
                </div>
              {:else}
                <div class="overflow-x-auto">
                  <Table class="w-full">
                    <TableHeader>
                      <TableRow>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider w-[120px]">
                          Actions
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Video
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Status
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Duration
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Resolution
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Size
                        </TableHead>
                        <TableHead class="px-4 py-2 text-left text-xs font-medium text-muted-foreground uppercase tracking-wider">
                          Uploaded
                        </TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody class="divide-y divide-border">
                      {#each filteredAssets as asset (asset.id)}
                        {@const isReady = asset.status === "READY"}
                        {@const isDeleted = asset.status === "DELETED"}
                        <TableRow
                          class="transition-colors group {isDeleted ? 'opacity-60 bg-muted/30 cursor-not-allowed' : isReady ? 'hover:bg-muted/50 cursor-pointer' : 'cursor-default'}"
                          onclick={() => isReady && asset.artifactHash && playAsset(asset.artifactHash)}
                        >
                          <!-- Actions Column -->
                          <TableCell
                            class="px-4 py-2 align-middle"
                            onclick={(e) => e.stopPropagation()}
                          >
                            <div class="flex items-center gap-1">
                              {#if isDeleted}
                                <span class="text-[10px] text-muted-foreground px-2 italic">Deleted</span>
                              {:else if isReady}
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  class="h-7 w-7 p-0 text-muted-foreground hover:text-primary"
                                  title="Play Video"
                                  onclick={() => asset.artifactHash && playAsset(asset.artifactHash)}
                                >
                                  <PlayIcon class="w-3.5 h-3.5" />
                                </Button>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  class="h-7 w-7 p-0 text-muted-foreground hover:text-destructive opacity-0 group-hover:opacity-100 transition-opacity focus:opacity-100"
                                  title="Delete Video"
                                  onclick={() => confirmDelete(asset)}
                                >
                                  <Trash2Icon class="w-3.5 h-3.5" />
                                </Button>
                              {:else if asset.status === "PROCESSING" || asset.status === "UPLOADING"}
                                <span class="text-[10px] text-warning animate-pulse px-2">Processing...</span>
                              {:else if asset.status === "FAILED"}
                                <span class="text-[10px] text-destructive px-2">{asset.errorMessage || "Failed"}</span>
                              {:else}
                                <span class="text-[10px] text-muted-foreground px-2">-</span>
                              {/if}
                            </div>
                          </TableCell>

                          <TableCell class="px-4 py-2">
                            <div class="flex flex-col">
                              <div class="text-sm font-medium text-foreground truncate max-w-xs group-hover:text-primary transition-colors" title={asset.title || asset.filename || ""}>
                                {asset.title || asset.filename || "Untitled"}
                              </div>
                              {#if asset.description}
                                <div class="text-[10px] text-muted-foreground truncate max-w-xs" title={asset.description}>
                                  {asset.description}
                                </div>
                              {/if}
                              <div class="text-[10px] text-muted-foreground font-mono">
                                {asset.artifactHash?.slice(0, 8) || "N/A"}...
                              </div>
                            </div>
                          </TableCell>
                          <TableCell class="px-4 py-2">
                            <span class="inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-medium border {getStatusColor(asset.status)}">
                              {asset.status || "Unknown"}
                            </span>
                          </TableCell>
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {formatDuration(asset.durationMs)}
                          </TableCell>
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {asset.resolution || "N/A"}
                          </TableCell>
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {asset.sizeBytes ? formatBytes(asset.sizeBytes) : "N/A"}
                          </TableCell>
                          <TableCell class="px-4 py-2 text-sm text-foreground">
                            {formatDate(asset.createdAt)}
                          </TableCell>
                        </TableRow>
                      {/each}
                    </TableBody>
                  </Table>
                </div>

                {#if hasMoreAssets}
                  <div class="flex justify-center py-4 border-t border-border/30">
                    <Button
                      variant="outline"
                      onclick={loadMoreAssets}
                      disabled={loadingMoreAssets}
                    >
                      {#if loadingMoreAssets}
                        Loading...
                      {:else}
                        Load More Videos
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

<!-- Upload Modal -->
<Dialog
  open={showUploadModal}
  onOpenChange={(value) => {
    if (!uploading) {
      showUploadModal = value;
      if (!value) resetUploadForm();
    }
  }}
>
  <DialogContent class="max-w-md rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden">
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-muted-foreground">Upload Video</DialogTitle>
      <DialogDescription class="text-xs text-muted-foreground/70">
        Upload a video file to your VOD library.
      </DialogDescription>
    </DialogHeader>

    <form
      id="upload-form"
      class="slab-body--padded space-y-4"
      onsubmit={(e) => { e.preventDefault(); startUpload(); }}
    >
      <!-- File Input -->
      <div class="space-y-2">
        <label
          for="file-input"
          class="block text-sm font-medium text-muted-foreground mb-2"
        >
          Video File
        </label>
        <div class="relative border-2 border-dashed border-border rounded-lg p-6 text-center hover:border-primary/50 transition-colors">
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

      <!-- Title -->
      <div class="space-y-2">
        <label
          for="upload-title"
          class="block text-sm font-medium text-muted-foreground mb-2"
        >
          Title
        </label>
        <Input
          id="upload-title"
          type="text"
          bind:value={uploadTitle}
          placeholder="Enter video title"
          disabled={uploading}
        />
      </div>

      <!-- Description -->
      <div class="space-y-2">
        <label
          for="upload-description"
          class="block text-sm font-medium text-muted-foreground mb-2"
        >
          Description (optional)
        </label>
        <Textarea
          id="upload-description"
          bind:value={uploadDescription}
          placeholder="Enter video description"
          rows={2}
          disabled={uploading}
        />
      </div>

      <!-- Upload Progress -->
      {#if uploading}
        <div class="space-y-2">
          <div class="flex justify-between text-xs text-muted-foreground">
            <span>
              {#if uploadStage === "initializing"}
                Initializing upload...
              {:else if uploadStage === "uploading"}
                Uploading... {uploadProgress}%
              {:else if uploadStage === "completing"}
                Completing upload...
              {:else if uploadStage === "done"}
                Upload complete!
              {/if}
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
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
        onclick={cancelUpload}
        disabled={uploading && uploadStage === "completing"}
      >
        {uploading ? "Cancel" : "Close"}
      </Button>
      <Button
        type="submit"
        variant="ghost"
        class="rounded-none h-12 flex-1 hover:bg-muted/10 text-primary hover:text-primary/80"
        disabled={uploading || !uploadFile}
        form="upload-form"
      >
        {uploading ? "Uploading..." : "Upload"}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>

<!-- Delete Confirmation Modal -->
<Dialog
  open={showDeleteModal}
  onOpenChange={(value) => {
    if (!deleting) {
      showDeleteModal = value;
      if (!value) assetToDelete = null;
    }
  }}
>
  <DialogContent class="max-w-sm rounded-none border-[hsl(var(--tn-fg-gutter)/0.3)] bg-background p-0 gap-0 overflow-hidden">
    <DialogHeader class="slab-header text-left space-y-1">
      <DialogTitle class="uppercase tracking-wide text-sm font-semibold text-destructive">Delete Video</DialogTitle>
      <DialogDescription class="text-xs text-muted-foreground/70">
        This action cannot be undone.
      </DialogDescription>
    </DialogHeader>

    <div class="slab-body--padded">
      <p class="text-sm text-foreground">
        Are you sure you want to delete <strong>{assetToDelete?.title || assetToDelete?.filename || "this video"}</strong>?
      </p>
      <p class="text-xs text-muted-foreground mt-2">
        The video will be permanently removed from your library.
      </p>
    </div>

    <DialogFooter class="slab-actions slab-actions--row gap-0">
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 border-r border-[hsl(var(--tn-fg-gutter)/0.3)] hover:bg-muted/10 text-muted-foreground hover:text-foreground"
        onclick={() => { showDeleteModal = false; assetToDelete = null; }}
        disabled={deleting}
      >
        Cancel
      </Button>
      <Button
        type="button"
        variant="ghost"
        class="rounded-none h-12 flex-1 hover:bg-destructive/10 text-destructive hover:text-destructive/80"
        onclick={deleteAsset}
        disabled={deleting}
      >
        {deleting ? "Deleting..." : "Delete"}
      </Button>
    </DialogFooter>
  </DialogContent>
</Dialog>
