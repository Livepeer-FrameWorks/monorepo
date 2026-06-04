import { MARKETING_ROUTES, MARKETING_SITE_URL } from "@frameworks/site-config";

// Prerendered resource route (see react-router.config.js). Generates the sitemap
// from MARKETING_ROUTES so it can never drift from the canonical route list /
// per-page SEO metadata that drives titles, descriptions, and the nav.
export function loader() {
  const lastmod = new Date().toISOString().slice(0, 10);

  const urls = MARKETING_ROUTES.map((route) => {
    const loc = new URL(route.path, MARKETING_SITE_URL).toString();
    return [
      "  <url>",
      `    <loc>${loc}</loc>`,
      `    <lastmod>${lastmod}</lastmod>`,
      `    <changefreq>${route.changefreq}</changefreq>`,
      `    <priority>${route.priority}</priority>`,
      "  </url>",
    ].join("\n");
  }).join("\n");

  const body = `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls}\n</urlset>\n`;

  return new Response(body, {
    headers: {
      "Content-Type": "application/xml",
      "Cache-Control": "public, max-age=3600",
    },
  });
}
