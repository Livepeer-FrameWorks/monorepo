<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import {
    Chart,
    DoughnutController,
    ArcElement,
    BarController,
    BarElement,
    CategoryScale,
    LinearScale,
    Tooltip,
    Legend,
    type ChartConfiguration,
  } from "chart.js";
  import { palette, seriesColors, tooltipTheme, gridColor } from "./theme";

  // One categorical-breakdown chart for the app: doughnut (codec, country, quality
  // tiers), horizontal/vertical bar (storage), and histogram (buffer health bins).
  // Replaces the per-metric breakdown components. Pure presentational.
  export interface BreakdownItem {
    label: string;
    value: number;
    color?: string;
  }

  interface Props {
    mode?: "doughnut" | "bar" | "histogram";
    items?: BreakdownItem[];
    values?: number[]; // histogram raw values
    horizontal?: boolean; // bar orientation
    height?: number;
    unit?: string; // histogram tooltip noun, e.g. "sessions"
    format?: "number" | "bytes" | "minutes";
    valueFormat?: (v: number) => string;
    legendPercent?: boolean; // doughnut legend shows share %
    binCount?: number;
    range?: [number, number];
    emptyText?: string;
  }

  let {
    mode = "doughnut",
    items = [],
    values = [],
    horizontal = false,
    height = 220,
    unit = "items",
    format = "number",
    valueFormat,
    legendPercent = true,
    binCount = 10,
    range = [0, 100],
    emptyText = "No data available",
  }: Props = $props();

  function fmtBytes(b: number): string {
    if (b === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(b) / Math.log(k));
    return parseFloat((b / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
  }
  function fmtMinutes(v: number): string {
    const h = Math.floor(v / 60);
    const m = Math.round(v % 60);
    return h > 0 ? `${h}h ${m}m` : `${m}m`;
  }
  const fmt = $derived(
    valueFormat ??
      (format === "bytes"
        ? fmtBytes
        : format === "minutes"
          ? fmtMinutes
          : (v: number) => v.toLocaleString())
  );

  let canvas = $state<HTMLCanvasElement>();
  let chart: Chart | null = null;

  const hasData = $derived(
    mode === "histogram" ? values.length > 0 : items.some((i) => i.value > 0)
  );

  const createChart = () => {
    if (!canvas || !hasData) return;
    if (chart) chart.destroy();
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    Chart.register(
      DoughnutController,
      ArcElement,
      BarController,
      BarElement,
      CategoryScale,
      LinearScale,
      Tooltip,
      Legend
    );

    let config: ChartConfiguration;

    if (mode === "histogram") {
      const [lo, hi] = range;
      const width = (hi - lo) / binCount;
      const bins = new Array(binCount).fill(0);
      const labels = bins.map(
        (_, i) => `${Math.round(lo + i * width)}-${Math.round(lo + (i + 1) * width)}%`
      );
      for (const v of values) {
        const clamped = Math.max(lo, Math.min(hi, v));
        bins[Math.min(Math.floor((clamped - lo) / width), binCount - 1)]++;
      }
      // Red (low) to green (high) by bin position.
      const colorAt = (i: number) =>
        i < binCount * 0.4 ? palette.red : i < binCount * 0.7 ? palette.yellow : palette.green;
      config = {
        type: "bar",
        data: {
          labels,
          datasets: [
            {
              data: bins,
              backgroundColor: bins.map((_, i) => colorAt(i)),
              borderRadius: 4,
            },
          ],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          plugins: {
            legend: { display: false },
            tooltip: {
              ...tooltipTheme(),
              callbacks: {
                label: (c: { parsed: { y: number } }) => {
                  const count = Number(c.parsed.y ?? 0);
                  const total = values.length || 1;
                  return `${count} ${unit} (${((count / total) * 100).toFixed(1)}%)`;
                },
              },
            },
          },
          scales: {
            x: {
              grid: { display: false },
              ticks: { color: palette.fgDark, font: { size: 11 } },
              border: { display: false },
            },
            y: {
              beginAtZero: true,
              grid: { color: gridColor },
              ticks: { color: palette.fgDark, precision: 0 },
              border: { display: false },
            },
          },
        },
      } as unknown as ChartConfiguration;
    } else {
      const filtered = items.filter((i) => i.value > 0);
      const total = filtered.reduce((s, i) => s + i.value, 0) || 1;
      const colors = filtered.map((i, idx) => i.color ?? seriesColors[idx % seriesColors.length]);

      if (mode === "bar") {
        config = {
          type: "bar",
          data: {
            labels: filtered.map((i) => i.label),
            datasets: [
              { data: filtered.map((i) => i.value), backgroundColor: colors, borderRadius: 4 },
            ],
          },
          options: {
            responsive: true,
            maintainAspectRatio: false,
            indexAxis: horizontal ? "y" : "x",
            plugins: {
              legend: { display: false },
              tooltip: {
                ...tooltipTheme(),
                callbacks: {
                  label: (c: { parsed: { x: number; y: number } }) => {
                    const v = horizontal ? c.parsed.x : c.parsed.y;
                    return `${fmt(v)} (${((v / total) * 100).toFixed(1)}%)`;
                  },
                },
              },
            },
            scales: {
              x: {
                grid: { display: !horizontal ? false : true, color: gridColor },
                ticks: {
                  color: palette.fgDark,
                  font: { size: 11 },
                  callback: (v: number | string) => (horizontal ? fmt(Number(v)) : v),
                },
                border: { display: false },
              },
              y: {
                grid: { display: horizontal ? false : true, color: gridColor },
                ticks: {
                  color: palette.fgDark,
                  font: { size: 11 },
                  callback: (v: number | string) => (horizontal ? v : fmt(Number(v))),
                },
                border: { display: false },
              },
            },
          },
        } as unknown as ChartConfiguration;
      } else {
        config = {
          type: "doughnut",
          data: {
            labels: filtered.map((i) => i.label),
            datasets: [
              {
                data: filtered.map((i) => i.value),
                backgroundColor: colors,
                borderColor: palette.surfaceDark,
                borderWidth: 2,
                hoverOffset: 4,
              },
            ],
          },
          options: {
            responsive: true,
            maintainAspectRatio: false,
            cutout: "60%",
            plugins: {
              legend: {
                display: true,
                position: "right",
                labels: {
                  color: palette.fgDark,
                  padding: 12,
                  usePointStyle: true,
                  pointStyle: "rect",
                  font: { size: 11 },
                  generateLabels: (ch: Chart) => {
                    const ds = ch.data.datasets[0];
                    const sum = (ds.data as number[]).reduce((a, b) => a + b, 0) || 1;
                    return (
                      ch.data.labels?.map((label, i) => {
                        const v = ds.data[i] as number;
                        const suffix = legendPercent ? `: ${((v / sum) * 100).toFixed(1)}%` : "";
                        return {
                          text: `${label}${suffix}`,
                          fillStyle: (ds.backgroundColor as string[])[i],
                          fontColor: palette.fgDark,
                          hidden: false,
                          index: i,
                        };
                      }) ?? []
                    );
                  },
                },
              },
              tooltip: {
                ...tooltipTheme(),
                callbacks: {
                  label: (c: { parsed: number; label: string }) =>
                    `${c.label}: ${fmt(c.parsed)} (${((c.parsed / total) * 100).toFixed(1)}%)`,
                },
              },
            },
          },
        } as unknown as ChartConfiguration;
      }
    }

    chart = new Chart(ctx, config);
  };

  $effect(() => {
    void items;
    void values;
    createChart();
  });
  onMount(() => createChart());
  onDestroy(() => {
    if (chart) chart.destroy();
  });
</script>

<div class="chart-container" style="height: {height}px;">
  {#if hasData}
    <canvas bind:this={canvas}></canvas>
  {:else}
    <div class="flex items-center justify-center h-full text-muted-foreground text-sm">
      {emptyText}
    </div>
  {/if}
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
