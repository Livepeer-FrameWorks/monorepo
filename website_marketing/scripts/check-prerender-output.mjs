import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";

const clientDir = join(process.cwd(), "build", "client");

const routes = [
  ["index.html", "FrameWorks - Sovereign Video Infrastructure", "Sovereign Video Infrastructure"],
  ["pricing/index.html", "FrameWorks Pricing - Hosted, Hybrid, and Self-Hosted Streaming"],
  ["about/index.html", "About FrameWorks - Sovereign Streaming Infrastructure Team"],
  ["contact/index.html", "Contact FrameWorks - Streaming Infrastructure Support"],
  ["status/index.html", "FrameWorks Status - Live Streaming Network Health"],
  ["privacy/index.html", "FrameWorks Privacy Policy"],
  ["terms/index.html", "FrameWorks Terms of Service"],
  ["aup/index.html", "FrameWorks Acceptable Use Policy"],
];

const staticFiles = ["health", "robots.txt", "llms.txt", "sitemap.xml"];
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
}

for (const relativePath of staticFiles) {
  const absolutePath = join(clientDir, relativePath);
  if (!existsSync(absolutePath)) {
    failures.push(`missing static output: build/client/${relativePath}`);
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
