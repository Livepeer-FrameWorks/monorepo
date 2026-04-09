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

export const DEMO_STREAM_NAME = "live+frameworks-demo";

// Protocol defaults — standard ports/paths baked into MistServer and edge nodes.
export const STREAMING_RTMP_PORT = "1935";
export const STREAMING_SRT_PORT = "8889";
export const STREAMING_RTMP_PATH = "/live";
export const STREAMING_HLS_PATH = "/hls";
export const STREAMING_WEBRTC_PATH = "/webrtc";
export const STREAMING_EMBED_PATH = "/";
