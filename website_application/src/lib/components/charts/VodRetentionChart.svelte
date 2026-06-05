<script lang="ts">
  // VOD retention heatmap. Renders two things from the per-bucket data:
  //  - an audience-retention area curve (reached/totalSessions at each timeline bucket)
  //  - a "most replayed" density strip beneath it (per-bucket watched-seconds, color
  //    intensity normalized to the busiest bucket — YouTube-style).
  // Pure SVG so it carries no chart-library dependency.

  interface Point {
    bucketIndex: number;
    secondsWatched: number;
    reached: number;
  }

  interface Props {
    points: Point[];
    totalSessions: number;
    bucketWidthS: number;
    assetDurationS: number;
  }

  let { points, totalSessions, bucketWidthS, assetDurationS }: Props = $props();

  const W = 1000;
  const H = 200;
  const STRIP_H = 28;
  const PLOT_H = H - STRIP_H - 8;

  // Densify: points is sparse (unwatched buckets omitted) — fill the timeline so the
  // curve and strip show gaps as true zeros, not interpolated-over.
  let bucketCount = $derived(
    bucketWidthS > 0 && assetDurationS > 0
      ? Math.ceil(assetDurationS / bucketWidthS)
      : points.length
  );

  let dense = $derived.by(() => {
    const byIndex = new Map(points.map((p) => [p.bucketIndex, p]));
    const n = Math.max(bucketCount, 1);
    const out: Array<{ retention: number; density: number }> = [];
    for (let i = 0; i < n; i++) {
      const p = byIndex.get(i);
      out.push({
        retention: p && totalSessions > 0 ? Math.min(1, p.reached / totalSessions) : 0,
        density: p ? p.secondsWatched : 0,
      });
    }
    return out;
  });

  let maxDensity = $derived(Math.max(1, ...dense.map((d) => d.density)));
  let n = $derived(Math.max(dense.length, 1));
  let dx = $derived(W / n);

  // Area path for the retention curve.
  let areaPath = $derived.by(() => {
    if (dense.length === 0) return "";
    let d = `M 0 ${PLOT_H}`;
    dense.forEach((pt, i) => {
      const x = i * dx + dx / 2;
      const y = PLOT_H - pt.retention * PLOT_H;
      d += ` L ${x.toFixed(1)} ${y.toFixed(1)}`;
    });
    d += ` L ${W} ${PLOT_H} Z`;
    return d;
  });

  function tlabel(sec: number): string {
    const s = Math.round(sec);
    const m = Math.floor(s / 60);
    const r = s % 60;
    return m > 0 ? `${m}:${String(r).padStart(2, "0")}` : `${r}s`;
  }
</script>

{#if dense.length === 0 || totalSessions === 0}
  <div class="text-sm text-muted-foreground py-8 text-center">
    No retention data for this asset yet.
  </div>
{:else}
  <svg viewBox="0 0 {W} {H}" class="w-full" role="img" aria-label="VOD retention curve">
    <!-- retention area -->
    <path
      d={areaPath}
      fill="hsl(var(--tn-blue) / 0.18)"
      stroke="hsl(var(--tn-blue))"
      stroke-width="2"
    />
    <!-- density strip ("most replayed") -->
    {#each dense as pt, i (i)}
      <rect
        x={i * dx}
        y={PLOT_H + 8}
        width={Math.max(dx - 0.5, 0.5)}
        height={STRIP_H}
        fill="hsl(var(--tn-purple))"
        opacity={0.12 + 0.88 * (pt.density / maxDensity)}
      />
    {/each}
  </svg>
  <div class="flex justify-between text-[10px] text-muted-foreground mt-1">
    <span>0:00</span>
    <span>{tlabel(assetDurationS / 2)}</span>
    <span>{tlabel(assetDurationS)}</span>
  </div>
  <div class="flex gap-4 text-[10px] text-muted-foreground mt-2">
    <span
      ><span class="inline-block w-2 h-2 align-middle" style="background:hsl(var(--tn-blue))"
      ></span> audience retention</span
    >
    <span
      ><span class="inline-block w-2 h-2 align-middle" style="background:hsl(var(--tn-purple))"
      ></span> most replayed (watch density)</span
    >
  </div>
{/if}
