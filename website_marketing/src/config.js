// Environment configuration for the marketing website
const config = {
  // Application URLs
  appUrl: import.meta.env.VITE_APP_URL || 'http://localhost:9090',
  contactApiUrl: import.meta.env.VITE_CONTACT_API_URL || 'http://localhost:18032',

  // External URLs
  githubUrl: import.meta.env.VITE_GITHUB_URL || 'https://github.com/livepeer-frameworks/monorepo',
  livepeerUrl: import.meta.env.VITE_LIVEPEER_URL || 'https://livepeer.org',
  livepeerExplorerUrl: import.meta.env.VITE_LIVEPEER_EXPLORER_URL || 'https://explorer.livepeer.org',

  // Contact information
  contactEmail: import.meta.env.VITE_CONTACT_EMAIL || 'info@frameworks.network',

  // Community URLs
  forumUrl: import.meta.env.VITE_FORUM_URL || 'https://forum.frameworks.network',
  discordUrl: import.meta.env.VITE_DISCORD_URL || 'https://discord.gg/9J6haUjdAq',

  // Demo configuration
  demoStreamName: import.meta.env.VITE_DEMO_STREAM_NAME || 'live+frameworks-demo',

  // Company information
  companyName: import.meta.env.VITE_COMPANY_NAME || 'FrameWorks',
  domain: import.meta.env.VITE_DOMAIN || 'frameworks.network'
};

export default config;