import { useEffect, useRef } from "react";
import { palette } from "./fixtures";

// Multi-series time-line chart (Chart.js). Chart.js is imported lazily inside the
// effect so it only loads/instantiates on the client, so the RR7 prerender shell
// stays free of canvas/SSR access, same client-only pattern as NetworkMap.
//
// data:   [{ timestamp, <key>: number, ... }]
// series: [{ label, key, scale?, axis: "y"|"y1", color, fill?, unit?, digits? }]
export function TrendChart({ data, series, height = 280, leftTitle, rightTitle }) {
  const canvasRef = useRef(null);

  useEffect(() => {
    let chart = null;
    let cancelled = false;

    (async () => {
      const chartjs = await import("chart.js");
      await import("chartjs-adapter-date-fns");
      if (cancelled || !canvasRef.current) return;

      const {
        Chart,
        LineController,
        LineElement,
        PointElement,
        LinearScale,
        TimeScale,
        Tooltip,
        Legend,
        Filler,
      } = chartjs;
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

      const ctx = canvasRef.current.getContext("2d");
      if (!ctx) return;

      const sorted = [...data].sort((a, b) => new Date(a.timestamp) - new Date(b.timestamp));
      const counts = sorted.map((d) => d.sessionCount ?? 0);
      const hasRight = series.some((s) => s.axis === "y1");

      const datasets = series.map((s) => ({
        label: s.label,
        data: sorted.map((d) => (d[s.key] != null ? d[s.key] * (s.scale ?? 1) : null)),
        borderColor: s.color,
        backgroundColor: s.fill
          ? s.color.replace("rgb(", "rgba(").replace(")", ", 0.12)")
          : "transparent",
        fill: !!s.fill,
        tension: 0.35,
        pointRadius: 0,
        pointHoverRadius: 4,
        borderWidth: 1.75,
        yAxisID: s.axis ?? "y",
        spanGaps: true,
      }));

      chart = new Chart(ctx, {
        type: "line",
        data: { labels: sorted.map((d) => new Date(d.timestamp)), datasets },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          interaction: { intersect: false, mode: "index" },
          plugins: {
            legend: {
              display: true,
              position: "top",
              labels: { color: palette.muted, usePointStyle: true, padding: 16, boxHeight: 7 },
            },
            tooltip: {
              backgroundColor: palette.tooltipBg,
              titleColor: palette.fg,
              bodyColor: palette.muted,
              borderColor: palette.tooltipBorder,
              borderWidth: 1,
              padding: 12,
              usePointStyle: true,
              callbacks: {
                title: (items) =>
                  items.length && items[0].parsed.x != null
                    ? new Date(items[0].parsed.x).toLocaleString()
                    : "",
                label: (c) => {
                  const s = series[c.datasetIndex];
                  const v = c.parsed.y;
                  if (v == null) return `${s.label}: N/A`;
                  return `${s.label}: ${v.toFixed(s.digits ?? 2)}${s.unit ?? ""}`;
                },
                afterBody: (items) => {
                  if (!items.length) return "";
                  const n = counts[items[0].dataIndex] ?? 0;
                  return `${n.toLocaleString()} session${n === 1 ? "" : "s"}`;
                },
              },
            },
          },
          scales: {
            x: {
              type: "time",
              time: { unit: "hour" },
              grid: { display: false },
              ticks: { color: palette.muted, maxRotation: 0, maxTicksLimit: 7 },
              border: { display: false },
            },
            y: {
              type: "linear",
              position: "left",
              min: 0,
              grid: { color: palette.grid },
              ticks: { color: palette.muted },
              border: { display: false },
              title: leftTitle
                ? { display: true, text: leftTitle, color: palette.muted }
                : undefined,
            },
            ...(hasRight
              ? {
                  y1: {
                    type: "linear",
                    position: "right",
                    min: 0,
                    grid: { drawOnChartArea: false },
                    ticks: { color: palette.cyan },
                    border: { display: false },
                    title: rightTitle
                      ? { display: true, text: rightTitle, color: palette.cyan }
                      : undefined,
                  },
                }
              : {}),
          },
        },
      });
    })();

    return () => {
      cancelled = true;
      if (chart) chart.destroy();
    };
  }, [data, series, leftTitle, rightTitle]);

  return (
    <div className="dashboard-chart" style={{ height }}>
      <canvas ref={canvasRef} />
    </div>
  );
}

export default TrendChart;
