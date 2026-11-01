import { customerStories, customerStoryPath } from "./src/lib/customerStories.js";

const marketingRoutes = [
  "/",
  "/analytics",
  "/pricing",
  "/about",
  "/showcase",
  "/contact",
  "/status",
  "/network",
  "/privacy",
  "/terms",
  "/aup",
  "/security",
  "/sitemap.xml",
];

export default {
  ssr: false,
  prerender: [...marketingRoutes, ...customerStories.map(customerStoryPath)],
};
