import {
  BRAND_NAME,
  CONTACT_EMAIL,
  APP_SITE_URL,
  DOCS_SITE_URL,
  API_SITE_URL,
  FORMS_SITE_URL,
  MARKETING_SITE_URL,
  GITHUB_URL,
  LIVEPEER_URL,
  LIVEPEER_EXPLORER_URL,
  FORUM_URL,
  DISCORD_URL,
  TWITTER_URL,
  DEMO_STREAM_NAME,
  DEMO_FIXTURES,
} from "@frameworks/site-config";

function publicConfig(name, fallback) {
  const value = import.meta.env[name];
  if (typeof value === "string" && value.trim() !== "") {
    return value;
  }
  if (!fallback) {
    throw new Error(`${name} is required`);
  }
  return fallback;
}

// Public defaults come from site-config; VITE_* overrides let local and
// provisioned deployments point the static bundle at environment-specific origins.
const marketingUrl = publicConfig("VITE_MARKETING_SITE_URL", MARKETING_SITE_URL);
const domain = new URL(marketingUrl).hostname;
const apiUrl = publicConfig("VITE_GRAPHQL_HTTP_URL", `${API_SITE_URL}/graphql`);

const config = {
  appUrl: publicConfig("VITE_APP_URL", APP_SITE_URL),
  docsUrl: publicConfig("VITE_DOCS_SITE_URL", DOCS_SITE_URL),
  contactApiUrl: publicConfig("VITE_CONTACT_API_URL", FORMS_SITE_URL),
  contactEmail: publicConfig("VITE_CONTACT_EMAIL", CONTACT_EMAIL),
  turnstileSiteKey: import.meta.env.VITE_TURNSTILE_FORMS_SITE_KEY ?? "",
  gatewayUrl: apiUrl,
  mcpUrl: publicConfig("VITE_MCP_URL", `${API_SITE_URL}/mcp`),

  githubUrl: GITHUB_URL,
  livepeerUrl: LIVEPEER_URL,
  livepeerExplorerUrl: LIVEPEER_EXPLORER_URL,
  forumUrl: FORUM_URL,
  discordUrl: DISCORD_URL,
  twitterUrl: TWITTER_URL,
  demoStreamName: DEMO_STREAM_NAME,
  demoFixtures: DEMO_FIXTURES,
  companyName: BRAND_NAME,
  domain,
};

export default config;
