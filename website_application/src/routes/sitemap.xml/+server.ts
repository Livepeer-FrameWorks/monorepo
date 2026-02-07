import { env } from "$env/dynamic/private";

const publicRoutes = [
  "/login",
  "/register",
  "/verify-email",
  "/forgot-password",
  "/reset-password",
];

export function GET() {
  const origin = (env.WEBAPP_PUBLIC_URL || "https://frameworks.network/app").replace(/\/$/, "");
  const urls = publicRoutes.map((route) => `  <url><loc>${origin}${route}</loc></url>`).join("\n");

  return new Response(
    `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
${urls}
</urlset>`,
    {
      headers: {
        "Content-Type": "application/xml",
      },
    }
  );
}
