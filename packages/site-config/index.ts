// Shared by all three webapps. Deployment-specific values live in the
// VITE_* generated surface (see pkg/configgen); these are fixed.

export const BRAND_NAME = "FrameWorks";

export const GITHUB_URL = "https://github.com/livepeer-frameworks/monorepo";
export const LIVEPEER_URL = "https://livepeer.org";
export const LIVEPEER_EXPLORER_URL = "https://explorer.livepeer.org";
export const FORUM_URL = "https://forum.frameworks.network";
export const DISCORD_URL = "https://discord.gg/9J6haUjdAq";
export const DISCORD_GUILD_ID = "1396822950421991495";
export const TWITTER_URL = "https://x.com/GetFrames";

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
