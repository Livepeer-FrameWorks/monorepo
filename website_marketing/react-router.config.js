const marketingRoutes = [
  "/",
  "/analytics",
  "/pricing",
  "/about",
  "/contact",
  "/status",
  "/privacy",
  "/terms",
  "/aup",
  "/security",
  "/sitemap.xml",
];

export default {
  ssr: false,
  prerender: marketingRoutes,
};
