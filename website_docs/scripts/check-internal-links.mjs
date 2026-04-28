#!/usr/bin/env node

import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import process from "node:process";

const docsRoot = path.resolve("src/content/docs");

async function walk(dir) {
  const entries = await readdir(dir, { withFileTypes: true });
  const files = [];
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      files.push(...(await walk(fullPath)));
    } else if (entry.isFile() && entry.name.endsWith(".mdx")) {
      files.push(fullPath);
    }
  }
  return files;
}

function routeForFile(file) {
  const rel = path
    .relative(docsRoot, file)
    .replaceAll(path.sep, "/")
    .replace(/\.mdx$/, "");
  if (rel === "index") {
    return "/";
  }
  return `/${rel}/`;
}

function normalizeTarget(rawTarget, sourceRoute) {
  const target = rawTarget.trim();
  if (target.startsWith("#")) {
    return { route: sourceRoute, anchor: target.slice(1) };
  }
  if (!target.startsWith("/") || target.startsWith("//")) {
    return null;
  }

  const withoutQuery = target.split("?")[0];
  const [rawRoutePart, rawAnchor] = withoutQuery.split("#");
  const routePart = rawRoutePart || "/";
  const route = routePart.endsWith("/") ? routePart : `${routePart}/`;
  return { route, anchor: rawAnchor ?? "" };
}

function stripCodeBlocks(content) {
  return content.replace(/```[\s\S]*?```/g, "");
}

function slugifyHeading(heading) {
  const text = heading
    .replace(/<[^>]+>/g, "")
    .replace(/`([^`]+)`/g, "$1")
    .replace(/[^\p{Letter}\p{Number}\s-]/gu, "")
    .trim()
    .toLowerCase();

  return text.replace(/\s/g, "-");
}

function collectAnchors(content) {
  const anchors = new Set();
  const seen = new Map();
  for (const match of stripCodeBlocks(content).matchAll(/^#{2,6}\s+(.+?)\s*#*\s*$/gm)) {
    const base = slugifyHeading(match[1]);
    if (!base) {
      continue;
    }
    const count = seen.get(base) ?? 0;
    seen.set(base, count + 1);
    anchors.add(count === 0 ? base : `${base}-${count}`);
  }
  return anchors;
}

function findInternalLinks(content) {
  const links = [];
  const patterns = [
    /\[[^\]]+\]\((\/[^)\s]+)\)/g,
    /\[[^\]]+\]\((#[^)\s]+)\)/g,
    /\bhref=["'](\/[^"']+)["']/g,
    /\blink:\s*(\/[^\s]+)/g,
  ];

  for (const pattern of patterns) {
    for (const match of stripCodeBlocks(content).matchAll(pattern)) {
      links.push(match[1]);
    }
  }
  return links;
}

const files = await walk(docsRoot);
const routes = new Set(files.map(routeForFile));
const anchorsByRoute = new Map();
const failures = [];

for (const file of files) {
  const content = await readFile(file, "utf8");
  anchorsByRoute.set(routeForFile(file), collectAnchors(content));
}

for (const file of files) {
  const content = await readFile(file, "utf8");
  const sourceRoute = routeForFile(file);
  for (const rawLink of findInternalLinks(content)) {
    const target = normalizeTarget(rawLink, sourceRoute);
    if (!target) {
      continue;
    }
    if (!routes.has(target.route)) {
      failures.push({
        source: path.relative(process.cwd(), file),
        target: rawLink,
        reason: "missing route",
      });
      continue;
    }
    if (target.anchor && !anchorsByRoute.get(target.route)?.has(target.anchor)) {
      failures.push({
        source: path.relative(process.cwd(), file),
        target: rawLink,
        reason: "missing anchor",
      });
    }
  }
}

if (failures.length > 0) {
  console.error("Broken internal docs links:");
  for (const failure of failures) {
    console.error(`- ${failure.source} -> ${failure.target} (${failure.reason})`);
  }
  process.exit(1);
}

console.log(`Checked ${files.length} docs pages, ${routes.size} routes, and heading anchors.`);
