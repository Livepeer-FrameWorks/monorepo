// Shared by all three webapps. Deployment-specific values live in the
// VITE_* generated surface (see pkg/configgen); these are fixed.

export const BRAND_NAME = "FrameWorks";

export const MARKETING_SITE_URL = "https://frameworks.network";
export const APP_SITE_URL = "https://app.frameworks.network";
export const DOCS_SITE_URL = "https://logbook.frameworks.network";
export const API_SITE_URL = "https://bridge.frameworks.network";
export const FORMS_SITE_URL = "https://steward.frameworks.network";
export const CONTACT_EMAIL = "info@frameworks.network";

export const GITHUB_URL = "https://github.com/livepeer-frameworks/monorepo";
export const LIVEPEER_URL = "https://livepeer.org";
export const LIVEPEER_EXPLORER_URL = "https://explorer.livepeer.org";
export const FORUM_URL = "https://forum.frameworks.network";
export const DISCORD_URL = "https://discord.gg/9J6haUjdAq";
export const DISCORD_GUILD_ID = "1396822950421991495";
export const TWITTER_URL = "https://x.com/GetFrames";

export interface MarketingRouteSeo {
  id: string;
  path: string;
  title: string;
  description: string;
  changefreq: string;
  priority: string;
}

export const MARKETING_ROUTES: MarketingRouteSeo[] = [
  {
    id: "home",
    path: "/",
    title: "FrameWorks - Sovereign Live Streaming Platform, Hosted or Self-Hosted",
    description:
      "FrameWorks is a live streaming platform and API for sovereign video operations: MistServer delivery, Livepeer-backed transcoding, and real-time analytics. Start hosted, go hybrid, or run the whole stack self-hosted on your own infrastructure.",
    changefreq: "weekly",
    priority: "1.0",
  },
  {
    id: "analytics",
    path: "/analytics",
    title: "FrameWorks Analytics - Real-Time Streaming Telemetry, QoE & Geo Insights",
    description:
      "See why viewer X connected to edge Y. FrameWorks gives every plan real-time viewer geo, routing decisions, player QoE, VOD retention, and transparent usage analytics - your data, on your infrastructure.",
    changefreq: "weekly",
    priority: "0.9",
  },
  {
    id: "pricing",
    path: "/pricing",
    title: "FrameWorks Pricing - Hosted, Hybrid, and Self-Hosted Streaming",
    description:
      "Compare FrameWorks beta pricing for free, supporter, production, and enterprise streaming deployments with transparent allowances, hosted load balancing, and pay-as-you-go usage.",
    changefreq: "weekly",
    priority: "0.9",
  },
  {
    id: "about",
    path: "/about",
    title: "About FrameWorks - The Team Behind Sovereign Live Streaming",
    description:
      "Learn how FrameWorks builds sovereign live video infrastructure around MistServer, Livepeer, open operations, and self-hosted edge clusters.",
    changefreq: "monthly",
    priority: "0.8",
  },
  {
    id: "contact",
    path: "/contact",
    title: "Contact FrameWorks - Streaming Infrastructure Support",
    description:
      "Contact the FrameWorks team for self-hosted streaming deployments, hosted load balancing, enterprise live video infrastructure, and community support.",
    changefreq: "monthly",
    priority: "0.7",
  },
  {
    id: "status",
    path: "/status",
    title: "FrameWorks Status - Live Streaming Network Health",
    description:
      "View FrameWorks platform and edge cluster status for live streaming routing, ingest, playback, and infrastructure health.",
    changefreq: "daily",
    priority: "0.6",
  },
  {
    id: "privacy",
    path: "/privacy",
    title: "FrameWorks Privacy Policy",
    description:
      "Read the FrameWorks privacy policy for account, billing, streaming, analytics, and operational data.",
    changefreq: "yearly",
    priority: "0.3",
  },
  {
    id: "terms",
    path: "/terms",
    title: "FrameWorks Terms of Service",
    description:
      "Read the FrameWorks terms for using hosted, hybrid, and self-hosted live streaming services.",
    changefreq: "yearly",
    priority: "0.3",
  },
  {
    id: "aup",
    path: "/aup",
    title: "FrameWorks Acceptable Use Policy",
    description:
      "Read the FrameWorks acceptable use policy for live streaming, hosted infrastructure, self-hosted clusters, and platform accounts.",
    changefreq: "yearly",
    priority: "0.3",
  },
];

// Marketing landing demo fixtures. Each entry is a public playback_id whose
// upstream is declared in gitops bootstrap (commodore.pull_streams). The
// commodore bootstrap reconciler creates a row in commodore.streams with this
// exact playback_id, plus a sibling commodore.stream_pull_sources row with the
// configured source_uri. The landing-page player resolves these like any
// public stream — viewer arrives, /source picks the origin, Mist starts the pull.
//
// Adding a fixture: declare it in gitops bootstrap.yaml's commodore.pull_streams,
// then append the matching {id, label} entry here. Removing: delete from both.
export interface DemoFixture {
  id: string;
  label: string;
}

export const DEMO_FIXTURES: DemoFixture[] = [{ id: "frameworks-demo", label: "Demo" }];

// First fixture's playback_id, kept for callers that want a single string.
// Prefer DEMO_FIXTURES for any UI that wants to surface the toggle.
export const DEMO_STREAM_NAME = DEMO_FIXTURES[0].id;

// Protocol defaults — standard ports/paths baked into MistServer and edge nodes.
export const STREAMING_RTMP_PORT = "1935";
export const STREAMING_SRT_PORT = "8889";
export const STREAMING_RTMP_PATH = "/live";
export const STREAMING_HLS_PATH = "/hls";
export const STREAMING_WEBRTC_PATH = "/webrtc";
export const STREAMING_EMBED_PATH = "/";
