<script lang="ts">
  import { onMount, onDestroy } from "svelte";
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

  Chart.register(BarController, BarElement, CategoryScale, LinearScale, Tooltip, Legend);

  interface Props {
    // Array of buffer health percentages (0-100) from individual sessions/metrics
    data: number[];
    height?: number;
    title?: string;
  }

  let { data = [], height = 300, title = "Buffer Health Distribution" }: Props = $props();

  let canvas = $state<HTMLCanvasElement>();
  let chart = $state<Chart | null>(null);

  const createChart = () => {
    if (!canvas || !data.length) return;

    if (chart) chart.destroy();

    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    // Bucket data into bins: 0-10, 10-20, ... 90-100
    const bins = new Array(10).fill(0);
    const labels = [
      "0-10%",
      "10-20%",
      "20-30%",
      "30-40%",
      "40-50%",
      "50-60%",
      "60-70%",
      "70-80%",
      "80-90%",
      "90-100%",
    ];

    data.forEach((value) => {
      // Clamp value between 0 and 100
      const clamped = Math.max(0, Math.min(100, value));
      // Determine bin index (0 to 9)
      const index = Math.min(Math.floor(clamped / 10), 9);
      bins[index]++;
    });

    // Color gradient for bars (Red -> Green)
    const backgroundColors = bins.map((_, i) => {
      // 0-3 (bad/red), 4-6 (warning/yellow), 7-9 (good/green)
      if (i < 4) return "rgba(239, 68, 68, 0.7)"; // red
      if (i < 7) return "rgba(245, 158, 11, 0.7)"; // amber
      return "rgba(34, 197, 94, 0.7)"; // green
    });

    const borderColors = bins.map((_, i) => {
      if (i < 4) return "rgb(239, 68, 68)";
      if (i < 7) return "rgb(245, 158, 11)";
      return "rgb(34, 197, 94)";
    });

    const config: ChartConfiguration<"bar"> = {
      type: "bar",
      data: {
        labels: labels,
        datasets: [
          {
            label: "User Sessions",
            data: bins,
            backgroundColor: backgroundColors,
            borderColor: borderColors,
            borderWidth: 1,
            borderRadius: 4,
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        plugins: {
          title: {
            display: !!title,
            text: title,
            color: "rgb(148, 163, 184)",
            font: { size: 14, weight: "normal" },
          },
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
                const count = Number(context.parsed.y ?? 0);
                const total = data.length || 1;
                const percentage = ((count / total) * 100).toFixed(1);
                return `${count} sessions (${percentage}%)`;
              },
            },
          },
        },
        scales: {
          x: {
            grid: { display: false },
            ticks: { color: "rgb(148, 163, 184)", font: { size: 11 } },
            border: { display: false },
          },
          y: {
            beginAtZero: true,
            grid: { color: "rgba(148, 163, 184, 0.1)" },
            ticks: { color: "rgb(148, 163, 184)", precision: 0 },
            border: { display: false },
            title: {
              display: true,
              text: "Count",
              color: "rgb(148, 163, 184)",
            },
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
  {#if data.length > 0}
    <canvas bind:this={canvas}></canvas>
  {:else}
    <div class="flex items-center justify-center h-full text-muted-foreground text-sm">
      No buffer data available
    </div>
  {/if}
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
