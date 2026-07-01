// Canned, illustrative analytics fixtures for the /analytics marketing page.
// These mirror the SHAPE of real Periscope/ClickHouse-backed dashboard data so
// the marketing visualizations behave like the product, but the numbers are
// hand-crafted demo values, not benchmarks. No live data, no tenant data.

// Tokyo Night brand palette as concrete colors (Chart.js / Leaflet can't read
// the HSL CSS custom properties), kept in sync with src/index.css :root tokens.
export const palette = {
  blue: "rgb(122, 162, 247)", // --accent #7aa2f7
  cyan: "rgb(125, 207, 255)", // --brand-cyan #7dcfff
  green: "rgb(158, 206, 106)", // --brand-green #9ece6a
  yellow: "rgb(224, 175, 104)", // --brand-yellow #e0af68
  orange: "rgb(255, 158, 100)", // --brand-orange #ff9e64
  red: "rgb(247, 118, 142)", // --brand-red #f7768e
  fg: "rgb(192, 202, 245)", // --foreground #c0caf5
  muted: "rgb(169, 177, 214)", // --muted-foreground #a9b1d6
  grid: "rgba(122, 162, 247, 0.1)",
  tooltipBg: "rgb(36, 40, 59)", // --brand-surface #24283b
  tooltipBorder: "rgba(122, 162, 247, 0.32)",
};

const HOUR_MS = 3_600_000;
// Fixed base instant so prerender + client render produce identical fixtures
// (no Date.now() drift between the static shell and hydration).
const TREND_BASE = new Date("2026-06-16T00:00:00Z").getTime();

// Diurnal 0..1 load curve, peaking in the evening (hour 18).
function diurnal(hour) {
  return 0.5 - 0.5 * Math.cos(((hour - 6) / 24) * Math.PI * 2);
}

function tinySpark(seed, n = 14) {
  // Deterministic gentle wave, varied per seed, for sparkline chrome only.
  const out = [];
  for (let i = 0; i < n; i++) {
    out.push(0.5 + 0.42 * Math.sin(i / 2.1 + seed) + 0.12 * Math.sin(i / 0.8 + seed * 1.7));
  }
  return out;
}

// Section 1: Live + VOD
export const liveVodStats = [
  { label: "Live viewers", value: "12,847", delta: "+8.2%", trend: "up", spark: tinySpark(0.4) },
  { label: "Peak concurrent", value: "18,402", sub: "last 24h", spark: tinySpark(1.3) },
  {
    label: "Total views",
    value: "284K",
    sub: "last 24h",
    delta: "+3.1%",
    trend: "up",
    spark: tinySpark(2.2),
  },
  { label: "Viewer hours", value: "39.6K", sub: "last 24h", spark: tinySpark(3.1) },
];

function buildRetention() {
  const assetDurationS = 600; // 10-minute VOD
  const bucketWidthS = 10;
  const n = Math.round(assetDurationS / bucketWidthS); // 60 buckets
  const totalSessions = 4200;
  const points = [];
  for (let i = 0; i < n; i++) {
    const t = i / (n - 1); // 0..1 along the timeline
    // Sharp drop in the first ~10%, then a gentle decline.
    let r = 0.18 + 0.82 * Math.exp(-2.5 * t);
    // A replay highlight around 62% of the runtime lifts the curve slightly.
    r += 0.1 * Math.exp(-Math.pow((t - 0.62) / 0.05, 2));
    r = Math.min(1, r);
    const reached = Math.round(r * totalSessions);
    // Watch density for the "most replayed" strip. Shaped so the clear maximum is
    // the mid-timeline replay spike, not the high-reach intro.
    const replay =
      Math.exp(-Math.pow((t - 0.62) / 0.05, 2)) + 0.45 * Math.exp(-Math.pow((t - 0.32) / 0.06, 2));
    const density = 0.3 + 0.35 * Math.exp(-1.4 * t) + replay;
    points.push({ bucketIndex: i, reached, secondsWatched: Math.round(density * 1000) });
  }
  return { points, totalSessions, bucketWidthS, assetDurationS };
}

export const retention = buildRetention();

// Section 2: Player experience
function buildQoe() {
  const out = [];
  for (let i = 0; i < 24; i++) {
    const load = diurnal(i);
    out.push({
      timestamp: new Date(TREND_BASE + i * HOUR_MS).toISOString(),
      rebufferingRatio: 0.004 + 0.0065 * load, // 0.4% – 1.05%
      frameDropRatio: 0.0015 + 0.0032 * load, // 0.15% – 0.47%
      avgBitrateBps: (5.8 - 1.5 * load) * 1_000_000, // eases under peak load
      sessionCount: Math.round(380 + 1500 * load),
    });
  }
  return out;
}

export const qoeTrend = buildQoe();

export const qoeSeries = [
  {
    label: "Rebuffering ratio",
    key: "rebufferingRatio",
    scale: 100,
    axis: "y",
    color: palette.blue,
    fill: true,
    unit: "%",
    digits: 2,
  },
  {
    label: "Frame drop ratio",
    key: "frameDropRatio",
    scale: 100,
    axis: "y",
    color: palette.red,
    unit: "%",
    digits: 2,
  },
  {
    label: "Avg bitrate",
    key: "avgBitrateBps",
    scale: 1 / 1_000_000,
    axis: "y1",
    color: palette.cyan,
    unit: " Mbps",
    digits: 1,
  },
];

export const bootWaterfall = {
  // Average boot waterfall: client-reported spans from playback request to first
  // frame. Mirrors the BootTracer stages surfaced in /analytics/player-experience.
  stages: [
    { label: "Gateway resolve", ms: 48, color: palette.blue, hint: "GraphQL playback resolve" },
    { label: "Mist hydrate", ms: 94, color: palette.cyan, hint: "json_<stream>.js" },
    { label: "Player select", ms: 17, color: palette.green, hint: "Protocol scoring" },
    { label: "Connect", ms: 131, color: palette.yellow, hint: "Transport + first byte" },
    { label: "Prebuffer", ms: 318, color: palette.orange, hint: "Fill to first frame" },
  ],
  cacheHitRatio: 0.91,
};

// Section 3: Infrastructure (geo + routing)
export const geo = {
  // Edge clusters that serve viewers.
  clusters: [
    { id: "eu-ams", name: "EU · Amsterdam", lat: 52.37, lng: 4.9 },
    { id: "us-iad", name: "US · Ashburn", lat: 39.04, lng: -77.49 },
    { id: "us-sjc", name: "US · San Jose", lat: 37.34, lng: -121.89 },
    { id: "ap-sin", name: "APAC · Singapore", lat: 1.35, lng: 103.82 },
    { id: "sa-gru", name: "SA · São Paulo", lat: -23.55, lng: -46.63 },
  ],
  // Viewer demand as heat points: [lat, lng, intensity 0..1].
  viewers: [
    { lat: 51.51, lng: -0.13, intensity: 0.95 }, // London
    { lat: 48.85, lng: 2.35, intensity: 0.82 }, // Paris
    { lat: 52.52, lng: 13.4, intensity: 0.78 }, // Berlin
    { lat: 41.39, lng: 2.17, intensity: 0.52 }, // Barcelona
    { lat: 40.71, lng: -74.0, intensity: 0.93 }, // New York
    { lat: 43.65, lng: -79.38, intensity: 0.46 }, // Toronto
    { lat: 34.05, lng: -118.24, intensity: 0.72 }, // Los Angeles
    { lat: 37.77, lng: -122.42, intensity: 0.66 }, // San Francisco
    { lat: 19.43, lng: -99.13, intensity: 0.41 }, // Mexico City
    { lat: 1.35, lng: 103.82, intensity: 0.7 }, // Singapore
    { lat: 35.68, lng: 139.69, intensity: 0.62 }, // Tokyo
    { lat: 28.61, lng: 77.21, intensity: 0.5 }, // Delhi
    { lat: 25.2, lng: 55.27, intensity: 0.36 }, // Dubai
    { lat: -23.55, lng: -46.63, intensity: 0.55 }, // São Paulo
    { lat: -33.87, lng: 151.21, intensity: 0.42 }, // Sydney
    { lat: 55.75, lng: 37.62, intensity: 0.34 }, // Moscow
  ],
  // Client-to-edge routing decisions.
  flows: [
    { from: [51.51, -0.13], to: [52.37, 4.9], status: "success" },
    { from: [48.85, 2.35], to: [52.37, 4.9], status: "success" },
    { from: [52.52, 13.4], to: [52.37, 4.9], status: "success" },
    { from: [40.71, -74.0], to: [39.04, -77.49], status: "success" },
    { from: [43.65, -79.38], to: [39.04, -77.49], status: "success" },
    { from: [34.05, -118.24], to: [37.34, -121.89], status: "success" },
    { from: [37.77, -122.42], to: [37.34, -121.89], status: "success" },
    { from: [35.68, 139.69], to: [1.35, 103.82], status: "success" },
    { from: [28.61, 77.21], to: [1.35, 103.82], status: "degraded" }, // long-haul
    { from: [-23.55, -46.63], to: [-23.55, -46.63], status: "success" },
  ],
};

// Section 4: Usage & cost
export const usageStats = [
  { label: "Egress", value: "992 GB", sub: "this period", spark: tinySpark(0.9) },
  { label: "Stream hours", value: "1,284", sub: "this period", spark: tinySpark(1.8) },
  { label: "Recording storage", value: "412 GiB", spark: tinySpark(2.6) },
  { label: "Settlement lag", value: "~5 min", sub: "near real-time" },
];

export const codecMix = [
  { label: "H.264", value: 614, color: palette.blue },
  { label: "HEVC", value: 208, color: palette.cyan },
  { label: "VP9", value: 96, color: palette.green },
  { label: "AV1", value: 74, color: palette.orange },
];
