<script lang="ts">
  import {
    Table,
    TableBody,
    TableCell,
    TableHead,
    TableHeader,
    TableRow,
  } from "$lib/components/ui/table";
  import { Button } from "$lib/components/ui/button";

  interface AssetRow {
    artifactHash: string;
    totalSessions: number;
    durationS: number;
    lastSeen: string;
    title?: string | null;
    playbackId?: string | null;
  }

  interface Props {
    assets: AssetRow[];
    selectedHash?: string | null;
    loading?: boolean;
    totalCount?: number;
    hasNextPage?: boolean;
    hasPreviousPage?: boolean;
    onSelect: (hash: string) => void;
    onNext?: () => void;
    onPrev?: () => void;
  }

  let {
    assets = [],
    selectedHash = null,
    loading = false,
    totalCount = 0,
    hasNextPage = false,
    hasPreviousPage = false,
    onSelect,
    onNext,
    onPrev,
  }: Props = $props();

  const hasPageControls = $derived(hasNextPage || hasPreviousPage);

  function shortHash(hash: string): string {
    return hash.length > 12 ? `${hash.slice(0, 12)}…` : hash;
  }

  function fmtDuration(seconds: number): string {
    if (!seconds || seconds <= 0) return "—";
    const h = Math.floor(seconds / 3600);
    const m = Math.floor((seconds % 3600) / 60);
    const s = Math.floor(seconds % 60);
    const pad = (n: number) => n.toString().padStart(2, "0");
    return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
  }

  function fmtLastSeen(iso: string): string {
    if (!iso) return "—";
    const then = new Date(iso).getTime();
    if (Number.isNaN(then)) return "—";
    const diffMs = Date.now() - then;
    const mins = Math.round(diffMs / 60000);
    if (mins < 1) return "just now";
    if (mins < 60) return `${mins}m ago`;
    const hours = Math.round(mins / 60);
    if (hours < 24) return `${hours}h ago`;
    const days = Math.round(hours / 24);
    if (days < 30) return `${days}d ago`;
    return new Date(iso).toLocaleDateString();
  }
</script>

<div class="space-y-3">
  {#if loading && assets.length === 0}
    <div class="flex items-center justify-center py-8">
      <div class="loading-spinner w-6 h-6"></div>
    </div>
  {:else if assets.length === 0}
    <p class="text-xs text-muted-foreground py-4">
      No VOD assets have retention data in this time range yet.
    </p>
  {:else}
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Asset</TableHead>
          <TableHead class="text-right">Sessions</TableHead>
          <TableHead class="text-right">Duration</TableHead>
          <TableHead class="text-right">Last seen</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {#each assets as asset (asset.artifactHash)}
          <TableRow
            class={"cursor-pointer hover:bg-muted/30 transition-colors " +
              (asset.artifactHash === selectedHash ? "bg-primary/10" : "")}
            onclick={() => onSelect(asset.artifactHash)}
          >
            <TableCell>
              <div class="flex flex-col">
                <span class="text-sm text-foreground">
                  {asset.title || shortHash(asset.artifactHash)}
                </span>
                {#if asset.title}
                  <span class="text-[10px] text-muted-foreground font-mono">
                    {shortHash(asset.artifactHash)}
                  </span>
                {/if}
              </div>
            </TableCell>
            <TableCell class="text-right">{asset.totalSessions}</TableCell>
            <TableCell class="text-right">{fmtDuration(asset.durationS)}</TableCell>
            <TableCell class="text-right text-muted-foreground"
              >{fmtLastSeen(asset.lastSeen)}</TableCell
            >
          </TableRow>
        {/each}
      </TableBody>
    </Table>

    <div class="flex items-center justify-between">
      <span class="text-xs text-muted-foreground"
        >{totalCount} asset{totalCount === 1 ? "" : "s"}</span
      >
      {#if hasPageControls}
        <div class="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={!hasPreviousPage || loading}
            onclick={() => onPrev?.()}
          >
            Previous
          </Button>
          <Button
            variant="outline"
            size="sm"
            disabled={!hasNextPage || loading}
            onclick={() => onNext?.()}
          >
            Next
          </Button>
        </div>
      {/if}
    </div>
  {/if}
</div>
