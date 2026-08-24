import { existsSync, readFileSync, readdirSync } from "node:fs";
import { basename, join } from "node:path";

const outputDir = join(process.cwd(), "dist");
const blogSourceDir = join(process.cwd(), "src", "content", "docs", "blog");
const failures = [];

for (const sourceName of readdirSync(blogSourceDir).filter((name) => name.endsWith(".mdx"))) {
  const slug = basename(sourceName, ".mdx");
  const outputPath = join(outputDir, "blog", slug, "index.html");
  if (!existsSync(outputPath)) {
    failures.push(`missing blog output: dist/blog/${slug}/index.html`);
    continue;
  }

  const html = readFileSync(outputPath, "utf8");
  if (!/<meta name="description" content="[^"]+"\s*\/>/.test(html)) {
    failures.push(`dist/blog/${slug}/index.html has no meta description`);
  }
  if (!/<meta property="og:description" content="[^"]+"\s*\/>/.test(html)) {
    failures.push(`dist/blog/${slug}/index.html has no Open Graph description`);
  }
  if (!html.includes('"@type":"BlogPosting"')) {
    failures.push(`dist/blog/${slug}/index.html has no BlogPosting JSON-LD`);
  }
  if (!html.includes('"@type":"BreadcrumbList"')) {
    failures.push(`dist/blog/${slug}/index.html has no BreadcrumbList JSON-LD`);
  }
  if (!html.includes(`rel="canonical" href="https://logbook.frameworks.network/blog/${slug}/"`)) {
    failures.push(`dist/blog/${slug}/index.html has an unexpected canonical URL`);
  }
}

const serveConfigPath = join(outputDir, "serve.json");
if (!existsSync(serveConfigPath)) {
  failures.push("missing dist/serve.json");
} else {
  const serveConfig = readFileSync(serveConfigPath, "utf8");
  for (const required of [
    '"trailingSlash": true',
    '"source": "/streamers/quick-start"',
    '"source": "@(404|404.html)"',
    '"source": "_astro/**"',
    "max-age=31536000, immutable",
  ]) {
    if (!serveConfig.includes(required)) failures.push(`dist/serve.json missing ${required}`);
  }
}

if (failures.length > 0) {
  console.error("Docs SEO output check failed:");
  for (const failure of failures) console.error(`- ${failure}`);
  process.exit(1);
}

console.log("Docs SEO output verified.");
