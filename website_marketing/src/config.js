// Environment configuration for the marketing website
const config = {
  // Application URLs
  appUrl: import.meta.env.VITE_APP_URL ?? "",
  docsUrl: import.meta.env.VITE_DOCS_SITE_URL ?? "",
  contactApiUrl: import.meta.env.VITE_CONTACT_API_URL ?? "",

  // External URLs
  githubUrl: import.meta.env.VITE_GITHUB_URL ?? "",
  livepeerUrl: import.meta.env.VITE_LIVEPEER_URL ?? "",
  livepeerExplorerUrl: import.meta.env.VITE_LIVEPEER_EXPLORER_URL ?? "",

  // Contact information
  contactEmail: import.meta.env.VITE_CONTACT_EMAIL ?? "",

  // Community URLs
  forumUrl: import.meta.env.VITE_FORUM_URL ?? "",
  discordUrl: import.meta.env.VITE_DISCORD_URL ?? "",
  twitterUrl: import.meta.env.VITE_TWITTER_URL ?? "",

  // Demo configuration
  demoStreamName: import.meta.env.VITE_DEMO_STREAM_NAME ?? "",

  // Turnstile (Forms)
  turnstileSiteKey: import.meta.env.VITE_TURNSTILE_FORMS_SITE_KEY ?? "",

  // Gateway base URL (domain + proxy subpath, no endpoint path)
  // In dev: use relative path so Vite proxy can intercept
  // In prod: set VITE_GATEWAY_URL to base URL (e.g., https://api.example.com)
  gatewayBaseUrl: import.meta.env.VITE_GATEWAY_URL ?? "",

  // Company information
  companyName: import.meta.env.VITE_COMPANY_NAME ?? "",
  domain: import.meta.env.VITE_DOMAIN ?? "",
};

const gatewayBase = config.gatewayBaseUrl.replace(/\/$/, "");
config.gatewayUrl = gatewayBase ? `${gatewayBase}/graphql` : "/graphql";
config.mcpUrl = gatewayBase ? `${gatewayBase}/mcp` : "/mcp";

export default config;
