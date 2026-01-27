<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
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

  // Register Chart.js components
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

  interface StorageTrendPoint {
    timestamp: string | Date;
    totalBytes: number;
    frozenBytes: number;
  }

  interface Props {
    data: StorageTrendPoint[];
    height?: number;
    title?: string;
  }

  let { data = [], height = 240, title = "Storage Usage Trend" }: Props = $props();

  let canvas = $state<HTMLCanvasElement>();
  let chart = $state<Chart | null>(null);

  function formatBytes(bytes: number): string {
    if (bytes === 0) return "0 B";
    const k = 1024;
    const sizes = ["B", "KB", "MB", "GB", "TB"];
    const i = Math.floor(Math.log(bytes) / Math.log(k));
    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + " " + sizes[i];
  }

  const createChart = () => {
    if (!canvas) return;

    // Destroy existing chart
    if (chart) {
      chart.destroy();
    }

    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    // Sort data by timestamp just in case
    const sortedData = [...data].sort(
      (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
    );

    const config: ChartConfiguration = {
      type: "line",
      data: {
        labels: sortedData.map((d) => d.timestamp),
        datasets: [
          {
            label: "Total Storage",
            data: sortedData.map((d) => d.totalBytes),
            borderColor: "rgb(59, 130, 246)", // Primary Blue
            backgroundColor: "rgba(59, 130, 246, 0.1)",
            borderWidth: 2,
            tension: 0.4,
            fill: true,
            pointRadius: 0,
            pointHoverRadius: 4,
          },
          {
            label: "Frozen (Cold) Storage",
            data: sortedData.map((d) => d.frozenBytes),
            borderColor: "rgb(96, 165, 250)", // Light Blue / Ice
            backgroundColor: "rgba(96, 165, 250, 0.2)",
            borderWidth: 2,
            borderDash: [5, 5],
            tension: 0.4,
            fill: true,
            pointRadius: 0,
            pointHoverRadius: 4,
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
          legend: {
            display: true,
            labels: {
              color: "rgb(148, 163, 184)",
              font: {
                size: 11,
              },
            },
          },
          tooltip: {
            mode: "index",
            intersect: false,
            backgroundColor: "rgb(30, 41, 59)",
            titleColor: "rgb(226, 232, 240)",
            bodyColor: "rgb(148, 163, 184)",
            borderColor: "rgb(51, 65, 85)",
            borderWidth: 1,
            padding: 12,
            callbacks: {
              label: (context) => {
                let label = context.dataset.label || "";
                if (label) {
                  label += ": ";
                }
                if (context.parsed.y !== null) {
                  label += formatBytes(context.parsed.y);
                }
                return label;
              },
            },
          },
          title: {
            display: !!title,
            text: title,
            color: "rgb(148, 163, 184)",
            align: "start",
            font: {
              size: 13,
              weight: "normal",
            },
            padding: {
              bottom: 20,
            },
          },
        },
        scales: {
          x: {
            type: "time",
            time: {
              unit: "day",
              tooltipFormat: "PP pp",
            },
            grid: {
              color: "rgba(148, 163, 184, 0.05)",
              display: false,
            },
            ticks: {
              color: "rgb(148, 163, 184)",
              font: {
                size: 10,
              },
              maxRotation: 0,
            },
            border: {
              display: false,
            },
          },
          y: {
            grid: {
              color: "rgba(148, 163, 184, 0.1)",
            },
            ticks: {
              color: "rgb(148, 163, 184)",
              font: {
                size: 10,
              },
              callback: (value) => formatBytes(value as number),
            },
            border: {
              display: false,
            },
            beginAtZero: true,
          },
        },
        interaction: {
          mode: "nearest",
          axis: "x",
          intersect: false,
        },
      },
    };

    chart = new Chart(ctx, config);
  };

  $effect(() => {
    const currentData = data;
    if (currentData) {
      untrack(() => createChart());
    }
  });

  onMount(() => {
    createChart();
  });

  onDestroy(() => {
    if (chart) {
      chart.destroy();
    }
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
