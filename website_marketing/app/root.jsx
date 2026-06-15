import {
  Links,
  Meta,
  Outlet,
  Scripts,
  ScrollRestoration,
  isRouteErrorResponse,
} from "react-router";
import Navigation from "../src/components/Navigation";
import Footer from "../src/components/Footer";
import ScrollToTop from "../src/components/shared/ScrollToTop";
import { baseMeta } from "./seo";
import "../src/index.css";
import "@livepeer-frameworks/player-react/player.css";

export const links = () => [
  { rel: "icon", type: "image/svg+xml", href: "/frameworks-dark-logomark.svg" },
  { rel: "icon", type: "image/png", sizes: "48x48", href: "/favicon-48.png" },
  { rel: "icon", type: "image/png", sizes: "96x96", href: "/favicon-96.png" },
  { rel: "icon", type: "image/png", sizes: "192x192", href: "/favicon-192.png" },
  { rel: "apple-touch-icon", sizes: "180x180", href: "/apple-touch-icon.png" },
  { rel: "manifest", href: "/site.webmanifest" },
  { rel: "preconnect", href: "https://fonts.googleapis.com" },
  { rel: "preconnect", href: "https://fonts.gstatic.com", crossOrigin: "anonymous" },
  {
    rel: "stylesheet",
    href: "https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;600;700&family=Inter:wght@300;400;500;600;700&display=swap",
  },
];

export function meta() {
  return baseMeta("home");
}

export function Layout({ children }) {
  return (
    <html lang="en">
      <head>
        <meta charSet="UTF-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1.0" />
        <Meta />
        <Links />
      </head>
      <body className="bg-background text-foreground">
        {children}
        <ScrollRestoration />
        <Scripts />
      </body>
    </html>
  );
}

export default function Root() {
  return (
    <>
      <ScrollToTop />
      <div className="App">
        <Navigation />
        <Outlet />
      </div>
      <Footer />
    </>
  );
}

export function ErrorBoundary({ error }) {
  const title = isRouteErrorResponse(error) ? `${error.status} ${error.statusText}` : "Route error";

  return (
    <main className="min-h-screen bg-background px-6 py-24 text-foreground">
      <div className="mx-auto max-w-3xl">
        <p className="mb-3 text-sm uppercase tracking-[0.18em] text-muted-foreground">FrameWorks</p>
        <h1 className="mb-4 text-4xl font-semibold">{title}</h1>
        <p className="text-muted-foreground">The requested marketing page could not be rendered.</p>
      </div>
    </main>
  );
}
