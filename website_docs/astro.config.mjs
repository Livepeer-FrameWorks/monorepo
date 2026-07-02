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
import {
  GITHUB_URL,
  DISCORD_URL,
  TWITTER_URL,
  STREAMING_RTMP_PORT,
  STREAMING_SRT_PORT,
  STREAMING_RTMP_PATH,
  STREAMING_HLS_PATH,
  STREAMING_WEBRTC_PATH,
} from "@frameworks/site-config";

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
const gatewayUrl = env.VITE_GATEWAY_URL || "http://localhost:18000";
const gatewayWsUrl =
  env.VITE_GRAPHQL_WS_URL || `${gatewayUrl.replace(/^http/, "ws").replace(/\/$/, "")}/graphql/ws`;
const ingest = parseStreamingUrl(ingestUrl);
const edge = parseStreamingUrl(edgeUrl);

// Protocol defaults from site-config (not env)
const rtmpPort = STREAMING_RTMP_PORT;
const srtPort = STREAMING_SRT_PORT;
const rtmpPath = STREAMING_RTMP_PATH;
const hlsPath = STREAMING_HLS_PATH;
const webrtcPath = STREAMING_WEBRTC_PATH;

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
  GATEWAY_URL: gatewayUrl,
  GATEWAY_WS_URL: gatewayWsUrl,
  API_URL: gatewayUrl,
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
  DISCORD_URL,
  GITHUB_URL,
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
  redirects: {
    "/streamers/quick-start": "/builders/quick-start",
    "/streamers/encoder-setup": "/builders/encoder-setup",
    "/streamers/integration-guide": "/builders/integration-guide",
    "/streamers/ingest": "/builders/ingest",
    "/streamers/ingest-react": "/builders/ingest-react",
    "/streamers/ingest-svelte": "/builders/ingest-svelte",
    "/streamers/ingest-wc": "/builders/ingest-wc",
    "/streamers/ingest-vanilla": "/builders/ingest-vanilla",
    "/streamers/ingest-compositor": "/builders/ingest-compositor",
    "/streamers/ingest-advanced": "/builders/ingest-advanced",
    "/streamers/playback": "/builders/playback",
    "/streamers/playback-react": "/builders/playback-react",
    "/streamers/playback-svelte": "/builders/playback-svelte",
    "/streamers/playback-wc": "/builders/playback-wc",
    "/streamers/playback-vanilla": "/builders/playback-vanilla",
    "/streamers/playback-theming": "/builders/playback-theming",
    "/streamers/playback-advanced": "/builders/playback-advanced",
    "/streamers/multistreaming": "/builders/multistreaming",
    "/streamers/recordings": "/builders/recordings",
    "/streamers/api-reference": "/builders/api-reference",
    "/streamers/billing": "/builders/billing",
    "/streamers/troubleshooting": "/builders/troubleshooting",
  },
  markdown: {
    remarkPlugins: [remarkEnvReplace],
    rehypePlugins: [rehypeBaseLinks],
  },
  vite: {
    resolve: {
      noExternal: ["svelte-turnstile"],
    },
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
      favicon: "/favicon.svg",
      head: [
        {
          tag: "link",
          attrs: { rel: "icon", type: "image/png", sizes: "48x48", href: "/favicon-48.png" },
        },
        {
          tag: "link",
          attrs: { rel: "icon", type: "image/png", sizes: "96x96", href: "/favicon-96.png" },
        },
        {
          tag: "link",
          attrs: { rel: "icon", type: "image/png", sizes: "192x192", href: "/favicon-192.png" },
        },
        {
          tag: "link",
          attrs: { rel: "apple-touch-icon", sizes: "180x180", href: "/apple-touch-icon.png" },
        },
        { tag: "link", attrs: { rel: "manifest", href: "/site.webmanifest" } },
        // og:image must be an absolute URL, so it can only be emitted when the
        // site origin is known (production builds always set VITE_DOCS_SITE_URL).
        ...(siteOrigin
          ? [
              {
                tag: "meta",
                attrs: {
                  property: "og:image",
                  content: new URL(`${basePath}/og-card.png`.replace("//", "/"), siteOrigin).href,
                },
              },
              { tag: "meta", attrs: { property: "og:image:width", content: "1200" } },
              { tag: "meta", attrs: { property: "og:image:height", content: "630" } },
              { tag: "meta", attrs: { name: "twitter:card", content: "summary_large_image" } },
            ]
          : []),
      ],
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
          href: GITHUB_URL,
        },
        {
          icon: "x.com",
          label: "X (Twitter)",
          href: TWITTER_URL,
        },
        {
          icon: "discord",
          label: "Discord",
          href: DISCORD_URL,
        },
      ],
      customCss: [
        // Relative path to your custom CSS file
        "./src/styles/custom.css",
      ],
      sidebar: [
        {
          label: "Platform",
          items: [{ label: "Capabilities", slug: "platform/feature-matrix" }, { slug: "roadmap" }],
        },
        {
          label: "Migrate",
          autogenerate: { directory: "migrate" },
        },
        {
          label: "For Builders",
          items: [
            { slug: "builders/quick-start" },
            { slug: "builders/streams" },
            { slug: "builders/encoder-setup" },
            { slug: "builders/integration-guide" },
            {
              label: "StreamCrafter (Ingest)",
              items: [
                { slug: "builders/ingest" },
                { slug: "builders/ingest-react" },
                { slug: "builders/ingest-svelte" },
                { slug: "builders/ingest-wc" },
                { slug: "builders/ingest-vanilla" },
                { slug: "builders/ingest-compositor" },
                { slug: "builders/ingest-advanced" },
              ],
            },
            {
              label: "Player (Playback)",
              items: [
                { slug: "builders/playback" },
                { slug: "builders/playback-react" },
                { slug: "builders/playback-svelte" },
                { slug: "builders/playback-wc" },
                { slug: "builders/playback-vanilla" },
                { slug: "builders/playback-theming" },
                { slug: "builders/playback-advanced" },
              ],
            },
            { slug: "builders/multistreaming" },
            { slug: "builders/pull-streams" },
            { slug: "builders/thumbnails-and-previews" },
            { slug: "builders/recordings" },
            { slug: "builders/clips" },
            { slug: "builders/dvr-chapters" },
            { slug: "builders/storage-and-retention" },
            { slug: "builders/playback-access-control" },
            { slug: "builders/api-reference" },
            { slug: "builders/analytics-api" },
            { slug: "builders/billing" },
            { slug: "builders/wallet-and-crypto" },
            { slug: "builders/troubleshooting" },
          ],
        },
        {
          label: "For Agents",
          autogenerate: { directory: "agents" },
        },
        {
          label: "For Selfhosters",
          autogenerate: { directory: "hybrid" },
        },
        {
          label: "For Infrastructure Operators",
          items: [
            {
              label: "Start Here",
              items: [
                { slug: "operators/deployment" },
                { slug: "operators/prerequisites" },
                { slug: "operators/cluster-manifest" },
                { slug: "operators/cli-reference" },
              ],
            },
            {
              label: "Architecture",
              items: [{ slug: "operators/architecture" }, { slug: "operators/data-models" }],
            },
            {
              label: "Deployment & Lifecycle",
              items: [
                { slug: "operators/ansible-provisioning" },
                { slug: "operators/deployment-manual" },
                { slug: "operators/running-upgrades" },
                { slug: "operators/postgres-migrations" },
                { slug: "operators/clickhouse-migrations" },
              ],
            },
            {
              label: "Networking",
              items: [
                { slug: "operators/dns" },
                { slug: "operators/mesh-bootstrap" },
                { slug: "operators/multi-cluster" },
              ],
            },
            {
              label: "Operations",
              items: [
                { slug: "operators/operations" },
                { slug: "operators/media-storage" },
                { slug: "operators/dvr" },
                { slug: "operators/external-services" },
              ],
            },
            {
              label: "Integrations",
              items: [
                { slug: "operators/chatwoot-setup" },
                { slug: "operators/listmonk-setup" },
                { slug: "operators/skipper" },
              ],
            },
          ],
        },
      ],
    }),
  ],
});
