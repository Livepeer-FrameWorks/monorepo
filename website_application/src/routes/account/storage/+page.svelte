<script lang="ts">
  import { onMount } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import { resolve } from "$app/paths";
  import {
    GetVodAssetsConnectionStore,
    GetClipsConnectionStore,
    GetDVRRequestsStore,
    GetStorageUsageStore,
    DeleteClipStore,
    DeleteDVRStore,
    DeleteVodAssetStore,
  } from "$houdini";
  import { Button } from "$lib/components/ui/button";
  import { HardDrive, Film, Disc, Trash2, Clock } from "lucide-svelte";
  import StorageBreakdownChart from "$lib/components/charts/StorageBreakdownChart.svelte";
  import EmptyState from "$lib/components/EmptyState.svelte";
  import MediaRetentionPanel from "$lib/components/account/MediaRetentionPanel.svelte";
  import AssetRetentionDialog from "$lib/components/library/AssetRetentionDialog.svelte";
  import { toast } from "$lib/stores/toast";

  // Per-asset storage browser. Each row carries marginal $/day + $/month
  // for the tenant's tier. Three "no cost" states are visually distinct:
  //   - operator-absorbed (storageCost = null, asset has bytes): the
  //     hosting cluster is self-hosted or third-party-marketplace;
  //   - pending/unsized (sizeBytes ≤ 0): renders "—" with no operator label;
  //   - free tier with no metered storage: renders "—".

  const vodStore = new GetVodAssetsConnectionStore();
  const clipStore = new GetClipsConnectionStore();
  const dvrStore = new GetDVRRequestsStore();
  const storageUsageStore = new GetStorageUsageStore();
  const deleteClipMutation = new DeleteClipStore();
  const deleteDvrMutation = new DeleteDVRStore();
  const deleteVodMutation = new DeleteVodAssetStore();

  let loading = $derived(
    $vodStore.fetching || $clipStore.fetching || $dvrStore.fetching || $storageUsageStore.fetching
  );
  let loadingMoreVod = $state(false);
  let loadingMoreClips = $state(false);
  let loadingMoreDvr = $state(false);

  let vodAssets = $derived($vodStore.data?.vodAssetsConnection?.edges?.map((e) => e.node) ?? []);
  let clips = $derived($clipStore.data?.clipsConnection?.edges?.map((e) => e.node) ?? []);
  let dvrs = $derived($dvrStore.data?.dvrRecordingsConnection?.edges?.map((e) => e.node) ?? []);

  let vodPageInfo = $derived($vodStore.data?.vodAssetsConnection?.pageInfo);
  let clipsPageInfo = $derived($clipStore.data?.clipsConnection?.pageInfo);
  let dvrPageInfo = $derived($dvrStore.data?.dvrRecordingsConnection?.pageInfo);
  let vodTotal = $derived($vodStore.data?.vodAssetsConnection?.totalCount ?? null);
  let clipsTotal = $derived($clipStore.data?.clipsConnection?.totalCount ?? null);
  let dvrTotal = $derived($dvrStore.data?.dvrRecordingsConnection?.totalCount ?? null);

  // Latest storage-usage snapshot for the breakdown chart. The query
  // returns a time-series; the chart consumes the most recent row.
  let storageBreakdown = $derived.by(() => {
    const edges =
      $storageUsageStore.data?.analytics?.usage?.storage?.storageUsageConnection?.edges ?? [];
    if (edges.length === 0) return null;
    const node = edges[edges.length - 1]?.node;
    if (!node) return null;
    return {
      dvrBytes: node.dvrBytes,
      clipBytes: node.clipBytes,
      vodBytes: node.vodBytes,
      totalBytes: node.totalBytes,
    };
  });

  // The page rollup is accurate only when every page is loaded. We warn when
  // a section still has more pages so users don't read the bill projection
  // as authoritative on a partial view.
  let hasUnloadedPages = $derived(
    (vodPageInfo?.hasNextPage ?? false) ||
      (clipsPageInfo?.hasNextPage ?? false) ||
      (dvrPageInfo?.hasNextPage ?? false)
  );

  // Per-stream rollup: aggregate cost across each stream's DVR + clip
  // artifacts. VOD has no stream binding and is summarized separately below.
  type RollupRow = {
    streamId: string;
    title: string;
    bytes: number;
    perMonth: number;
    currency: string;
    items: number;
  };
  let perStreamRollups = $derived.by<RollupRow[]>(() => {
    const m = new SvelteMap<string, RollupRow>();
    const accumulate = (
      streamId: string | null | undefined,
      title: string | null | undefined,
      sizeBytes: number | null | undefined,
      cost: { perMonth: number; currency: string } | null | undefined
    ) => {
      if (!streamId) return;
      const row = m.get(streamId) ?? {
        streamId,
        title: title ?? streamId,
        bytes: 0,
        perMonth: 0,
        currency: cost?.currency ?? "",
        items: 0,
      };
      row.bytes += sizeBytes ?? 0;
      row.perMonth += cost?.perMonth ?? 0;
      row.items += 1;
      if (cost?.currency && !row.currency) row.currency = cost.currency;
      m.set(streamId, row);
    };
    for (const d of dvrs) accumulate(d.streamId, d.title, d.sizeBytes, d.storageCost);
    for (const c of clips) accumulate(c.streamId, c.title, c.sizeBytes, c.storageCost);
    return Array.from(m.values()).sort((a, b) => b.perMonth - a.perMonth);
  });

  function fmtMoney(
    amount: number | null | undefined,
    currency: string | null | undefined
  ): string {
    if (amount === null || amount === undefined || amount <= 0) return "—";
    const display = amount < 0.01 ? "< 0.01" : amount.toFixed(2);
    return currency ? `${display} ${currency}` : display;
  }

  // costCellLabel distinguishes operator-absorbed from blank/unsized:
  //   - cost present & > 0 → render money
  //   - cost null && bytes > 0 → "operator-absorbed" (self-hosted /
  //     third-party-marketplace cluster; pricing intentionally null)
  //   - else → "—"
  function costCellLabel(
    cost: { perDay?: number; perMonth?: number; currency?: string } | null | undefined,
    bytes: number | null | undefined,
    field: "perDay" | "perMonth"
  ): string {
    const amount = cost ? (field === "perDay" ? cost.perDay : cost.perMonth) : null;
    if (amount !== null && amount !== undefined && amount > 0) {
      return fmtMoney(amount, cost?.currency ?? "");
    }
    if (cost === null && (bytes ?? 0) > 0) {
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

  // sourceLabel renders GraphQL's SCREAMING_CASE enum into a humane string
  // (e.g. PER_ASSET_OVERRIDE → "per-asset override").
  function sourceLabel(source: string | null | undefined): string {
    if (!source) return "";
    return source.replace(/_/g, " ").toLowerCase();
  }

  function fmtRetention(
    eff:
      | {
          retentionDays: number;
          retentionUntil?: Date | string | null;
          source: string;
        }
      | null
      | undefined
  ): string {
    if (!eff || eff.retentionDays <= 0) return "Keep forever";
    return `${eff.retentionDays}d (${sourceLabel(eff.source)})`;
  }

  // Per-asset action dialog state.
  let retentionDialogOpen = $state(false);
  let retentionDialogTarget = $state<{
    type: "DVR" | "CLIP" | "VOD";
    id: string;
    name: string;
    until: string | null;
  } | null>(null);

  function openRetentionDialog(
    type: "DVR" | "CLIP" | "VOD",
    id: string,
    name: string,
    until: Date | string | null | undefined
  ) {
    retentionDialogTarget = {
      type,
      id,
      name,
      until: until ? (until instanceof Date ? until.toISOString() : until) : null,
    };
    retentionDialogOpen = true;
  }

  async function refreshConnections() {
    await Promise.all([
      vodStore.fetch({ policy: "NetworkOnly" }),
      clipStore.fetch({ policy: "NetworkOnly" }),
      dvrStore.fetch({ policy: "NetworkOnly" }),
      storageUsageStore.fetch({ policy: "NetworkOnly" }),
    ]);
  }

  async function deleteClip(id: string, name: string) {
    if (!confirm(`Delete clip "${name}"? Storage is freed immediately.`)) return;
    const result = await deleteClipMutation.mutate({ id });
    const data = result.data?.deleteClip;
    if (data && "success" in data) {
      toast.success(`Clip deleted`);
      await refreshConnections();
    } else if (data && "message" in data) {
      toast.error(String(data.message));
    }
  }
  async function deleteDvr(hash: string, name: string) {
    if (!confirm(`Delete DVR recording "${name}"? Storage is freed immediately.`)) return;
    const result = await deleteDvrMutation.mutate({ dvrHash: hash });
    const data = result.data?.deleteDVR;
    if (data && "success" in data) {
      toast.success(`DVR recording deleted`);
      await refreshConnections();
    } else if (data && "message" in data) {
      toast.error(String(data.message));
    }
  }
  async function deleteVod(id: string, name: string) {
    if (!confirm(`Delete VOD "${name}"? Storage is freed immediately.`)) return;
    const result = await deleteVodMutation.mutate({ id });
    const data = result.data?.deleteVodAsset;
    if (data && "success" in data) {
      toast.success(`VOD deleted`);
      await refreshConnections();
    } else if (data && "message" in data) {
      toast.error(String(data.message));
    }
  }

  async function loadMoreVod() {
    if (loadingMoreVod || !vodPageInfo?.hasNextPage || !vodPageInfo.endCursor) return;
    loadingMoreVod = true;
    try {
      await vodStore.fetch({ variables: { first: 50, after: vodPageInfo.endCursor } });
    } catch (e) {
      toast.error(`Failed to load more VOD: ${(e as Error).message}`);
    } finally {
      loadingMoreVod = false;
    }
  }
  async function loadMoreClips() {
    if (loadingMoreClips || !clipsPageInfo?.hasNextPage || !clipsPageInfo.endCursor) return;
    loadingMoreClips = true;
    try {
      await clipStore.fetch({ variables: { first: 50, after: clipsPageInfo.endCursor } });
    } catch (e) {
      toast.error(`Failed to load more clips: ${(e as Error).message}`);
    } finally {
      loadingMoreClips = false;
    }
  }
  async function loadMoreDvr() {
    if (loadingMoreDvr || !dvrPageInfo?.hasNextPage || !dvrPageInfo.endCursor) return;
    loadingMoreDvr = true;
    try {
      await dvrStore.fetch({ variables: { first: 50, after: dvrPageInfo.endCursor } });
    } catch (e) {
      toast.error(`Failed to load more DVR: ${(e as Error).message}`);
    } finally {
      loadingMoreDvr = false;
    }
  }

  onMount(() => {
    vodStore.fetch();
    clipStore.fetch();
    dvrStore.fetch();
    storageUsageStore.fetch();
  });
</script>

<svelte:head>
  <title>Storage · FrameWorks</title>
</svelte:head>

<div class="page-padded space-y-6">
  <header class="flex items-center justify-between">
    <div class="flex items-center gap-2">
      <HardDrive class="w-5 h-5 text-primary" />
      <h1 class="text-xl">Storage</h1>
    </div>
    <Button variant="outline" href={resolve("/library")}>Open library</Button>
  </header>

  <p class="text-sm text-muted-foreground max-w-3xl">
    Everything you're keeping. Costs are the marginal monthly amount each asset is adding to your
    bill at your current tier — delete an asset to recover that amount on next billing. Assets on
    self-hosted or third-party marketplace clusters render as <em>operator-absorbed</em>.
  </p>

  {#if hasUnloadedPages}
    <div class="text-xs text-warning">
      The per-stream rollup below only sums currently-loaded artifacts. Load more in each section
      for a complete picture.
    </div>
  {/if}

  <section class="slab">
    <div class="slab-header">
      <h2>Per-stream rollup</h2>
    </div>
    <div class="slab-body--padded">
      {#if loading && perStreamRollups.length === 0}
        <p class="text-sm text-muted-foreground">Loading…</p>
      {:else if perStreamRollups.length === 0}
        <EmptyState
          icon="Video"
          title="No stream-bound artifacts"
          description="Your DVR recordings and clips will appear here grouped by stream."
        />
      {:else}
        <table class="w-full text-sm">
          <thead class="text-xs text-muted-foreground">
            <tr>
              <th class="text-left p-2">Stream</th>
              <th class="text-right p-2">Items</th>
              <th class="text-right p-2">Bytes</th>
              <th class="text-right p-2">$/month</th>
            </tr>
          </thead>
          <tbody>
            {#each perStreamRollups as row (row.streamId)}
              <tr class="border-t">
                <td class="p-2">{row.title}</td>
                <td class="p-2 text-right font-mono">{row.items}</td>
                <td class="p-2 text-right font-mono">{fmtBytes(row.bytes)}</td>
                <td class="p-2 text-right font-mono">{fmtMoney(row.perMonth, row.currency)}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      {/if}
    </div>
  </section>

  <section class="slab">
    <div class="slab-header flex items-center justify-between">
      <h2 class="flex items-center gap-2"><Disc class="w-4 h-4" /> VOD uploads</h2>
      {#if vodTotal !== null}
        <span class="text-xs text-muted-foreground font-mono">
          {vodAssets.length} of {vodTotal}
        </span>
      {/if}
    </div>
    <div class="slab-body--padded overflow-x-auto">
      {#if vodAssets.length === 0}
        <p class="text-sm text-muted-foreground">No VOD uploads yet.</p>
      {:else}
        <table class="w-full text-sm">
          <thead class="text-xs text-muted-foreground">
            <tr>
              <th class="text-left p-2">Title</th>
              <th class="text-right p-2">Size</th>
              <th class="text-left p-2">Retention</th>
              <th class="text-right p-2">$/day</th>
              <th class="text-right p-2">$/month</th>
              <th class="text-right p-2">Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each vodAssets as v (v.id)}
              <tr class="border-t">
                <td class="p-2">{v.title ?? v.filename ?? v.artifactHash}</td>
                <td class="p-2 text-right font-mono">{fmtBytes(v.sizeBytes)}</td>
                <td class="p-2 font-mono">{fmtRetention(v.effectiveRetention)}</td>
                <td class="p-2 text-right font-mono">
                  {costCellLabel(v.storageCost, v.sizeBytes, "perDay")}
                </td>
                <td class="p-2 text-right font-mono">
                  {costCellLabel(v.storageCost, v.sizeBytes, "perMonth")}
                </td>
                <td class="p-2 text-right">
                  <div class="flex justify-end gap-1">
                    <Button
                      size="sm"
                      variant="ghost"
                      onclick={() =>
                        openRetentionDialog(
                          "VOD",
                          v.id,
                          v.title ?? v.filename ?? v.artifactHash,
                          v.expiresAt
                        )}
                      title="Change retention"
                    >
                      <Clock class="w-4 h-4" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      onclick={() => deleteVod(v.id, v.title ?? v.filename ?? v.artifactHash)}
                      title="Delete"
                    >
                      <Trash2 class="w-4 h-4 text-destructive" />
                    </Button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#if vodPageInfo?.hasNextPage}
          <div class="pt-3 flex justify-center">
            <Button variant="outline" size="sm" onclick={loadMoreVod} disabled={loadingMoreVod}>
              {loadingMoreVod ? "Loading…" : "Load more"}
            </Button>
          </div>
        {/if}
      {/if}
    </div>
  </section>

  <section class="slab">
    <div class="slab-header flex items-center justify-between">
      <h2 class="flex items-center gap-2"><Film class="w-4 h-4" /> Clips</h2>
      {#if clipsTotal !== null}
        <span class="text-xs text-muted-foreground font-mono">
          {clips.length} of {clipsTotal}
        </span>
      {/if}
    </div>
    <div class="slab-body--padded overflow-x-auto">
      {#if clips.length === 0}
        <p class="text-sm text-muted-foreground">No clips yet.</p>
      {:else}
        <table class="w-full text-sm">
          <thead class="text-xs text-muted-foreground">
            <tr>
              <th class="text-left p-2">Title</th>
              <th class="text-right p-2">Size</th>
              <th class="text-left p-2">Retention</th>
              <th class="text-right p-2">$/day</th>
              <th class="text-right p-2">$/month</th>
              <th class="text-right p-2">Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each clips as c (c.id)}
              <tr class="border-t">
                <td class="p-2">{c.title ?? c.clipHash}</td>
                <td class="p-2 text-right font-mono">{fmtBytes(c.sizeBytes)}</td>
                <td class="p-2 font-mono">{fmtRetention(c.effectiveRetention)}</td>
                <td class="p-2 text-right font-mono">
                  {costCellLabel(c.storageCost, c.sizeBytes, "perDay")}
                </td>
                <td class="p-2 text-right font-mono">
                  {costCellLabel(c.storageCost, c.sizeBytes, "perMonth")}
                </td>
                <td class="p-2 text-right">
                  <div class="flex justify-end gap-1">
                    <Button
                      size="sm"
                      variant="ghost"
                      onclick={() =>
                        openRetentionDialog("CLIP", c.id, c.title ?? c.clipHash, c.expiresAt)}
                      title="Change retention"
                    >
                      <Clock class="w-4 h-4" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      onclick={() => deleteClip(c.id, c.title ?? c.clipHash)}
                      title="Delete"
                    >
                      <Trash2 class="w-4 h-4 text-destructive" />
                    </Button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#if clipsPageInfo?.hasNextPage}
          <div class="pt-3 flex justify-center">
            <Button variant="outline" size="sm" onclick={loadMoreClips} disabled={loadingMoreClips}>
              {loadingMoreClips ? "Loading…" : "Load more"}
            </Button>
          </div>
        {/if}
      {/if}
    </div>
  </section>

  <section class="slab">
    <div class="slab-header flex items-center justify-between">
      <h2 class="flex items-center gap-2"><Disc class="w-4 h-4" /> DVR recordings</h2>
      {#if dvrTotal !== null}
        <span class="text-xs text-muted-foreground font-mono">
          {dvrs.length} of {dvrTotal}
        </span>
      {/if}
    </div>
    <div class="slab-body--padded overflow-x-auto">
      {#if dvrs.length === 0}
        <p class="text-sm text-muted-foreground">No DVR recordings yet.</p>
      {:else}
        <table class="w-full text-sm">
          <thead class="text-xs text-muted-foreground">
            <tr>
              <th class="text-left p-2">Title</th>
              <th class="text-right p-2">Size</th>
              <th class="text-left p-2">Retention</th>
              <th class="text-right p-2">$/day</th>
              <th class="text-right p-2">$/month</th>
              <th class="text-right p-2">Actions</th>
            </tr>
          </thead>
          <tbody>
            {#each dvrs as d (d.id)}
              <tr class="border-t">
                <td class="p-2">{d.title ?? d.dvrHash}</td>
                <td class="p-2 text-right font-mono">{fmtBytes(d.sizeBytes)}</td>
                <td class="p-2 font-mono">{fmtRetention(d.effectiveRetention)}</td>
                <td class="p-2 text-right font-mono">
                  {costCellLabel(d.storageCost, d.sizeBytes, "perDay")}
                </td>
                <td class="p-2 text-right font-mono">
                  {costCellLabel(d.storageCost, d.sizeBytes, "perMonth")}
                </td>
                <td class="p-2 text-right">
                  <div class="flex justify-end gap-1">
                    <Button
                      size="sm"
                      variant="ghost"
                      onclick={() =>
                        openRetentionDialog("DVR", d.dvrHash, d.title ?? d.dvrHash, d.expiresAt)}
                      title="Change retention"
                    >
                      <Clock class="w-4 h-4" />
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      onclick={() => deleteDvr(d.dvrHash, d.title ?? d.dvrHash)}
                      title="Delete"
                    >
                      <Trash2 class="w-4 h-4 text-destructive" />
                    </Button>
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
        {#if dvrPageInfo?.hasNextPage}
          <div class="pt-3 flex justify-center">
            <Button variant="outline" size="sm" onclick={loadMoreDvr} disabled={loadingMoreDvr}>
              {loadingMoreDvr ? "Loading…" : "Load more"}
            </Button>
          </div>
        {/if}
      {/if}
    </div>
  </section>

  <MediaRetentionPanel />

  <section class="slab">
    <div class="slab-header">
      <h2>Aggregate usage</h2>
    </div>
    <div class="slab-body--padded">
      <StorageBreakdownChart data={storageBreakdown} />
    </div>
  </section>
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
    onSaved={refreshConnections}
  />
{/if}
