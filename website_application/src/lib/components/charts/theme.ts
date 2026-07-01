// Shared Chart.js theme for all analytics charts.
//
// tokens.css stores colors as HSL triples for `hsl(var(--token) / a)` alpha use,
// which Chart.js cannot read at runtime, so we mirror the Tokyo Night values as
// RGB here. These are the same values the marketing dashboards use, so the webapp
// and marketing charts stay visually identical. Keep in sync with src/styles/tokens.css.

export const palette = {
  blue: "rgb(122, 162, 247)", // --tn-blue  #7aa2f7
  cyan: "rgb(125, 207, 255)", // --tn-cyan  #7dcfff
  teal: "rgb(115, 218, 202)", // --tn-teal  #73daca
  green: "rgb(158, 206, 106)", // --tn-green #9ece6a
  yellow: "rgb(224, 175, 104)", // --tn-yellow #e0af68
  orange: "rgb(255, 158, 100)", // --tn-orange #ff9e64
  red: "rgb(247, 118, 142)", // --tn-red   #f7768e
  magenta: "rgb(255, 0, 124)", // --tn-magenta #ff007c
  purple: "rgb(187, 154, 247)", // --tn-purple #bb9af7
  fg: "rgb(192, 202, 245)", // --tn-fg     #c0caf5
  fgDark: "rgb(169, 177, 214)", // --tn-fg-dark #a9b1d6
  gutter: "rgb(59, 66, 97)", // --tn-fg-gutter #3b4261
  surface: "rgb(36, 40, 59)", // --tn-bg-highlight #24283b
  surfaceDark: "rgb(22, 22, 30)", // --tn-bg-dark #16161e
} as const;

// Ordered categorical palette for breakdowns (codec, country, storage, etc).
export const seriesColors = [
  palette.blue,
  palette.cyan,
  palette.green,
  palette.yellow,
  palette.orange,
  palette.red,
  palette.purple,
  palette.teal,
];

// Heat gradient shared by GeoView's viewer-demand layer (blue to red).
export const heatGradient: Record<number, string> = {
  0.05: "rgba(122, 162, 247, 0.55)",
  0.25: "rgba(125, 207, 255, 0.75)",
  0.45: "rgba(158, 206, 106, 0.85)",
  0.65: "rgba(224, 175, 104, 0.9)",
  0.9: "rgba(247, 118, 142, 0.95)",
};

export function alpha(rgb: string, a: number): string {
  return rgb.replace("rgb(", "rgba(").replace(")", `, ${a})`);
}

export const gridColor = alpha(palette.fgDark, 0.1);

// Per-series config for TrendChart.
export interface TrendSeries {
  key: string; // field on each data row
  label: string;
  color?: string; // defaults walk the brand palette
  axis?: "y" | "y1"; // y1 = secondary right axis
  filled?: boolean;
  dashed?: boolean;
  scale?: number; // multiply raw value (e.g. 100 for ratio->%, 1/1e6 for bps->Mbps)
  unit?: string; // appended in tooltip when no format
  digits?: number; // tooltip decimals when no format
  format?: (displayValue: number) => string; // full control of tooltip value text
}

// Chart.js option fragments. Returned as plain objects; consumers spread them into
// a ChartConfiguration and cast as needed (Chart.js deep-partial typing is noisy).

export function tooltipTheme() {
  return {
    backgroundColor: palette.surface,
    titleColor: palette.fg,
    bodyColor: palette.fgDark,
    borderColor: alpha(palette.blue, 0.32),
    borderWidth: 1,
    padding: 12,
    usePointStyle: true,
  };
}

export function legendTheme() {
  return {
    display: true,
    position: "top" as const,
    labels: { color: palette.fgDark, usePointStyle: true, padding: 16, boxHeight: 7 },
  };
}

// Time x-axis (Chart.js "time" scale; needs chartjs-adapter-date-fns registered).
export function timeScale(maxTicks = 7) {
  return {
    type: "time" as const,
    grid: { display: false },
    ticks: { color: palette.fgDark, maxRotation: 0, maxTicksLimit: maxTicks },
    border: { display: false },
  };
}

export function categoryScale() {
  return {
    grid: { display: false },
    ticks: { color: palette.fg },
    border: { display: false },
  };
}

// Linear value axis. Pass grid:false for a secondary (right) axis so it does not
// double-draw gridlines over the primary.
export interface AxisCfg {
  title?: string;
  max?: number;
  color?: string;
  tickFormat?: (v: number) => string;
}

export function linearAxis(
  opts: {
    position?: "left" | "right";
    title?: string;
    color?: string;
    grid?: boolean;
    min?: number;
    max?: number;
    tickFormat?: (v: number) => string;
  } = {}
) {
  const color = opts.color ?? palette.fgDark;
  return {
    type: "linear" as const,
    position: opts.position ?? ("left" as const),
    min: opts.min ?? 0,
    ...(opts.max != null ? { max: opts.max } : {}),
    grid: opts.grid === false ? { drawOnChartArea: false } : { color: gridColor },
    ticks: {
      color,
      ...(opts.tickFormat ? { callback: (v: number | string) => opts.tickFormat!(Number(v)) } : {}),
    },
    border: { display: false },
    ...(opts.title ? { title: { display: true, text: opts.title, color } } : {}),
  };
}

// A themed line dataset. color drives border + a faint fill when filled=true.
export function lineDataset(opts: {
  label: string;
  data: (number | null)[];
  color: string;
  axis?: string;
  filled?: boolean;
}) {
  return {
    label: opts.label,
    data: opts.data,
    borderColor: opts.color,
    backgroundColor: opts.filled ? alpha(opts.color, 0.12) : "transparent",
    fill: !!opts.filled,
    tension: 0.35,
    pointRadius: 0,
    pointHoverRadius: 4,
    borderWidth: 1.75,
    yAxisID: opts.axis ?? "y",
    spanGaps: true,
  };
}
