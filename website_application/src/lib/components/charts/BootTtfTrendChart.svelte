<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import {
    Chart,
    LineController,
    LineElement,
    PointElement,
    LinearScale,
    TimeScale,
    Tooltip,
    Legend,
    Filler,
    type ChartConfiguration,
  } from "chart.js";
  import "chartjs-adapter-date-fns";

  Chart.register(
    LineController,
    LineElement,
    PointElement,
    LinearScale,
    TimeScale,
    Tooltip,
    Legend,
    Filler
  );

  interface BootBucket {
    timestamp: string;
    bootCount?: number | null;
    p50TtfMs?: number | null;
    p95TtfMs?: number | null;
    p99TtfMs?: number | null;
  }

  interface Props {
    data: BootBucket[];
    height?: number;
  }

  let { data = [], height = 280 }: Props = $props();

  let canvas: HTMLCanvasElement;
  let chart: Chart | null = null;

  const fmtMs = (v: number): string =>
    v >= 1000 ? `${(v / 1000).toFixed(2)}s` : `${Math.round(v)}ms`;

  const createChart = () => {
    if (!canvas || !data.length) return;
    if (chart) chart.destroy();
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    const sorted = [...data].sort(
      (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
    );
    // Per-bucket sample counts; surfaced in the tooltip so low-sample windows read
    // as low-confidence rather than as real spikes.
    const counts = sorted.map((d) => d.bootCount ?? 0);

    const series = [
      { label: "TTF p50", key: "p50TtfMs", color: "rgb(34, 197, 94)" },
      { label: "TTF p95", key: "p95TtfMs", color: "rgb(234, 179, 8)" },
      { label: "TTF p99", key: "p99TtfMs", color: "rgb(168, 85, 247)" },
    ] as const;

    const config: ChartConfiguration = {
      type: "line",
      data: {
        labels: sorted.map((d) => new Date(d.timestamp)),
        datasets: series.map((s, i) => ({
          label: s.label,
          data: sorted.map((d) => {
            const v = d[s.key];
            return v != null ? v : null;
          }),
          borderColor: s.color,
          backgroundColor: i === 0 ? "rgba(34, 197, 94, 0.08)" : "transparent",
          fill: i === 0,
          tension: 0.35,
          pointRadius: 1,
          pointHoverRadius: 4,
          borderWidth: 1.75,
          yAxisID: "y",
          spanGaps: true,
        })),
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        interaction: { intersect: false, mode: "index" },
        plugins: {
          tooltip: {
            backgroundColor: "rgb(30, 41, 59)",
            titleColor: "rgb(226, 232, 240)",
            bodyColor: "rgb(148, 163, 184)",
            borderColor: "rgb(51, 65, 85)",
            borderWidth: 1,
            padding: 12,
            callbacks: {
              title: (items) => {
                if (!items.length) return "";
                const x = items[0].parsed.x;
                return x == null ? "" : new Date(x).toLocaleString();
              },
              label: (c) => {
                const v = c.parsed.y;
                return v == null ? `${c.dataset.label}: N/A` : `${c.dataset.label}: ${fmtMs(v)}`;
              },
              afterBody: (items) => {
                if (!items.length) return "";
                const n = counts[items[0].dataIndex] ?? 0;
                return `${n} boot${n === 1 ? "" : "s"}`;
              },
            },
          },
          legend: {
            display: true,
            position: "top",
            labels: { color: "rgb(148, 163, 184)", usePointStyle: true, padding: 16 },
          },
        },
        scales: {
          x: {
            type: "time",
            grid: { display: false },
            ticks: { color: "rgb(148, 163, 184)", maxRotation: 0, maxTicksLimit: 8 },
            border: { display: false },
          },
          y: {
            type: "linear",
            position: "left",
            min: 0,
            grid: { color: "rgba(148, 163, 184, 0.1)" },
            ticks: { color: "rgb(148, 163, 184)", callback: (v) => fmtMs(Number(v)) },
            border: { display: false },
            title: { display: true, text: "Time to first frame", color: "rgb(148, 163, 184)" },
          },
        },
      },
    };

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
