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
    Title,
    Tooltip,
    Filler
  );

  interface DataPoint {
    timestamp: string | Date;
    viewers: number;
  }

  interface Props {
    data: DataPoint[];
    mini?: boolean;
    height?: number;
    title?: string;
    seriesLabel?: string;
    valueFormatter?: (value: number) => string;
  }

  let {
    data = [],
    mini = false,
    height = 200,
    title = "",
    seriesLabel = "Viewers",
    valueFormatter = (value: number) => `${value} viewers`,
  }: Props = $props();

  let canvas = $state<HTMLCanvasElement>();
  let chart: Chart | null = null;

  const createChart = () => {
    if (!canvas || !data.length) return;

    // Destroy existing chart
    if (chart) {
      chart.destroy();
    }

    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    // Sort data by timestamp
    const sortedData = [...data].sort(
      (a, b) => new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
    );

    const config: ChartConfiguration = {
      type: "line",
      data: {
        labels: sortedData.map((d) => new Date(d.timestamp)),
        datasets: [
          {
            label: seriesLabel,
            data: sortedData.map((d) => d.viewers),
            borderColor: "rgb(59, 130, 246)",
            backgroundColor: "rgba(59, 130, 246, 0.1)",
            fill: true,
            tension: 0.4,
            pointRadius: mini ? 0 : 3,
            pointHoverRadius: mini ? 3 : 6,
            borderWidth: mini ? 1.5 : 2,
          },
        ],
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
            display: !mini && !!title,
            text: title,
            color: "rgb(148, 163, 184)",
            font: {
              size: 14,
              weight: "normal",
            },
          },
          tooltip: {
            enabled: !mini,
            backgroundColor: "rgb(30, 41, 59)",
            titleColor: "rgb(226, 232, 240)",
            bodyColor: "rgb(148, 163, 184)",
            borderColor: "rgb(51, 65, 85)",
            borderWidth: 1,
            padding: 12,
            displayColors: false,
            callbacks: {
              title: (items) => {
                if (!items.length) return "";
                const x = items[0].parsed.x;
                if (x === null || x === undefined) return "";
                const date = new Date(x);
                return date.toLocaleString();
              },
              label: (context) => valueFormatter(context.parsed.y as number),
            },
          },
          legend: {
            display: false,
          },
        },
        scales: {
          x: {
            type: "time",
            display: !mini,
            grid: {
              display: false,
            },
            ticks: {
              color: "rgb(148, 163, 184)",
              maxRotation: 0,
            },
            border: {
              display: false,
            },
          },
          y: {
            display: !mini,
            beginAtZero: true,
            grid: {
              color: "rgba(148, 163, 184, 0.1)",
            },
            ticks: {
              color: "rgb(148, 163, 184)",
              precision: 0,
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
  <canvas bind:this={canvas}></canvas>
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
