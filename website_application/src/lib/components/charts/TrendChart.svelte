<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import {
    Chart,
    LineController,
    LineElement,
    PointElement,
    LinearScale,
    TimeScale,
    Title,
    Tooltip,
    Legend,
    Filler,
    type ChartConfiguration,
  } from "chart.js";
  import "chartjs-adapter-date-fns";
  import { SvelteMap } from "svelte/reactivity";
  import {
    palette,
    alpha,
    tooltipTheme,
    legendTheme,
    timeScale,
    linearAxis,
    seriesColors,
    type TrendSeries,
    type AxisCfg,
  } from "./theme";

  // One configurable time-series line chart for every trend in the app: QoE,
  // boot TTF percentiles, viewer counts, usage, country trends, health. Replaces
  // the per-metric chart components. Pure presentational: the page owns the query
  // and passes rows + a series config. Up to three value axes (y / y1 / y2).
  interface Props {
    data: object[];
    series?: TrendSeries[];
    // Pivot long rows ({ ts, <key>, <valueKey> }) into one line per top-N key.
    pivot?: {
      key: string;
      valueKey: string;
      max?: number;
      unit?: string;
      nameFn?: (k: string) => string;
    };
    xKey?: string;
    height?: number;
    mini?: boolean;
    axes?: { y?: AxisCfg; y1?: AxisCfg; y2?: AxisCfg };
    leftTitle?: string; // shorthand for axes.y.title
    rightTitle?: string; // shorthand for axes.y1.title
    maxTicks?: number;
    sampleKey?: string; // per-bucket count field, shown as tooltip footer (confidence)
    sampleNoun?: string;
  }

  let {
    data = [],
    series = [],
    pivot,
    xKey = "timestamp",
    height = 280,
    mini = false,
    axes,
    leftTitle,
    rightTitle,
    maxTicks = 7,
    sampleKey,
    sampleNoun = "sample",
  }: Props = $props();

  Chart.register(
    LineController,
    LineElement,
    PointElement,
    LinearScale,
    TimeScale,
    Title,
    Tooltip,
    Legend,
    Filler
  );

  let canvas = $state<HTMLCanvasElement>();
  let chart: Chart | null = null;

  const AXIS_DEFAULTS: Record<
    string,
    { position: "left" | "right"; color: string; grid: boolean }
  > = {
    y: { position: "left", color: palette.fgDark, grid: true },
    y1: { position: "right", color: palette.cyan, grid: false },
    y2: { position: "right", color: palette.red, grid: false },
  };

  const createChart = () => {
    if (!canvas || !data.length) return;
    if (chart) chart.destroy();
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    const rows = data as Record<string, unknown>[];

    // Pivot mode: long rows become wide rows with one generated series per top-N
    // pivot value (e.g. one line per country, ranked by total).
    let activeRows = rows;
    let activeSeries = series;
    if (pivot) {
      const totals = new SvelteMap<string, number>();
      for (const r of rows) {
        const k = String(r[pivot.key] ?? "");
        totals.set(k, (totals.get(k) ?? 0) + Number(r[pivot.valueKey] ?? 0));
      }
      const top = [...totals.entries()]
        .sort((a, b) => b[1] - a[1])
        .slice(0, pivot.max ?? 6)
        .map(([k]) => k);
      const byTs = new SvelteMap<string, Record<string, unknown>>();
      for (const r of rows) {
        const ts = String(r[xKey]);
        if (!byTs.has(ts)) byTs.set(ts, { [xKey]: r[xKey] });
        const k = String(r[pivot.key] ?? "");
        if (top.includes(k)) byTs.get(ts)![k] = Number(r[pivot.valueKey] ?? 0);
      }
      activeRows = [...byTs.values()];
      activeSeries = top.map((k, i) => ({
        key: k,
        label: pivot.nameFn ? pivot.nameFn(k) : k,
        color: seriesColors[i % seriesColors.length],
        format: (v: number) => `${v.toLocaleString()}${pivot.unit ?? ""}`,
      }));
    }
    if (!activeSeries.length) return;

    const sorted = [...activeRows].sort(
      (a, b) => new Date(a[xKey] as string).getTime() - new Date(b[xKey] as string).getTime()
    );
    const samples = sampleKey ? sorted.map((d) => Number(d[sampleKey] ?? 0)) : null;

    const datasets = activeSeries.map((s) => {
      const color = s.color ?? palette.blue;
      return {
        label: s.label,
        data: sorted.map((d) => {
          const v = d[s.key];
          return v == null ? null : Number(v) * (s.scale ?? 1);
        }),
        borderColor: color,
        backgroundColor: s.filled ? alpha(color, 0.12) : "transparent",
        fill: !!s.filled,
        borderDash: s.dashed ? [5, 4] : undefined,
        tension: 0.35,
        pointRadius: 0,
        pointHoverRadius: mini ? 3 : 4,
        borderWidth: mini ? 1.5 : 1.75,
        yAxisID: s.axis ?? "y",
        spanGaps: true,
      };
    });

    // Build only the axes referenced by the series (y always present).
    const merged: Record<string, AxisCfg> = {
      y: { title: leftTitle, ...(axes?.y ?? {}) },
      y1: { title: rightTitle, ...(axes?.y1 ?? {}) },
      y2: { ...(axes?.y2 ?? {}) },
    };
    const used = new Set<string>(["y", ...activeSeries.map((s) => s.axis ?? "y")]);
    const scales: Record<string, unknown> = { x: { ...timeScale(maxTicks), display: !mini } };
    for (const id of ["y", "y1", "y2"]) {
      if (!used.has(id)) continue;
      const def = AXIS_DEFAULTS[id];
      const cfg = merged[id];
      scales[id] = {
        ...linearAxis({
          position: def.position,
          grid: def.grid,
          color: cfg.color ?? def.color,
          title: cfg.title,
          max: cfg.max,
          tickFormat: cfg.tickFormat,
        }),
        display: !mini,
      };
    }

    const config = {
      type: "line",
      data: { labels: sorted.map((d) => new Date(d[xKey] as string)), datasets },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        interaction: { intersect: false, mode: "index" },
        plugins: {
          legend: mini ? { display: false } : legendTheme(),
          tooltip: {
            enabled: !mini,
            ...tooltipTheme(),
            callbacks: {
              title: (items: { parsed: { x: number | null } }[]) =>
                items.length && items[0].parsed.x != null
                  ? new Date(items[0].parsed.x).toLocaleString()
                  : "",
              label: (c: { datasetIndex: number; parsed: { y: number | null } }) => {
                const s = activeSeries[c.datasetIndex];
                const v = c.parsed.y;
                if (v == null) return `${s.label}: N/A`;
                if (s.format) return `${s.label}: ${s.format(v)}`;
                return `${s.label}: ${v.toFixed(s.digits ?? 2)}${s.unit ?? ""}`;
              },
              afterBody: (items: { dataIndex: number }[]) => {
                if (!samples || !items.length) return "";
                const n = samples[items[0].dataIndex] ?? 0;
                return `${n.toLocaleString()} ${sampleNoun}${n === 1 ? "" : "s"}`;
              },
            },
          },
        },
        scales,
      },
    } as unknown as ChartConfiguration;

    chart = new Chart(ctx, config);
  };

  $effect(() => {
    if (data) createChart();
  });
  onMount(() => createChart());
  onDestroy(() => {
    if (chart) chart.destroy();
  });
</script>

<div class="chart-container" style="height: {height}px;">
  <canvas bind:this={canvas}></canvas>
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
