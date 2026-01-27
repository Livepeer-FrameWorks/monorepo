<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import {
    Chart,
    DoughnutController,
    ArcElement,
    Tooltip,
    Legend,
    type ChartConfiguration,
  } from "chart.js";

  // Register Chart.js components
  Chart.register(DoughnutController, ArcElement, Tooltip, Legend);

  interface QualityTierData {
    tier2160pMinutes: number;
    tier1440pMinutes: number;
    tier1080pMinutes: number;
    tier720pMinutes: number;
    tier480pMinutes: number;
    tierSdMinutes: number;
  }

  interface Props {
    data: QualityTierData | null;
    height?: number;
  }

  let { data = null, height = 200 }: Props = $props();

  let canvas = $state<HTMLCanvasElement>();
  let chart: Chart | null = null;

  const tierColors = {
    "2160p": "rgb(14, 165, 233)", // sky
    "1440p": "rgb(34, 197, 94)", // green
    "1080p": "rgb(59, 130, 246)", // blue
    "720p": "rgb(234, 179, 8)", // yellow
    "480p": "rgb(249, 115, 22)", // orange
    SD: "rgb(148, 163, 184)", // muted gray
  };

  const createChart = () => {
    if (!canvas || !data) return;

    // Destroy existing chart
    if (chart) {
      chart.destroy();
    }

    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    const totalMinutes =
      data.tier2160pMinutes +
      data.tier1440pMinutes +
      data.tier1080pMinutes +
      data.tier720pMinutes +
      data.tier480pMinutes +
      data.tierSdMinutes;

    // Skip if no data
    if (totalMinutes === 0) return;

    const config: ChartConfiguration<"doughnut", number[], string> = {
      type: "doughnut",
      data: {
        labels: ["2160p", "1440p", "1080p", "720p", "480p", "SD"],
        datasets: [
          {
            data: [
              data.tier2160pMinutes,
              data.tier1440pMinutes,
              data.tier1080pMinutes,
              data.tier720pMinutes,
              data.tier480pMinutes,
              data.tierSdMinutes,
            ],
            backgroundColor: [
              tierColors["2160p"],
              tierColors["1440p"],
              tierColors["1080p"],
              tierColors["720p"],
              tierColors["480p"],
              tierColors["SD"],
            ],
            borderColor: "rgb(15, 23, 42)",
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
              color: "rgb(148, 163, 184)",
              padding: 12,
              usePointStyle: true,
              pointStyle: "rect",
              font: {
                size: 11,
              },
              generateLabels: (chart: Chart<"doughnut">) => {
                const dataset = chart.data.datasets[0];
                const total = (dataset.data as number[]).reduce((a, b) => a + b, 0);
                return (
                  chart.data.labels?.map((label: unknown, i: number) => {
                    const value = dataset.data[i] as number;
                    const percentage = total > 0 ? ((value / total) * 100).toFixed(1) : "0";
                    return {
                      text: `${label}: ${percentage}%`,
                      fillStyle: (dataset.backgroundColor as string[])[i],
                      fontColor: "rgb(148, 163, 184)",
                      hidden: false,
                      index: i,
                    };
                  }) ?? []
                );
              },
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
              label: (context: { parsed: number; dataset: { data: number[] }; label: string }) => {
                const value = context.parsed;
                const total = (context.dataset.data as number[]).reduce(
                  (a: number, b: number) => a + b,
                  0
                );
                const percentage = ((value / total) * 100).toFixed(1);
                const hours = Math.floor(value / 60);
                const mins = value % 60;
                const timeStr = hours > 0 ? `${hours}h ${mins}m` : `${mins}m`;
                return `${context.label}: ${timeStr} (${percentage}%)`;
              },
            },
          },
        },
      },
    };

    chart = new Chart(ctx, config);
  };

  // Recreate chart when data changes
  $effect(() => {
    if (data) {
      createChart();
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

  const hasData = $derived(
    data &&
      (data.tier2160pMinutes > 0 ||
        data.tier1440pMinutes > 0 ||
        data.tier1080pMinutes > 0 ||
        data.tier720pMinutes > 0 ||
        data.tier480pMinutes > 0 ||
        data.tierSdMinutes > 0)
  );
</script>

<div class="chart-container" style="height: {height}px;">
  {#if hasData}
    <canvas bind:this={canvas}></canvas>
  {:else}
    <div class="flex items-center justify-center h-full text-muted-foreground text-sm">
      No quality tier data available
    </div>
  {/if}
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
