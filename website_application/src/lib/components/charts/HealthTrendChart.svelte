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

  interface HealthDataPoint {
    timestamp: string;
    bufferHealth?: number | null;
    bitrate?: number | null;
  }

  interface Props {
    data: HealthDataPoint[];
    height?: number;
    showBufferHealth?: boolean;
    showBitrate?: boolean;
  }

  let {
    data = [],
    height = 300,
    showBufferHealth = true,
    showBitrate = true,
  }: Props = $props();

  let canvas: HTMLCanvasElement;
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
      (a, b) =>
        new Date(a.timestamp).getTime() - new Date(b.timestamp).getTime()
    );

    const datasets: any[] = [];

    if (showBufferHealth) {
      datasets.push({
        label: "Buffer Health (%)",
        data: sortedData.map((d) =>
          d.bufferHealth != null ? d.bufferHealth * 100 : null
        ),
        borderColor: "rgb(34, 197, 94)", // green
        backgroundColor: "rgba(34, 197, 94, 0.1)",
        fill: true,
        tension: 0.4,
        pointRadius: 2,
        pointHoverRadius: 5,
        borderWidth: 2,
        yAxisID: "y",
      });
    }

    if (showBitrate) {
      datasets.push({
        label: "Bitrate (Mbps)",
        data: sortedData.map((d) =>
          d.bitrate != null ? d.bitrate / 1000000 : null
        ),
        borderColor: "rgb(59, 130, 246)", // blue
        backgroundColor: "transparent",
        fill: false,
        tension: 0.4,
        pointRadius: 1,
        pointHoverRadius: 4,
        borderWidth: 1.5,
        yAxisID: "y1",
      });
    }

    const config: ChartConfiguration = {
      type: "line",
      data: {
        labels: sortedData.map((d) => new Date(d.timestamp)),
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
            display: false,
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

                if (label.includes("Buffer Health")) {
                  return `${label}: ${value.toFixed(0)}%`;
                } else if (label.includes("Bitrate")) {
                  return `${label}: ${value.toFixed(2)} Mbps`;
                }
                return `${label}: ${value}`;
              },
            },
          },
          legend: {
            display: true,
            position: "top",
            labels: {
              color: "rgb(148, 163, 184)",
              usePointStyle: true,
              padding: 20,
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
            max: 100,
            grid: {
              color: "rgba(148, 163, 184, 0.1)",
            },
            ticks: {
              color: "rgb(148, 163, 184)",
              callback: (value) => `${value}%`,
            },
            border: {
              display: false,
            },
            title: {
              display: true,
              text: "Buffer Health %",
              color: "rgb(148, 163, 184)",
            },
          },
          y1: {
            type: "linear",
            display: showBitrate,
            position: "right",
            min: 0,
            grid: {
              drawOnChartArea: false,
            },
            ticks: {
              color: "rgb(148, 163, 184)",
            },
            border: {
              display: false,
            },
            title: {
              display: true,
              text: "Bitrate (Mbps)",
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
  <canvas bind:this={canvas}></canvas>
</div>

<style>
  .chart-container {
    position: relative;
    width: 100%;
  }
</style>
