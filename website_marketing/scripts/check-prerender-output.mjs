import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

const clientDir = join(process.cwd(), "build", "client");

const routes = [
  [
    "index.html",
    "FrameWorks - Sovereign Live Streaming Platform, Hosted or Self-Hosted",
    "Sovereign Video Infrastructure",
  ],
  // Snippet stops before "QoE & Geo" because the built title HTML-encodes the ampersand.
  ["analytics/index.html", "FrameWorks Analytics - Real-Time Streaming Telemetry"],
  ["pricing/index.html", "FrameWorks Pricing - Hosted, Hybrid, and Self-Hosted Streaming"],
  ["about/index.html", "About FrameWorks - The Team Behind Sovereign Live Streaming"],
  ["contact/index.html", "Contact FrameWorks - Streaming Infrastructure Support"],
  ["status/index.html", "FrameWorks Status - Live Streaming Network Health"],
  ["privacy/index.html", "FrameWorks Privacy Policy"],
  ["terms/index.html", "FrameWorks Terms of Service"],
  ["aup/index.html", "FrameWorks Acceptable Use Policy"],
  // Snippet stops before "& Responsible" because the built title HTML-encodes the ampersand.
  ["security/index.html", "FrameWorks Security"],
];

const staticFiles = [
  "health",
  "robots.txt",
  "llms.txt",
  "sitemap.xml",
  "favicon.ico",
  "favicon-192.png",
  "apple-touch-icon.png",
  "site.webmanifest",
];
const failures = [];

for (const [relativePath, ...snippets] of routes) {
  const absolutePath = join(clientDir, relativePath);
  if (!existsSync(absolutePath)) {
    failures.push(`missing route output: build/client/${relativePath}`);
    continue;
  }

  const html = readFileSync(absolutePath, "utf8");
  for (const snippet of snippets) {
    if (!html.includes(snippet)) {
      failures.push(`build/client/${relativePath} missing ${JSON.stringify(snippet)}`);
    }
  }

  // Search and assistive clients need one document title and one primary
  // heading per prerendered content route.
  const titleMatch = html.match(/<title>([^<]*)<\/title>/);
  if (!titleMatch || titleMatch[1].trim().length === 0) {
    failures.push(`build/client/${relativePath} has no non-empty <title>`);
  }
  const h1Count = (html.match(/<h1[\s>]/g) || []).length;
  if (h1Count !== 1) {
    failures.push(`build/client/${relativePath} has ${h1Count} <h1> tags (expected exactly 1)`);
  }
  const emptyHeadings = html.match(/<h[1-6][^>]*>\s*<\/h[1-6]>/g) || [];
  if (emptyHeadings.length > 0) {
    failures.push(`build/client/${relativePath} has ${emptyHeadings.length} empty heading tag(s)`);
  }

  if (!html.includes('<main id="main-content">')) {
    failures.push(`build/client/${relativePath} is missing the primary main landmark`);
  }
}

const homeHtml = readFileSync(join(clientDir, "index.html"), "utf8");
if (!homeHtml.includes("hero-player-card__overlay-glitch")) {
  failures.push("build/client/index.html is missing the animated hero glitch overlay");
}
if (!homeHtml.includes("hero-player-card__standby")) {
  failures.push("build/client/index.html is missing the automatic-player standby surface");
}
if (homeHtml.includes("Start live demo")) {
  failures.push("build/client/index.html unexpectedly requires a click to start the live demo");
}

const executableHomeHead = homeHtml
  .split("</head>", 1)[0]
  .replaceAll(/<noscript>[\s\S]*?<\/noscript>/g, "");
for (const deferredRuntime of [
  "fonts.googleapis.com",
  "NetworkMap-",
  "player-",
  "hls-",
  "video.es-",
  "dash.all",
  "maplibre-gl-",
]) {
  if (executableHomeHead.includes(deferredRuntime)) {
    failures.push(`build/client/index.html eagerly loads deferred runtime ${deferredRuntime}`);
  }
}

const landingSource = readFileSync(
  join(process.cwd(), "src/components/pages/LandingPage.jsx"),
  "utf8"
);
if (
  !landingSource.includes("requestIdleCallback(loadPlayer") ||
  !landingSource.includes("autoplay: true")
) {
  failures.push("LandingPage must automatically load and autoplay the deferred live player");
}

for (const relativePath of staticFiles) {
  const absolutePath = join(clientDir, relativePath);
  if (!existsSync(absolutePath)) {
    failures.push(`missing static output: build/client/${relativePath}`);
  }
}

// Non-content artifacts need explicit noindex headers because they are copied
// into the served build output beside the real routes.
const serveConfigPath = join(clientDir, "serve.json");
if (!existsSync(serveConfigPath)) {
  failures.push("missing build/client/serve.json (de-index rules for non-content files)");
} else {
  const serveConfig = readFileSync(serveConfigPath, "utf8");
  if (!serveConfig.includes("__spa-fallback.html")) {
    failures.push("build/client/serve.json missing the __spa-fallback.html noindex rule");
  }
  if (!serveConfig.includes("X-Robots-Tag")) {
    failures.push("build/client/serve.json missing X-Robots-Tag noindex headers");
  }
  if (!serveConfig.includes("sitemap.xml.data")) {
    failures.push("build/client/serve.json missing the sitemap sidecar noindex rule");
  }
  if (!serveConfig.includes("max-age=31536000, immutable")) {
    failures.push("build/client/serve.json missing immutable caching for hashed assets");
  }
}

const sitemapPath = join(clientDir, "sitemap.xml");
if (existsSync(sitemapPath)) {
  const sitemap = readFileSync(sitemapPath, "utf8");
  if (!sitemap.startsWith("<?xml")) {
    failures.push("build/client/sitemap.xml is not XML");
  }
  if (!sitemap.includes("<loc>https://frameworks.network/pricing</loc>")) {
    failures.push("build/client/sitemap.xml is missing the pricing route");
  }
  if (sitemap.includes("<lastmod>")) {
    failures.push("build/client/sitemap.xml contains synthetic build-time lastmod values");
  }
}

const dockerfile = readFileSync(join(process.cwd(), "Dockerfile"), "utf8");
if (/serve\s+-s\s+build\/client/.test(dockerfile)) {
  failures.push("Dockerfile uses serve -s, which rewrites deep routes to the root HTML");
}
if (!/serve\s+build\/client\s+-l/.test(dockerfile)) {
  failures.push("Dockerfile does not serve build/client as static route files");
}

if (failures.length > 0) {
  console.error("Marketing prerender output check failed:");
  for (const failure of failures) {
    console.error(`- ${failure}`);
  }
  process.exit(1);
}

console.log("Marketing prerender output verified.");
