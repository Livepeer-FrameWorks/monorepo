<script lang="ts">
  // Audience-retention curve + "most replayed" density strip. Smoothed area curve,
  // gradient fill, hover scrubbing, and an edge-clamped most-replayed pill. Pure SVG,
  // no chart library. Ported from the marketing dashboard design.
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
  let { points = [], totalSessions = 0, bucketWidthS = 0, assetDurationS = 0 }: Props = $props();

  const W = 1000;
  const PLOT_TOP = 14;
  const PLOT_BOTTOM = 150;
  const STRIP_TOP = 166;
  const STRIP_H = 28;
  const H = STRIP_TOP + STRIP_H + 2;

  let wrap = $state<HTMLDivElement>();
  let hover = $state<number | null>(null);

  function tlabel(sec: number): string {
    const s = Math.round(sec);
    const m = Math.floor(s / 60);
    const r = s % 60;
    return m > 0 ? `${m}:${String(r).padStart(2, "0")}` : `${r}s`;
  }

  // Catmull-Rom to cubic bezier so the curve reads smooth, not segmented.
  function smoothLine(pts: number[][]): string {
    if (pts.length < 2) return "";
    let d = `M ${pts[0][0].toFixed(1)} ${pts[0][1].toFixed(1)}`;
    for (let i = 0; i < pts.length - 1; i++) {
      const p0 = pts[i - 1] || pts[i];
      const p1 = pts[i];
      const p2 = pts[i + 1];
      const p3 = pts[i + 2] || p2;
      const c1x = p1[0] + (p2[0] - p0[0]) / 6;
      const c1y = p1[1] + (p2[1] - p0[1]) / 6;
      const c2x = p2[0] - (p3[0] - p1[0]) / 6;
      const c2y = p2[1] - (p3[1] - p1[1]) / 6;
      d += ` C ${c1x.toFixed(1)} ${c1y.toFixed(1)}, ${c2x.toFixed(1)} ${c2y.toFixed(1)}, ${p2[0].toFixed(1)} ${p2[1].toFixed(1)}`;
    }
    return d;
  }

  const model = $derived.by(() => {
    const bucketCount =
      bucketWidthS > 0 && assetDurationS > 0
        ? Math.ceil(assetDurationS / bucketWidthS)
        : points.length;
    const byIndex = new Map(points.map((p) => [p.bucketIndex, p]));
    const n = Math.max(bucketCount, 1);
    const dense: { retention: number; density: number }[] = [];
    for (let i = 0; i < n; i++) {
      const p = byIndex.get(i);
      dense.push({
        retention: p && totalSessions > 0 ? Math.min(1, p.reached / totalSessions) : 0,
        density: p ? p.secondsWatched : 0,
      });
    }
    const maxDensity = Math.max(1, ...dense.map((d) => d.density));
    const dx = W / n;
    const linePts = dense.map((d, i) => [
      i * dx + dx / 2,
      PLOT_BOTTOM - d.retention * (PLOT_BOTTOM - PLOT_TOP),
    ]);
    const line = smoothLine(linePts);
    const area = line ? `${line} L ${W} ${PLOT_BOTTOM} L 0 ${PLOT_BOTTOM} Z` : "";
    let peakIdx = 0;
    dense.forEach((d, i) => {
      if (d.density > dense[peakIdx].density) peakIdx = i;
    });
    return { dense, maxDensity, dx, linePts, line, area, peakIdx, n };
  });

  let peakX = $derived(model.linePts.length ? model.linePts[model.peakIdx][0] : 0);
  let peakFrac = $derived(peakX / W);
  let peakEdge = $derived(peakFrac > 0.78 ? "right" : peakFrac < 0.22 ? "left" : "mid");
  let hoverPt = $derived(hover != null && model.linePts[hover] ? model.linePts[hover] : null);
  let hoverData = $derived(hover != null && model.dense[hover] ? model.dense[hover] : null);

  function onMove(e: MouseEvent) {
    const rect = wrap?.getBoundingClientRect();
    if (!rect) return;
    const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
    hover = Math.min(model.n - 1, Math.round(frac * (model.n - 1)));
  }
</script>

{#if model.dense.length === 0 || totalSessions === 0}
  <div class="retention__empty">No retention data for this asset yet.</div>
{:else}
  <div class="retention">
    <div
      bind:this={wrap}
      class="retention__plot"
      role="img"
      aria-label="Audience retention curve with most-replayed density"
      onmousemove={onMove}
      onmouseleave={() => (hover = null)}
    >
      <svg viewBox="0 0 {W} {H}" class="retention__svg" preserveAspectRatio="none">
        <defs>
          <linearGradient id="retention-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="hsl(var(--tn-cyan))" stop-opacity="0.34" />
            <stop offset="100%" stop-color="hsl(var(--tn-cyan))" stop-opacity="0.02" />
          </linearGradient>
        </defs>

        {#each [0.25, 0.5, 0.75] as g (g)}
          <line
            x1="0"
            y1={PLOT_BOTTOM - g * (PLOT_BOTTOM - PLOT_TOP)}
            x2={W}
            y2={PLOT_BOTTOM - g * (PLOT_BOTTOM - PLOT_TOP)}
            class="retention__grid"
          />
        {/each}

        <path d={model.area} fill="url(#retention-fill)" />
        <path d={model.line} class="retention__line" fill="none" />

        {#each model.dense as d, i (i)}
          <rect
            x={i * model.dx}
            y={STRIP_TOP}
            width={Math.max(model.dx - 0.6, 0.6)}
            height={STRIP_H}
            class="retention__density"
            opacity={0.1 + 0.9 * (d.density / model.maxDensity)}
          />
        {/each}

        <line
          x1={peakX}
          y1={PLOT_TOP}
          x2={peakX}
          y2={STRIP_TOP + STRIP_H}
          class="retention__peak"
        />

        {#if hoverPt}
          <line
            x1={hoverPt[0]}
            y1={PLOT_TOP}
            x2={hoverPt[0]}
            y2={STRIP_TOP + STRIP_H}
            class="retention__cursor"
          />
          <circle cx={hoverPt[0]} cy={hoverPt[1]} r="5" class="retention__dot" />
        {/if}
      </svg>

      <span class="retention__pill" data-edge={peakEdge} style="left: {peakFrac * 100}%">
        Most replayed
      </span>

      {#if hoverData && hoverPt}
        <div
          class="retention__tip"
          data-edge={hoverPt[0] / W > 0.7 ? "right" : hoverPt[0] / W < 0.3 ? "left" : "mid"}
          style="left: {(hoverPt[0] / W) * 100}%"
        >
          <span class="retention__tip-time">{tlabel((hover ?? 0) * bucketWidthS)}</span>
          <span class="retention__tip-row">
            <span class="retention__tip-dot retention__tip-dot--curve"></span>
            {Math.round(hoverData.retention * 100)}% still watching
          </span>
          <span class="retention__tip-row">
            <span class="retention__tip-dot retention__tip-dot--density"></span>
            {Math.round((hoverData.density / model.maxDensity) * 100)}% of peak replay
          </span>
        </div>
      {/if}
    </div>

    <div class="retention__axis">
      <span>0:00</span>
      <span>{tlabel(assetDurationS / 2)}</span>
      <span>{tlabel(assetDurationS)}</span>
    </div>

    <div class="retention__legend">
      <span><i class="retention__swatch retention__swatch--curve"></i> audience retention</span>
      <span
        ><i class="retention__swatch retention__swatch--density"></i> most replayed (watch density)</span
      >
    </div>
  </div>
{/if}

<style>
  .retention {
    display: flex;
    flex-direction: column;
    gap: 0.45rem;
  }
  .retention__empty {
    padding: 2rem 0;
    text-align: center;
    font-size: 0.85rem;
    color: hsl(var(--tn-fg-dark));
  }
  .retention__plot {
    position: relative;
    width: 100%;
  }
  .retention__svg {
    display: block;
    width: 100%;
    height: auto;
    overflow: visible;
  }
  .retention__grid {
    stroke: hsl(var(--tn-fg-gutter) / 0.4);
    stroke-width: 1;
    stroke-dasharray: 3 5;
  }
  .retention__line {
    stroke: hsl(var(--tn-cyan));
    stroke-width: 2.5;
    vector-effect: non-scaling-stroke;
  }
  .retention__density {
    fill: hsl(var(--tn-orange));
  }
  .retention__peak {
    stroke: hsl(var(--tn-orange) / 0.5);
    stroke-width: 1.5;
    stroke-dasharray: 3 4;
    vector-effect: non-scaling-stroke;
  }
  .retention__cursor {
    stroke: hsl(var(--tn-fg) / 0.6);
    stroke-width: 1;
    vector-effect: non-scaling-stroke;
  }
  .retention__dot {
    fill: hsl(var(--tn-cyan));
    stroke: hsl(var(--tn-bg));
    stroke-width: 2;
  }
  .retention__pill {
    position: absolute;
    top: -0.2rem;
    transform: translateX(-50%);
    padding: 0.1rem 0.5rem;
    border-radius: 999px;
    font-size: 0.66rem;
    font-weight: 600;
    white-space: nowrap;
    color: hsl(var(--tn-orange));
    background: hsl(var(--tn-orange) / 0.16);
    border: 1px solid hsl(var(--tn-orange) / 0.4);
    pointer-events: none;
  }
  .retention__pill[data-edge="left"] {
    transform: translateX(0);
  }
  .retention__pill[data-edge="right"] {
    transform: translateX(-100%);
  }
  .retention__tip {
    position: absolute;
    top: 0.5rem;
    transform: translateX(-50%);
    display: flex;
    flex-direction: column;
    gap: 0.15rem;
    padding: 0.5rem 0.65rem;
    border-radius: 0.55rem;
    background: hsl(var(--tn-bg-highlight));
    border: 1px solid hsl(var(--tn-blue) / 0.32);
    box-shadow: 0 10px 22px hsl(var(--tn-bg-dark) / 0.5);
    pointer-events: none;
    white-space: nowrap;
    z-index: 2;
  }
  .retention__tip[data-edge="left"] {
    transform: translateX(-12%);
  }
  .retention__tip[data-edge="right"] {
    transform: translateX(-88%);
  }
  .retention__tip-time {
    font-size: 0.74rem;
    font-weight: 700;
    color: hsl(var(--tn-fg));
  }
  .retention__tip-row {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    font-size: 0.72rem;
    color: hsl(var(--tn-fg-dark));
  }
  .retention__tip-dot {
    width: 0.5rem;
    height: 0.5rem;
    border-radius: 999px;
  }
  .retention__tip-dot--curve {
    background: hsl(var(--tn-cyan));
  }
  .retention__tip-dot--density {
    background: hsl(var(--tn-orange));
  }
  .retention__axis,
  .retention__legend {
    display: flex;
    font-size: 0.68rem;
    color: hsl(var(--tn-fg-dark));
  }
  .retention__axis {
    justify-content: space-between;
  }
  .retention__legend {
    gap: 1.1rem;
    margin-top: 0.1rem;
  }
  .retention__legend span {
    display: inline-flex;
    align-items: center;
    gap: 0.35rem;
  }
  .retention__swatch {
    width: 0.7rem;
    height: 0.7rem;
    border-radius: 2px;
  }
  .retention__swatch--curve {
    background: hsl(var(--tn-cyan));
  }
  .retention__swatch--density {
    background: hsl(var(--tn-orange));
  }
</style>
