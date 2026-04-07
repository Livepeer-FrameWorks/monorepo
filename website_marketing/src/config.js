import {
  BRAND_NAME,
  GITHUB_URL,
  LIVEPEER_URL,
  LIVEPEER_EXPLORER_URL,
  FORUM_URL,
  DISCORD_URL,
  TWITTER_URL,
  DEMO_STREAM_NAME,
} from "@frameworks/site-config";

// Deployment-owned values from VITE_*, product constants from site-config.
const marketingUrl = import.meta.env.VITE_MARKETING_SITE_URL ?? "";
const domain = marketingUrl ? new URL(marketingUrl).hostname : "";

const config = {
  appUrl: import.meta.env.VITE_APP_URL ?? "",
  docsUrl: import.meta.env.VITE_DOCS_SITE_URL ?? "",
  contactApiUrl: import.meta.env.VITE_CONTACT_API_URL ?? "",
  contactEmail: import.meta.env.VITE_CONTACT_EMAIL ?? "",
  turnstileSiteKey: import.meta.env.VITE_TURNSTILE_FORMS_SITE_KEY ?? "",
  gatewayBaseUrl: import.meta.env.VITE_GATEWAY_URL ?? "",

  githubUrl: GITHUB_URL,
  livepeerUrl: LIVEPEER_URL,
  livepeerExplorerUrl: LIVEPEER_EXPLORER_URL,
  forumUrl: FORUM_URL,
  discordUrl: DISCORD_URL,
  twitterUrl: TWITTER_URL,
  demoStreamName: DEMO_STREAM_NAME,
  companyName: BRAND_NAME,
  domain,
};

const gatewayBase = config.gatewayBaseUrl.replace(/\/$/, "");
config.gatewayUrl = gatewayBase ? `${gatewayBase}/graphql` : "/graphql";
config.mcpUrl = gatewayBase ? `${gatewayBase}/mcp` : "/mcp";

export default config;
