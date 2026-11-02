import { copyFile } from "node:fs/promises";
import { join } from "node:path";
import { customerStories, customerStoryPath } from "./src/lib/customerStories.js";

const marketingRoutes = [
  "/",
  "/analytics",
  "/pricing",
  "/about",
  "/showcase",
  "/stream-test",
  "/contact",
  "/status",
  "/network",
  "/privacy",
  "/terms",
  "/aup",
  "/security",
  "/sitemap.xml",
];

// "/404" renders the catch-all route. `serve` answers unknown paths with
// 404.html from the site root, so the prerendered page is copied there.
const notFoundPath = "/404";

export default {
  ssr: false,
  prerender: [...marketingRoutes, notFoundPath, ...customerStories.map(customerStoryPath)],
  async buildEnd({ reactRouterConfig }) {
    const clientDir = join(reactRouterConfig.buildDirectory, "client");
    await copyFile(join(clientDir, "404", "index.html"), join(clientDir, "404.html"));
  },
};
