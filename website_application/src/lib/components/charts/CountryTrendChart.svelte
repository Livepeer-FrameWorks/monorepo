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
  import { getCountryName } from "$lib/utils/country-names";

  // Register Chart.js components
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

  interface CountryTimeSeriesPoint {
    timestamp: string;
    countryCode: string;
    viewerCount: number;
  }

  interface Props {
    data: CountryTimeSeriesPoint[];
    height?: number;
    title?: string;
    maxCountries?: number;
  }

  let { data = [], height = 300, title = "", maxCountries = 6 }: Props = $props();

  let canvas = $state<HTMLCanvasElement | null>(null);
  let chart: Chart | null = null;

  // Color palette for countries
  const colors = [
    { border: "rgb(59, 130, 246)", bg: "rgba(59, 130, 246, 0.1)" }, // blue
    { border: "rgb(34, 197, 94)", bg: "rgba(34, 197, 94, 0.1)" }, // green
    { border: "rgb(251, 146, 60)", bg: "rgba(251, 146, 60, 0.1)" }, // orange
    { border: "rgb(168, 85, 247)", bg: "rgba(168, 85, 247, 0.1)" }, // purple
    { border: "rgb(236, 72, 153)", bg: "rgba(236, 72, 153, 0.1)" }, // pink
    { border: "rgb(20, 184, 166)", bg: "rgba(20, 184, 166, 0.1)" }, // teal
    { border: "rgb(245, 158, 11)", bg: "rgba(245, 158, 11, 0.1)" }, // amber
    { border: "rgb(239, 68, 68)", bg: "rgba(239, 68, 68, 0.1)" }, // red
  ];

  const createChart = () => {
    if (!canvas || !data.length) return;

    // Destroy existing chart
    if (chart) {
      chart.destroy();
    }

    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    // Group data by country
    const countryData = new Map<string, { timestamp: Date; viewerCount: number }[]>();

    for (const point of data) {
      const country = point.countryCode;
      if (!countryData.has(country)) {
        countryData.set(country, []);
      }
      countryData.get(country)!.push({
        timestamp: new Date(point.timestamp),
        viewerCount: point.viewerCount,
      });
    }

    // Sort countries by total viewers
    const sortedCountries = [...countryData.entries()]
      .map(([country, points]) => ({
        country,
        points: points.sort((a, b) => a.timestamp.getTime() - b.timestamp.getTime()),
        total: points.reduce((sum, p) => sum + p.viewerCount, 0),
      }))
      .sort((a, b) => b.total - a.total)
      .slice(0, maxCountries);

    // Get all unique timestamps
    const allTimestamps = [...new Set(data.map((d) => d.timestamp))]
      .map((t) => new Date(t))
      .sort((a, b) => a.getTime() - b.getTime());

    // Create datasets
    const datasets = sortedCountries.map((country, index) => {
      const color = colors[index % colors.length];

      // Create data array with null for missing timestamps
      const dataPoints = allTimestamps.map((timestamp) => {
        const point = country.points.find(
          (p) => p.timestamp.getTime() === timestamp.getTime()
        );
        return point ? point.viewerCount : null;
      });

      return {
        label: getCountryName(country.country),
        data: dataPoints,
        borderColor: color.border,
        backgroundColor: color.bg,
        fill: false,
        tension: 0.4,
        pointRadius: 2,
        pointHoverRadius: 5,
        borderWidth: 2,
        spanGaps: true,
      };
    });

    const config: ChartConfiguration<"line"> = {
      type: "line",
      data: {
        labels: allTimestamps,
        datasets,
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        interaction: {
          intersect: false,
          mode: "index",
        },
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
              title: (items) => {
                if (!items.length) return "";
                const x = items[0].parsed.x;
                if (x === null || x === undefined) return "";
                const date = new Date(x);
                return date.toLocaleString();
              },
              label: (context) => {
                const label = context.dataset.label || "";
                const value = context.parsed.y;
                if (value === null) return `${label}: N/A`;
                return `${label}: ${value.toLocaleString()} viewers`;
              },
            },
          },
          legend: {
            display: true,
            position: "top",
            labels: {
              color: "rgb(148, 163, 184)",
              usePointStyle: true,
              padding: 16,
              font: {
                size: 12,
              },
            },
          },
        },
        scales: {
          x: {
            type: "time",
            display: true,
            grid: {
              display: false,
            },
            ticks: {
              color: "rgb(148, 163, 184)",
              maxRotation: 0,
              maxTicksLimit: 8,
            },
            border: {
              display: false,
            },
          },
          y: {
            type: "linear",
            display: true,
            position: "left",
            min: 0,
            grid: {
              color: "rgba(148, 163, 184, 0.1)",
            },
            ticks: {
              color: "rgb(148, 163, 184)",
              callback: (value) => {
                if (typeof value === "number") {
                  return value.toLocaleString();
                }
                return value;
              },
            },
            border: {
              display: false,
            },
            title: {
              display: true,
              text: "Viewers",
              color: "rgb(148, 163, 184)",
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
      No country trend data available
    </div>
  {/if}
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
