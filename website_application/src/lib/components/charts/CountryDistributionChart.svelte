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
  import { getCountryName } from "$lib/utils/country-names";

  // Register Chart.js components
  Chart.register(DoughnutController, ArcElement, Tooltip, Legend);

  interface CountryData {
    countryCode: string;
    viewerCount: number;
    percentage: number;
  }

  interface Props {
    data: CountryData[];
    height?: number;
    title?: string;
    maxItems?: number;
  }

  let { data = [], height = 300, title = "", maxItems = 8 }: Props = $props();

  let canvas: HTMLCanvasElement | null = null;
  let chart: Chart | null = null;

  // Color palette for countries
  const colors = [
    "rgb(59, 130, 246)", // blue
    "rgb(34, 197, 94)", // green
    "rgb(251, 146, 60)", // orange
    "rgb(168, 85, 247)", // purple
    "rgb(236, 72, 153)", // pink
    "rgb(20, 184, 166)", // teal
    "rgb(245, 158, 11)", // amber
    "rgb(239, 68, 68)", // red
    "rgb(107, 114, 128)", // gray (for "Other")
  ];

  const createChart = () => {
    if (!canvas || !data.length) return;

    // Destroy existing chart
    if (chart) {
      chart.destroy();
    }

    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    // Sort by viewer count and take top N
    const sortedData = [...data].sort((a, b) => b.viewerCount - a.viewerCount);
    let chartData = sortedData.slice(0, maxItems);

    // Group remaining as "Other" if there are more items
    if (sortedData.length > maxItems) {
      const otherCount = sortedData
        .slice(maxItems)
        .reduce((sum, d) => sum + d.viewerCount, 0);
      const otherPercentage = sortedData
        .slice(maxItems)
        .reduce((sum, d) => sum + d.percentage, 0);
      if (otherCount > 0) {
        chartData.push({
          countryCode: "Other",
          viewerCount: otherCount,
          percentage: otherPercentage,
        });
      }
    }

    const config: ChartConfiguration<"doughnut"> = {
      type: "doughnut",
      data: {
        labels: chartData.map((d) => d.countryCode === "Other" ? "Other" : getCountryName(d.countryCode)),
        datasets: [
          {
            data: chartData.map((d) => d.viewerCount),
            backgroundColor: colors.slice(0, chartData.length),
            borderColor: "rgb(30, 41, 59)",
            borderWidth: 2,
            hoverOffset: 8,
          },
        ],
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        cutout: "60%",
        plugins: {
          title: {
            display: !!title,
            text: title,
            color: "rgb(148, 163, 184)",
            font: {
              size: 14,
              weight: "normal",
            },
          },
          tooltip: {
            enabled: true,
            backgroundColor: "rgb(30, 41, 59)",
            titleColor: "rgb(226, 232, 240)",
            bodyColor: "rgb(148, 163, 184)",
            borderColor: "rgb(51, 65, 85)",
            borderWidth: 1,
            padding: 12,
            displayColors: true,
            callbacks: {
              label: (context) => {
                const item = chartData[context.dataIndex];
                return `${item.viewerCount.toLocaleString()} viewers (${item.percentage.toFixed(1)}%)`;
              },
            },
          },
          legend: {
            display: true,
            position: "right",
            labels: {
              color: "rgb(226, 232, 240)",
              usePointStyle: true,
              padding: 16,
              font: {
                size: 12,
              },
              generateLabels: (chart) => {
                const dataset = chart.data.datasets[0];
                return chart.data.labels?.map((label, i) => {
                  const item = chartData[i];
                  const displayName = item?.countryCode === "Other" ? "Other" : getCountryName(item?.countryCode || "");
                  return {
                    text: `${displayName} (${item?.percentage.toFixed(1)}%)`,
                    fillStyle: (dataset.backgroundColor as string[])[i],
                    strokeStyle: (dataset.borderColor as string),
                    fontColor: "rgb(226, 232, 240)",
                    lineWidth: 0,
                    pointStyle: "circle",
                    hidden: false,
                    index: i,
                  };
                }) || [];
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
</script>

<div class="chart-container" style="height: {height}px;">
  {#if data.length > 0}
    <canvas bind:this={canvas}></canvas>
  {:else}
    <div class="flex items-center justify-center h-full text-muted-foreground">
      No geographic data available
    </div>
  {/if}
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
