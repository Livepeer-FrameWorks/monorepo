<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import {
    Chart,
    PieController,
    ArcElement,
    Tooltip,
    Legend,
    type ChartConfiguration,
  } from "chart.js";

  Chart.register(PieController, ArcElement, Tooltip, Legend);

  interface CodecData {
    codec: string;
    minutes: number;
  }

  interface Props {
    data: CodecData[];
    height?: number;
    title?: string;
  }

  let { data = [], height = 300, title = "Codec Distribution" }: Props = $props();

  let canvas = $state<HTMLCanvasElement>();
  let chart: Chart | null = null;

  const createChart = () => {
    if (!canvas || !data.length) return;

    if (chart) chart.destroy();

    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    // Filter out zero values
    const filteredData = data.filter((d) => d.minutes > 0);
    const totalMinutes = filteredData.reduce((acc, d) => acc + d.minutes, 0);

    const config: ChartConfiguration<"pie"> = {
      type: "pie",
      data: {
        labels: filteredData.map((d) => d.codec),
        datasets: [
          {
            data: filteredData.map((d) => d.minutes),
            backgroundColor: [
              "rgb(59, 130, 246)", // H.264 (Blue)
              "rgb(168, 85, 247)", // H.265 (Purple)
              "rgb(34, 197, 94)", // VP9 (Green)
              "rgb(245, 158, 11)", // AV1 (Amber)
              "rgb(239, 68, 68)", // Other (Red)
            ],
            borderColor: "rgb(30, 41, 59)", // Dark border for segments
            borderWidth: 2,
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
            position: "right",
            labels: {
              color: "rgb(226, 232, 240)",
              usePointStyle: true,
              padding: 20,
            },
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
                const label = context.label || "";
                const value = context.parsed;
                const percentage = totalMinutes > 0 ? ((value / totalMinutes) * 100).toFixed(1) : 0;
                return `${label}: ${percentage}% (${value.toLocaleString()} min)`;
              },
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
  {#if data.length > 0 && data.some((d) => d.minutes > 0)}
    <canvas bind:this={canvas}></canvas>
  {:else}
    <div class="flex items-center justify-center h-full text-muted-foreground text-sm">
      No codec data available
    </div>
  {/if}
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
