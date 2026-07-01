<script lang="ts">
  import { onMount } from "svelte";
  import { resolve } from "$app/paths";
  import { get } from "svelte/store";
  import {
    fragment,
    GetStorageUsageStore,
    GetStreamsConnectionStore,
    StreamCoreFieldsStore,
    GetStorageArtifactsConnectionStore,
    DeleteClipStore,
    DeleteDVRStore,
    DeleteVodAssetStore,
  } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { Select, SelectContent, SelectItem, SelectTrigger } from "$lib/components/ui/select";
  import { GridSeam } from "$lib/components/layout";
  import DashboardMetricCard from "$lib/components/shared/DashboardMetricCard.svelte";
  import BreakdownChart from "$lib/components/charts/BreakdownChart.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import MediaRetentionPanel from "$lib/components/account/MediaRetentionPanel.svelte";
  import AssetRetentionDialog from "$lib/components/library/AssetRetentionDialog.svelte";
  import { toast } from "$lib/stores/toast";
  import {
    Archive,
    ArrowDown,
    ArrowUp,
    Clock,
    Disc,
    Film,
    HardDrive,
    Radio,
    RefreshCw,
    Search,
    Trash2,
  } from "lucide-svelte";

  const streamsStore = new GetStreamsConnectionStore();
  const streamCoreStore = new StreamCoreFieldsStore();
  const storageUsageStore = new GetStorageUsageStore();
  const storageArtifactsStore = new GetStorageArtifactsConnectionStore();
  const deleteClipMutation = new DeleteClipStore();
  const deleteDvrMutation = new DeleteDVRStore();
  const deleteVodMutation = new DeleteVodAssetStore();

  type DeleteMutationResult = { success?: boolean; message?: string } | null | undefined;
  type MediaKind = "vod" | "chapter" | "dvr" | "clip";
  type ViewMode = "all" | "stream" | "vod";
  type SortField = "created_at" | "title" | "kind" | "size_bytes" | "expires_at";
  type SortDirection = "asc" | "desc";

  type MediaAsset = {
    key: string;
    kind: MediaKind;
    id: string;
    hash: string;
    playbackId?: string;
    title: string;
    secondaryLabel: string;
    streamId?: string;
    streamTitle: string;
    sizeBytes: number | null;
    status: string;
    storageLocation?: string;
    isFrozen: boolean | null;
    createdAt: string | null;
    updatedAt: string | null;
    expiresAt: string | null;
    retention: {
      retentionDays: number;
      retentionUntil?: string | null;
      source: string;
    } | null;
    storageCost: {
      perDay: number;
      perMonth: number;
      currency: string;
    } | null;
    thumbnailUrl?: string;
  };

  type StorageKindInput = "VOD" | "CHAPTER" | "DVR" | "CLIP";
  type StorageSortInput = "CREATED_AT" | "TITLE" | "KIND" | "SIZE_BYTES" | "EXPIRES_AT";
  type SortDirectionInput = "ASC" | "DESC";

  type StorageArtifactWire = Omit<MediaAsset, "kind" | "retention"> & {
    kind: StorageKindInput;
    effectiveRetention: MediaAsset["retention"];
  };

  type StorageArtifactsGraphQLResponse = {
    storageArtifactsConnection: {
      nodes: StorageArtifactWire[];
      totalCount: number;
      hasNextPage: boolean;
      limit: number;
      offset: number;
    };
  };

  let viewMode = $state<ViewMode>("all");
  let selectedStreamId = $state<string | null>(null);
  let searchQuery = $state("");
  let sortField = $state<SortField>("created_at");
  let sortDirection = $state<SortDirection>("desc");
  let pageSize = $state(25);
  let pageOffset = $state(0);
  let artifacts = $state<MediaAsset[]>([]);
  let totalCount = $state(0);
  let hasNextPage = $state(false);
  let loadingArtifacts = $state(false);
  let searchTimer: ReturnType<typeof setTimeout> | null = null;

  let streams = $derived(
    $streamsStore.data?.streamsConnection?.edges?.map((edge) =>
      get(fragment(edge.node, streamCoreStore))
    ) ?? []
  );

  let selectedStream = $derived(
    streams.find((stream) => stream.id === selectedStreamId) ?? streams[0] ?? null
  );

  let storageBreakdown = $derived.by(() => {
    const edges =
      $storageUsageStore.data?.analytics?.usage?.storage?.storageUsageConnection?.edges ?? [];
    const node = edges[edges.length - 1]?.node;
    if (!node) return null;
    return {
      dvrBytes: node.dvrBytes,
      clipBytes: node.clipBytes,
      vodBytes: node.vodBytes,
      totalBytes: node.totalBytes,
    };
  });

  let loading = $derived($streamsStore.fetching || $storageUsageStore.fetching || loadingArtifacts);
  let totalBytes = $derived(artifacts.reduce((sum, asset) => sum + (asset.sizeBytes ?? 0), 0));
  let totalMonthlyCost = $derived(
    artifacts.reduce((sum, asset) => sum + (asset.storageCost?.perMonth ?? 0), 0)
  );
  let totalCurrency = $derived(
    artifacts.find((asset) => asset.storageCost?.currency)?.storageCost?.currency ?? ""
  );
  let loadedKinds = $derived.by(() => {
    const counts: Record<MediaKind, number> = { vod: 0, chapter: 0, dvr: 0, clip: 0 };
    for (const asset of artifacts) counts[asset.kind] += 1;
    return counts;
  });
  let showingFrom = $derived(totalCount === 0 ? 0 : pageOffset + 1);
  let showingTo = $derived(Math.min(pageOffset + artifacts.length, totalCount));

  let retentionDialogOpen = $state(false);
  let retentionDialogTarget = $state<{
    type: "DVR" | "CLIP" | "VOD";
    id: string;
    name: string;
    until: string | null;
  } | null>(null);

  function kindsForView(): MediaKind[] {
    if (viewMode === "vod") return ["vod"];
    if (viewMode === "stream") return ["dvr", "chapter", "clip"];
    return [];
  }

  function kindInput(kind: MediaKind): StorageKindInput {
    switch (kind) {
      case "chapter":
        return "CHAPTER";
      case "dvr":
        return "DVR";
      case "clip":
        return "CLIP";
      default:
        return "VOD";
    }
  }

  function sortInput(field: SortField): StorageSortInput {
    switch (field) {
      case "title":
        return "TITLE";
      case "kind":
        return "KIND";
      case "size_bytes":
        return "SIZE_BYTES";
      case "expires_at":
        return "EXPIRES_AT";
      default:
        return "CREATED_AT";
    }
  }

  function directionInput(direction: SortDirection): SortDirectionInput {
    return direction === "asc" ? "ASC" : "DESC";
  }

  function viewNeedsStream(): boolean {
    return viewMode === "stream";
  }

  async function loadArtifacts(resetPage = false) {
    if (resetPage) pageOffset = 0;
    if (viewNeedsStream() && !selectedStream?.id) {
      artifacts = [];
      totalCount = 0;
      hasNextPage = false;
      return;
    }

    loadingArtifacts = true;
    try {
      const kinds = kindsForView();
      const result = await storageArtifactsStore.fetch({
        policy: "NetworkOnly",
        variables: {
          input: {
            first: pageSize,
            offset: pageOffset,
            sort: sortInput(sortField),
            direction: directionInput(sortDirection),
            kinds: kinds.length > 0 ? kinds.map(kindInput) : null,
            search: searchQuery.trim() || null,
            streamId: viewNeedsStream() ? (selectedStream?.id ?? null) : null,
          },
        },
      });
      const data = result.data as unknown as StorageArtifactsGraphQLResponse | undefined;
      if (!data) throw new Error("empty GraphQL response");
      const connection = data.storageArtifactsConnection;
      artifacts = (connection.nodes ?? []).map(normalizeStorageArtifact);
      totalCount = connection.totalCount ?? 0;
      hasNextPage = connection.hasNextPage ?? false;
    } catch (err) {
      toast.error(`Failed to load storage artifacts: ${(err as Error).message}`);
    } finally {
      loadingArtifacts = false;
    }
  }

  function normalizeStorageArtifact(asset: StorageArtifactWire): MediaAsset {
    return {
      ...asset,
      kind: asset.kind.toLowerCase() as MediaKind,
      retention: asset.effectiveRetention,
    };
  }

  async function loadPage(offset: number) {
    pageOffset = Math.max(0, offset);
    await loadArtifacts(false);
  }

  function refreshPage() {
    void Promise.all([
      streamsStore.fetch({ policy: "NetworkOnly" }),
      storageUsageStore.fetch({ policy: "NetworkOnly" }),
    ]);
    void loadArtifacts(false);
  }

  function setViewMode(mode: ViewMode) {
    viewMode = mode;
    if (mode === "stream" && !selectedStreamId && streams[0]?.id) {
      selectedStreamId = streams[0].id;
    }
    void loadArtifacts(true);
  }

  function handleSelectedStreamChange(streamId: string) {
    selectedStreamId = streamId;
    void loadArtifacts(true);
  }

  function scheduleSearch() {
    if (searchTimer) clearTimeout(searchTimer);
    searchTimer = setTimeout(() => {
      void loadArtifacts(true);
    }, 250);
  }

  function setSort(field: SortField) {
    if (sortField === field) {
      sortDirection = sortDirection === "asc" ? "desc" : "asc";
    } else {
      sortField = field;
      sortDirection = field === "title" || field === "kind" ? "asc" : "desc";
    }
    void loadArtifacts(true);
  }

  function sortIcon(field: SortField) {
    if (sortField !== field) return null;
    return sortDirection === "asc" ? ArrowUp : ArrowDown;
  }

  function openRetentionDialog(asset: MediaAsset) {
    retentionDialogTarget = {
      type: asset.kind === "clip" ? "CLIP" : asset.kind === "dvr" ? "DVR" : "VOD",
      id: asset.hash,
      name: asset.title,
      until: asset.expiresAt ? new Date(asset.expiresAt).toISOString() : null,
    };
    retentionDialogOpen = true;
  }

  async function deleteAsset(asset: MediaAsset) {
    if (!confirm(`Delete ${kindLabel(asset.kind)} "${asset.title}"? Storage is freed immediately.`))
      return;
    let data: DeleteMutationResult;
    if (asset.kind === "clip") {
      data = (await deleteClipMutation.mutate({ id: asset.hash })).data?.deleteClip;
    } else if (asset.kind === "dvr") {
      data = (await deleteDvrMutation.mutate({ dvrHash: asset.hash })).data?.deleteDVR;
    } else {
      data = (await deleteVodMutation.mutate({ id: asset.hash })).data?.deleteVodAsset;
    }
    if (data && "success" in data) {
      toast.success(`${kindLabel(asset.kind)} deleted`);
      await loadArtifacts(false);
    } else if (data && "message" in data) {
      toast.error(String(data.message));
    }
  }

  function fmtMoney(
    amount: number | null | undefined,
    currency: string | null | undefined
  ): string {
    if (amount === null || amount === undefined || amount <= 0) return "—";
    const display = amount < 0.01 ? "< 0.01" : amount.toFixed(2);
    return currency ? `${display} ${currency}` : display;
  }

  function costCellLabel(asset: MediaAsset): string {
    if (asset.storageCost?.perMonth && asset.storageCost.perMonth > 0) {
      return fmtMoney(asset.storageCost.perMonth, asset.storageCost.currency);
    }
    if ((asset.sizeBytes ?? 0) > 0) {
      return "Operator-absorbed";
    }
    return "—";
  }

  function fmtBytes(bytes: number | null | undefined): string {
    if (!bytes || bytes <= 0) return "—";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    let v = bytes;
    while (v >= 1024 && i < units.length - 1) {
      v /= 1024;
      i++;
    }
    return `${v.toFixed(v < 10 ? 2 : 0)} ${units[i]}`;
  }

  function fmtDate(value: string | Date | null | undefined): string {
    if (!value) return "—";
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return "—";
    return date.toLocaleDateString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
    });
  }

  function sourceLabel(source: string | null | undefined): string {
    if (!source) return "";
    return source.replace(/_/g, " ").toLowerCase();
  }

  function fmtRetention(asset: MediaAsset): string {
    const eff = asset.retention;
    if (!eff || eff.retentionDays <= 0) return "Keep forever";
    return `${eff.retentionDays}d (${sourceLabel(eff.source)})`;
  }

  function kindLabel(kind: MediaKind) {
    switch (kind) {
      case "chapter":
        return "DVR chapter";
      case "dvr":
        return "DVR";
      case "clip":
        return "Clip";
      default:
        return "VOD";
    }
  }

  function kindClass(kind: MediaKind) {
    switch (kind) {
      case "chapter":
        return "bg-info/10 text-info border-info/30";
      case "dvr":
        return "bg-primary/10 text-primary border-primary/30";
      case "clip":
        return "bg-success/10 text-success border-success/30";
      default:
        return "bg-muted/50 text-muted-foreground border-[hsl(var(--tn-fg-gutter)/0.3)]";
    }
  }

  onMount(async () => {
    await Promise.all([streamsStore.fetch(), storageUsageStore.fetch()]);
    if (!selectedStreamId && streams[0]?.id) {
      selectedStreamId = streams[0].id;
    }
    await loadArtifacts(true);
  });
</script>

<svelte:head>
  <title>Storage - FrameWorks</title>
</svelte:head>

{#snippet sortHeader(field: SortField, label: string, align: "left" | "right" = "left")}
  {@const Icon = sortIcon(field)}
  <button
    type="button"
    class="inline-flex items-center gap-1 {align === 'right' ? 'justify-end w-full' : ''}"
    onclick={() => setSort(field)}
  >
    <span>{label}</span>
    {#if Icon}
      <Icon class="w-3 h-3" />
    {/if}
  </button>
{/snippet}

{#snippet assetTable(assets: MediaAsset[], showStream = true)}
  <div class="overflow-x-auto">
    <table class="w-full text-sm">
      <thead class="text-xs text-muted-foreground">
        <tr>
          <th class="text-left p-3">{@render sortHeader("title", "Asset")}</th>
          {#if showStream}
            <th class="text-left p-3">Stream</th>
          {/if}
          <th class="text-left p-3">{@render sortHeader("kind", "State")}</th>
          <th class="text-right p-3">{@render sortHeader("size_bytes", "Size", "right")}</th>
          <th class="text-left p-3">{@render sortHeader("created_at", "Created")}</th>
          <th class="text-left p-3">{@render sortHeader("expires_at", "Retention")}</th>
          <th class="text-right p-3">$/month</th>
          <th class="text-right p-3">Actions</th>
        </tr>
      </thead>
      <tbody>
        {#each assets as asset (asset.key)}
          <tr class="border-t border-[hsl(var(--tn-fg-gutter)/0.3)]">
            <td class="p-3 min-w-72">
              <div class="flex items-center gap-3">
                <div
                  class="w-12 h-8 bg-muted/40 border border-[hsl(var(--tn-fg-gutter)/0.25)] flex items-center justify-center overflow-hidden"
                >
                  {#if asset.thumbnailUrl}
                    <img src={asset.thumbnailUrl} alt="" class="w-full h-full object-cover" />
                  {:else if asset.kind === "clip"}
                    <Film class="w-4 h-4 text-muted-foreground" />
                  {:else if asset.kind === "dvr" || asset.kind === "chapter"}
                    <Radio class="w-4 h-4 text-muted-foreground" />
                  {:else}
                    <Disc class="w-4 h-4 text-muted-foreground" />
                  {/if}
                </div>
                <div class="min-w-0">
                  <div class="flex items-center gap-2">
                    <span
                      class="inline-flex items-center border px-1.5 py-0.5 text-[10px] uppercase tracking-wide {kindClass(
                        asset.kind
                      )}"
                    >
                      {kindLabel(asset.kind)}
                    </span>
                    <span class="font-medium truncate">{asset.title}</span>
                  </div>
                  <div class="text-xs text-muted-foreground font-mono truncate">{asset.hash}</div>
                </div>
              </div>
            </td>
            {#if showStream}
              <td class="p-3 min-w-44">
                <div class="truncate">{asset.streamTitle || "Unassigned"}</div>
                {#if asset.streamId}
                  <div class="text-xs text-muted-foreground font-mono truncate">
                    {asset.streamId}
                  </div>
                {/if}
              </td>
            {/if}
            <td class="p-3">
              <div class="flex flex-col gap-0.5">
                <span class="font-mono text-xs">{asset.status || "unknown"}</span>
                <span class="text-xs text-muted-foreground">
                  {asset.isFrozen ? "cold" : (asset.storageLocation ?? "local")}
                </span>
              </div>
            </td>
            <td class="p-3 text-right font-mono">{fmtBytes(asset.sizeBytes)}</td>
            <td class="p-3 font-mono text-xs">{fmtDate(asset.createdAt)}</td>
            <td class="p-3 font-mono text-xs">{fmtRetention(asset)}</td>
            <td class="p-3 text-right font-mono">{costCellLabel(asset)}</td>
            <td class="p-3 text-right">
              <div class="flex justify-end gap-1">
                <Button
                  size="sm"
                  variant="ghost"
                  onclick={() => openRetentionDialog(asset)}
                  title="Change retention"
                >
                  <Clock class="w-4 h-4" />
                </Button>
                <Button size="sm" variant="ghost" onclick={() => deleteAsset(asset)} title="Delete">
                  <Trash2 class="w-4 h-4 text-destructive" />
                </Button>
              </div>
            </td>
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{/snippet}

<div class="h-full flex flex-col">
  <div class="px-4 sm:px-6 lg:px-8 py-4 border-b border-[hsl(var(--tn-fg-gutter)/0.3)] shrink-0">
    <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
      <div class="flex items-center gap-3">
        <HardDrive class="w-5 h-5 text-primary" />
        <div>
          <h1 class="text-xl font-bold text-foreground">Storage</h1>
          <p class="text-sm text-muted-foreground">
            Browse and manage VOD uploads, stream artifacts, DVR recordings, and clips.
          </p>
        </div>
      </div>
      <div class="flex gap-2">
        <Button variant="ghost" onclick={refreshPage} disabled={loading}>
          <RefreshCw class="w-4 h-4 {loading ? 'animate-spin' : ''}" />
        </Button>
        <Button variant="outline" href={resolve("/library")}>Open library</Button>
      </div>
    </div>
  </div>

  <div class="flex-1 overflow-y-auto">
    {#if loading && artifacts.length === 0}
      <div class="flex items-center justify-center min-h-64">
        <div class="loading-spinner w-8 h-8"></div>
      </div>
    {:else}
      <div class="page-transition">
        <GridSeam cols={4} stack="2x2" surface="panel" flush={true} class="mb-0">
          <div>
            <DashboardMetricCard
              icon={Archive}
              iconColor="text-primary"
              value={totalCount}
              valueColor="text-foreground"
              label="Matching assets"
              subtitle={`${loadedKinds.vod} VOD · ${loadedKinds.dvr} DVR · ${loadedKinds.clip} clips loaded`}
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={HardDrive}
              iconColor="text-info"
              value={fmtBytes(totalBytes)}
              valueColor="text-foreground"
              label="Loaded bytes"
              subtitle="current page"
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={Radio}
              iconColor="text-success"
              value={streams.length}
              valueColor="text-foreground"
              label="Streams"
              subtitle={`${loadedKinds.chapter} finalized chapters loaded`}
            />
          </div>
          <div>
            <DashboardMetricCard
              icon={Disc}
              iconColor="text-warning"
              value={fmtMoney(totalMonthlyCost, totalCurrency)}
              valueColor="text-foreground"
              label="Loaded $/month"
              subtitle="operator-absorbed excluded"
            />
          </div>
        </GridSeam>

        <div class="dashboard-grid">
          <div class="slab col-span-full">
            <div class="slab-header">
              <h3>Media browser</h3>
              <div class="flex flex-wrap gap-2">
                <Button
                  size="sm"
                  variant={viewMode === "all" ? "default" : "outline"}
                  onclick={() => setViewMode("all")}
                >
                  All artifacts
                </Button>
                <Button
                  size="sm"
                  variant={viewMode === "stream" ? "default" : "outline"}
                  onclick={() => setViewMode("stream")}
                >
                  Stream artifacts
                </Button>
                <Button
                  size="sm"
                  variant={viewMode === "vod" ? "default" : "outline"}
                  onclick={() => setViewMode("vod")}
                >
                  VOD uploads
                </Button>
              </div>
            </div>
            <div class="slab-body--padded">
              <div class="grid gap-3 lg:grid-cols-[minmax(0,1fr)_auto] lg:items-center">
                <label class="relative block">
                  <Search
                    class="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground"
                  />
                  <input
                    class="h-10 w-full border border-[hsl(var(--tn-fg-gutter)/0.35)] bg-background pl-9 pr-3 text-sm outline-none focus:border-primary"
                    placeholder="Search title, hash, filename, or stream"
                    bind:value={searchQuery}
                    oninput={scheduleSearch}
                  />
                </label>
                {#if viewMode === "stream"}
                  <Select
                    value={selectedStream?.id ?? ""}
                    onValueChange={handleSelectedStreamChange}
                    type="single"
                  >
                    <SelectTrigger class="min-w-[260px] text-left">
                      {selectedStream?.name ?? "Select stream"}
                    </SelectTrigger>
                    <SelectContent>
                      {#each streams as stream (stream.id)}
                        <SelectItem value={stream.id}>{stream.name}</SelectItem>
                      {/each}
                    </SelectContent>
                  </Select>
                {/if}
              </div>
            </div>
          </div>

          <div class="slab col-span-full">
            <div class="slab-header">
              <h3>
                {viewMode === "vod"
                  ? "VOD uploads"
                  : viewMode === "stream"
                    ? (selectedStream?.name ?? "Stream artifacts")
                    : "All artifacts"}
              </h3>
              <span class="text-xs text-muted-foreground">
                {showingFrom}-{showingTo} of {totalCount}
              </span>
            </div>
            <div class="slab-body">
              {#if loadingArtifacts && artifacts.length === 0}
                <div class="p-4 text-sm text-muted-foreground">Loading artifacts…</div>
              {:else if artifacts.length === 0}
                <div class="p-4">
                  <EmptyState
                    icon="Archive"
                    title="No media"
                    description="Change the view or search query."
                  />
                </div>
              {:else}
                {@render assetTable(artifacts, viewMode !== "stream")}
              {/if}
            </div>
            <div
              class="flex items-center justify-between border-t border-[hsl(var(--tn-fg-gutter)/0.3)] px-3 py-2 text-xs text-muted-foreground"
            >
              <span>Page size {pageSize}</span>
              <div class="flex gap-2">
                <Button
                  size="sm"
                  variant="outline"
                  disabled={pageOffset === 0 || loadingArtifacts}
                  onclick={() => loadPage(pageOffset - pageSize)}
                >
                  Previous
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  disabled={!hasNextPage || loadingArtifacts}
                  onclick={() => loadPage(pageOffset + pageSize)}
                >
                  Next
                </Button>
              </div>
            </div>
          </div>

          <div class="col-span-full">
            <MediaRetentionPanel />
          </div>

          <div class="slab col-span-full">
            <div class="slab-header">
              <h3>Aggregate usage</h3>
            </div>
            <div class="slab-body--padded">
              <BreakdownChart
                mode="bar"
                horizontal
                format="bytes"
                height={180}
                emptyText="No storage data available"
                items={storageBreakdown
                  ? [
                      { label: "DVR", value: storageBreakdown.dvrBytes },
                      { label: "Clips", value: storageBreakdown.clipBytes },
                      { label: "VOD", value: storageBreakdown.vodBytes },
                    ]
                  : []}
              />
            </div>
          </div>
        </div>
      </div>
    {/if}
  </div>
</div>

{#if retentionDialogTarget}
  <AssetRetentionDialog
    bind:open={retentionDialogOpen}
    assetType={retentionDialogTarget.type}
    assetId={retentionDialogTarget.id}
    assetName={retentionDialogTarget.name}
    currentExpiresAt={retentionDialogTarget.until}
    onClose={() => {
      retentionDialogOpen = false;
      retentionDialogTarget = null;
    }}
    onSaved={() => loadArtifacts(false)}
  />
{/if}
