// @ts-check
import { defineConfig } from "astro/config";
import svelte from "@astrojs/svelte";
import starlight from "@astrojs/starlight";
import sitemap from "@astrojs/sitemap";
import starlightBlog from "starlight-blog";
import mermaid from "astro-mermaid";
import { visit } from "unist-util-visit";
import { loadEnv } from "vite";
import { codecovVitePlugin } from "@codecov/vite-plugin";

// Load .env files manually - Astro doesn't auto-load them in config
// See: https://docs.astro.build/en/guides/environment-variables/
const env = loadEnv(process.env.NODE_ENV ?? "development", process.cwd(), "");

// Parse streaming URLs to derive hostname and TLS mode
function parseStreamingUrl(url) {
  try {
    const parsed = new URL(url);
    return {
      hostname: parsed.hostname,
      port: parsed.port,
      useTls: parsed.protocol === "https:",
    };
  } catch {
    return { hostname: "localhost", port: "", useTls: false };
  }
}

const ingestUrl = env.VITE_STREAMING_INGEST_URL || "http://localhost:8080";
const edgeUrl = env.VITE_STREAMING_EDGE_URL || "http://localhost:8080";
const playUrl = env.VITE_STREAMING_PLAY_URL || "http://localhost:18008";
const ingest = parseStreamingUrl(ingestUrl);
const edge = parseStreamingUrl(edgeUrl);

// Build protocol-specific URLs
const rtmpPort = env.VITE_STREAMING_RTMP_PORT || "1935";
const srtPort = env.VITE_STREAMING_SRT_PORT || "8889";
const rtmpPath = env.VITE_STREAMING_RTMP_PATH || "/live";
const hlsPath = env.VITE_STREAMING_HLS_PATH || "/hls";
const webrtcPath = env.VITE_STREAMING_WEBRTC_PATH || "/webrtc";

// Build full URLs for docs examples
const rtmpProto = ingest.useTls ? "rtmps" : "rtmp";
const httpProto = ingest.useTls ? "https" : "http";
const edgeHttpProto = edge.useTls ? "https" : "http";
const edgePortPart = edge.port ? `:${edge.port}` : "";
const ingestPortPart = ingest.port ? `:${ingest.port}` : "";

// Env var mapping for MDX placeholder replacement
const envVarMap = {
  APP_URL: env.VITE_APP_URL,
  MARKETING_URL: env.VITE_MARKETING_SITE_URL,
  GATEWAY_URL: env.VITE_GATEWAY_URL,
  API_URL: env.VITE_GATEWAY_URL,
  // Streaming - constructed URLs for protocol-specific examples
  RTMP_URL: `${rtmpProto}://${ingest.hostname}:${rtmpPort}${rtmpPath}`,
  SRT_HOST: `${ingest.hostname}:${srtPort}`,
  WHIP_URL: `${httpProto}://${ingest.hostname}${ingestPortPart}${webrtcPath}`,
  EDGE_URL: `${edgeHttpProto}://${edge.hostname}${edgePortPart}`,
  PLAY_URL: playUrl,
  INGEST_URL: ingestUrl,
  HLS_PATH: hlsPath,
  WEBRTC_PATH: webrtcPath,
  // Raw hostname for SRT examples that need just the host
  INGEST_HOSTNAME: ingest.hostname,
  SRT_PORT: srtPort,
  // Community links
  DISCORD_URL: env.VITE_DISCORD_URL,
  GITHUB_URL: env.VITE_GITHUB_URL,
};

// Helper to replace %PLACEHOLDER% patterns
function replaceEnvVars(str) {
  if (typeof str !== "string" || !str.includes("%")) return str;
  return str.replace(/%(\w+)%/g, (match, key) => envVarMap[key] ?? match);
}

// Remark plugin to replace %PLACEHOLDER% with env var values
// Using % instead of {{ because MDX interprets {{ as JSX expressions
function remarkEnvReplace() {
  return (tree) => {
    visit(tree, (node) => {
      // Text nodes (regular markdown text)
      if (node.type === "text" && node.value) {
        node.value = replaceEnvVars(node.value);
      }
      // Code blocks (fenced ```code```)
      if (node.type === "code" && node.value) {
        node.value = replaceEnvVars(node.value);
      }
      // Inline code (`code`)
      if (node.type === "inlineCode" && node.value) {
        node.value = replaceEnvVars(node.value);
      }
      // Links [text](url)
      if (node.type === "link" && node.url) {
        node.url = replaceEnvVars(node.url);
      }
    });
  };
}

const docsUrl = env.VITE_DOCS_SITE_URL;
const parsedUrl = docsUrl ? new URL(docsUrl) : null;
const siteOrigin = parsedUrl ? parsedUrl.origin : undefined;
const basePath = parsedUrl ? parsedUrl.pathname : "";

// Rehype plugin to prefix root-relative links with base path
function rehypeBaseLinks() {
  return (tree) => {
    if (!basePath) return;
    visit(tree, "element", (node) => {
      if (node.tagName === "a" && node.properties?.href) {
        const href = node.properties.href;
        // Prefix root-relative links (start with / but not //) that don't already have base
        if (
          typeof href === "string" &&
          href.startsWith("/") &&
          !href.startsWith("//") &&
          !href.startsWith(basePath)
        ) {
          node.properties.href = basePath + href;
        }
      }
    });
  };
}

// https://astro.build/config
export default defineConfig({
  site: siteOrigin,
  base: basePath,
  outDir: `./dist${basePath}`,
  markdown: {
    remarkPlugins: [remarkEnvReplace],
    rehypePlugins: [rehypeBaseLinks],
  },
  vite: {
    plugins: [
      codecovVitePlugin({
        enableBundleAnalysis: process.env.CODECOV_TOKEN !== undefined,
        bundleName: "website-docs",
        uploadToken: process.env.CODECOV_TOKEN,
      }),
    ],
    server: {
      proxy: {
        "/auth": {
          target: env.VITE_GATEWAY_URL || "http://localhost:18090",
          changeOrigin: true,
        },
        "/graphql": {
          target: env.VITE_GATEWAY_URL || "http://localhost:18090",
          changeOrigin: true,
          ws: true,
        },
      },
    },
  },
  integrations: [
    sitemap(),
    mermaid({
      theme: "dark",
      autoTheme: true,
    }),
    svelte(),
    starlight({
      title: "FrameWorks Docs",
      plugins: [
        starlightBlog({
          authors: {
            frameworks: {
              name: "FrameWorks Team",
            },
          },
        }),
      ],
      components: {
        Hero: "./src/components/Hero.astro",
        Header: "./src/components/Header.astro",
        MobileMenuFooter: "./src/components/MobileMenuFooter.astro",
        PageFrame: "./src/components/DocsPageFrame.astro",
        ThemeProvider: "./src/components/ThemeProvider.astro",
      },
      social: [
        {
          icon: "github",
          label: "GitHub",
          href: "https://github.com/livepeer-frameworks/monorepo",
        },
      ],
      customCss: [
        // Relative path to your custom CSS file
        "./src/styles/custom.css",
      ],
      sidebar: [
        {
          label: "For Streamers",
          autogenerate: { directory: "streamers" },
        },
        {
          label: "For Agents",
          autogenerate: { directory: "agents" },
        },
        {
          label: "For Hybrid Operators",
          autogenerate: { directory: "hybrid" },
        },
        {
          label: "For Infrastructure Operators",
          autogenerate: { directory: "operators" },
        },
        {
          label: "Roadmap",
          slug: "roadmap",
        },
      ],
    }),
  ],
});
