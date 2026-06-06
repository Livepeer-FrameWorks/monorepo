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

  interface QoeBucket {
    timestamp: string;
    sessionCount?: number | null;
    playedHours?: number | null;
    rebufferingRatio?: number | null;
    frameDropRatio?: number | null;
    avgBitrateBps?: number | null;
  }

  interface Props {
    data: QoeBucket[];
    height?: number;
  }

  let { data = [], height = 280 }: Props = $props();

  let canvas: HTMLCanvasElement;
  let chart: Chart | null = null;

  const createChart = () => {
    if (!canvas || !data.length) return;
    if (chart) chart.destroy();
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    const sorted = [...data].sort(
      (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
    );
    const counts = sorted.map((d) => d.sessionCount ?? 0);

    const config: ChartConfiguration = {
      type: "line",
      data: {
        labels: sorted.map((d) => new Date(d.timestamp)),
        datasets: [
          {
            label: "Rebuffering ratio (%)",
            data: sorted.map((d) => (d.rebufferingRatio != null ? d.rebufferingRatio * 100 : null)),
            borderColor: "rgb(59, 130, 246)",
            backgroundColor: "rgba(59, 130, 246, 0.08)",
            fill: true,
            tension: 0.35,
            pointRadius: 1,
            pointHoverRadius: 4,
            borderWidth: 1.75,
            yAxisID: "y",
            spanGaps: true,
          },
          {
            label: "Frame drop ratio (%)",
            data: sorted.map((d) => (d.frameDropRatio != null ? d.frameDropRatio * 100 : null)),
            borderColor: "rgb(239, 68, 68)",
            backgroundColor: "transparent",
            fill: false,
            tension: 0.35,
            pointRadius: 1,
            pointHoverRadius: 4,
            borderWidth: 1.75,
            yAxisID: "y",
            spanGaps: true,
          },
          {
            label: "Avg bitrate (Mbps)",
            data: sorted.map((d) => (d.avgBitrateBps != null ? d.avgBitrateBps / 1_000_000 : null)),
            borderColor: "rgb(34, 211, 238)",
            backgroundColor: "transparent",
            fill: false,
            tension: 0.35,
            pointRadius: 1,
            pointHoverRadius: 4,
            borderWidth: 1.5,
            yAxisID: "y1",
            spanGaps: true,
          },
        ],
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
                if (v == null) return `${c.dataset.label}: N/A`;
                if (c.dataset.label?.includes("Mbps")) return `${c.dataset.label}: ${v.toFixed(2)}`;
                return `${c.dataset.label}: ${v.toFixed(3)}%`;
              },
              afterBody: (items) => {
                if (!items.length) return "";
                const n = counts[items[0].dataIndex] ?? 0;
                return `${n} session${n === 1 ? "" : "s"}`;
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
            ticks: { color: "rgb(148, 163, 184)", callback: (v) => `${v}%` },
            border: { display: false },
            title: { display: true, text: "Rebuffer / frame-drop %", color: "rgb(148, 163, 184)" },
          },
          y1: {
            type: "linear",
            position: "right",
            min: 0,
            grid: { drawOnChartArea: false },
            ticks: { color: "rgb(34, 211, 238)" },
            border: { display: false },
            title: { display: true, text: "Bitrate (Mbps)", color: "rgb(34, 211, 238)" },
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
