<script lang="ts">
  import { onMount, onDestroy, untrack } from "svelte";
  import {
    Chart,
    BarController,
    BarElement,
    CategoryScale,
    LinearScale,
    Tooltip,
    Legend,
    type ChartConfiguration,
  } from "chart.js";

  // Register Chart.js components
  Chart.register(BarController, BarElement, CategoryScale, LinearScale, Tooltip, Legend);

  interface StorageData {
    dvrBytes: number;
    clipBytes: number;
    vodBytes: number;
    totalBytes: number;
  }

  interface Props {
    data: StorageData | null;
    height?: number;
  }

  let { data = null, height = 180 }: Props = $props();

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
    if (!canvas || !data) return;

    // Destroy existing chart
    if (chart) {
      chart.destroy();
    }

    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    // Skip if no data
    if (data.totalBytes === 0) return;

    const config: ChartConfiguration = {
      type: "bar",
      data: {
        labels: ["DVR", "Clips", "VOD"],
        datasets: [
          {
            label: "Storage",
            data: [data.dvrBytes, data.clipBytes, data.vodBytes],
            backgroundColor: [
              "rgba(59, 130, 246, 0.8)", // primary blue (DVR)
              "rgba(168, 85, 247, 0.8)", // accent purple (Clips)
              "rgba(34, 197, 94, 0.8)", // success green (VOD)
            ],
            borderColor: ["rgb(59, 130, 246)", "rgb(168, 85, 247)", "rgb(34, 197, 94)"],
            borderWidth: 1,
            borderRadius: 4,
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        indexAxis: "y",
        plugins: {
          legend: {
            display: false,
          },
          tooltip: {
            backgroundColor: "rgb(30, 41, 59)",
            titleColor: "rgb(226, 232, 240)",
            bodyColor: "rgb(148, 163, 184)",
            borderColor: "rgb(51, 65, 85)",
            borderWidth: 1,
            padding: 12,
            callbacks: {
              label: (context) => {
                const bytes = context.parsed.x ?? 0;
                const total = data?.totalBytes ?? 1;
                const percentage = total > 0 ? ((bytes / total) * 100).toFixed(1) : "0";
                return `${formatBytes(bytes)} (${percentage}%)`;
              },
            },
          },
        },
        scales: {
          x: {
            display: true,
            grid: {
              color: "rgba(148, 163, 184, 0.1)",
            },
            ticks: {
              color: "rgb(148, 163, 184)",
              callback: (value) => formatBytes(value as number),
            },
            border: {
              display: false,
            },
          },
          y: {
            display: true,
            grid: {
              display: false,
            },
            ticks: {
              color: "rgb(148, 163, 184)",
              font: {
                size: 11,
              },
            },
            border: {
              display: false,
            },
          },
        },
      },
    };

    chart = new Chart(ctx, config);
  };

  // Recreate chart when data changes
  $effect(() => {
    // Read data dependency to trigger effect when data changes
    const currentData = data;
    if (currentData) {
      // Use untrack to avoid reactive loop when setting chart state
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

  const hasData = $derived(data && data.totalBytes > 0);
</script>

<div class="chart-container" style="height: {height}px;">
  {#if hasData}
    <canvas bind:this={canvas}></canvas>
  {:else}
    <div class="flex items-center justify-center h-full text-muted-foreground text-sm">
      No storage data available
    </div>
  {/if}
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
