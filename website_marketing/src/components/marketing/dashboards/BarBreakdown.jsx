import { useEffect, useRef } from "react";
import { palette } from "./fixtures";

// Horizontal bar breakdown (Chart.js), e.g. codec mix or bandwidth share. Tooltip
// shows the raw value + share of total. Lazy client-only init like TrendChart.
//
// items: [{ label, value, color }], unit appended in tooltips (default " GB").
export function BarBreakdown({ items, unit = " GB", height = 240 }) {
  const canvasRef = useRef(null);

  useEffect(() => {
    let chart = null;
    let cancelled = false;

    (async () => {
      const chartjs = await import("chart.js");
      if (cancelled || !canvasRef.current) return;

      const { Chart, BarController, BarElement, CategoryScale, LinearScale, Tooltip } = chartjs;
      Chart.register(BarController, BarElement, CategoryScale, LinearScale, Tooltip);

      const ctx = canvasRef.current.getContext("2d");
      if (!ctx) return;

      const total = items.reduce((sum, i) => sum + i.value, 0) || 1;

      chart = new Chart(ctx, {
        type: "bar",
        data: {
          labels: items.map((i) => i.label),
          datasets: [
            {
              data: items.map((i) => i.value),
              backgroundColor: items.map((i) => i.color),
              borderRadius: 5,
              borderSkipped: false,
              barThickness: 18,
            },
          ],
        },
        options: {
          indexAxis: "y",
          responsive: true,
          maintainAspectRatio: false,
          plugins: {
            legend: { display: false },
            tooltip: {
              backgroundColor: palette.tooltipBg,
              titleColor: palette.fg,
              bodyColor: palette.muted,
              borderColor: palette.tooltipBorder,
              borderWidth: 1,
              padding: 12,
              callbacks: {
                label: (c) => {
                  const v = c.parsed.x;
                  const share = ((v / total) * 100).toFixed(1);
                  return `${v.toLocaleString()}${unit} · ${share}%`;
                },
              },
            },
          },
          scales: {
            x: {
              min: 0,
              grid: { color: palette.grid },
              ticks: { color: palette.muted },
              border: { display: false },
            },
            y: {
              grid: { display: false },
              ticks: { color: palette.fg, font: { size: 13 } },
              border: { display: false },
            },
          },
        },
      });
    })();

    return () => {
      cancelled = true;
      if (chart) chart.destroy();
    };
  }, [items, unit]);

  return (
    <div className="dashboard-chart" style={{ height }}>
      <canvas ref={canvasRef} />
    </div>
  );
}

export default BarBreakdown;
