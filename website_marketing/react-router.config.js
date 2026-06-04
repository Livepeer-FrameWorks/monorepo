const marketingRoutes = [
  "/",
  "/pricing",
  "/about",
  "/contact",
  "/status",
  "/privacy",
  "/terms",
  "/aup",
  "/sitemap.xml",
];

export default {
  ssr: false,
  prerender: marketingRoutes,
};
