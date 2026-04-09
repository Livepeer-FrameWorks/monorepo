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

function requireEnv(name) {
  const value = import.meta.env[name];
  if (!value) {
    throw new Error(`${name} is required`);
  }
  return value;
}

// Deployment-owned values from VITE_*, product constants from site-config.
const marketingUrl = requireEnv("VITE_MARKETING_SITE_URL");
const domain = new URL(marketingUrl).hostname;

const config = {
  appUrl: requireEnv("VITE_APP_URL"),
  docsUrl: requireEnv("VITE_DOCS_SITE_URL"),
  contactApiUrl: requireEnv("VITE_CONTACT_API_URL"),
  contactEmail: requireEnv("VITE_CONTACT_EMAIL"),
  turnstileSiteKey: import.meta.env.VITE_TURNSTILE_FORMS_SITE_KEY ?? "",
  gatewayUrl: requireEnv("VITE_GRAPHQL_HTTP_URL"),
  mcpUrl: requireEnv("VITE_MCP_URL"),

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

export default config;
