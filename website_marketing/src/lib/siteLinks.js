// One definition of the site's links, read by the header (desktop and mobile)
// and the footer, so a page is added or renamed in one place.

const trimSlash = (url) => String(url ?? "").replace(/\/+$/, "");

export const docsLink = (docsUrl, path = "") => `${trimSlash(docsUrl)}${path}`;

// Signup links carry where on the site the visitor clicked (utm_content) and,
// for plan cards, the plan they picked. The register page stores the utm
// fields as signup attribution.
export function signupUrl(appUrl, cta, { plan } = {}) {
  const params = new URLSearchParams({ utm_source: "marketing", utm_content: cta });
  if (plan) params.set("plan", plan);
  return `${trimSlash(appUrl)}/register?${params.toString()}`;
}

export function siteLinks({ docsUrl, githubUrl }) {
  return {
    home: { id: "home", label: "Home", to: "/" },
    about: { id: "about", label: "About", to: "/about" },
    showcase: { id: "showcase", label: "Showcase", to: "/showcase" },
    pricing: { id: "pricing", label: "Pricing", to: "/pricing" },
    network: { id: "network", label: "Network", to: "/network" },
    analytics: { id: "analytics", label: "Analytics", to: "/analytics" },
    contact: { id: "contact", label: "Contact", to: "/contact" },
    status: {
      id: "status",
      label: "Status",
      to: "/status",
      description: "Platform and edge cluster health",
    },
    streamTest: {
      id: "stream-test",
      label: "Stream test",
      to: "/stream-test",
      description: "Play a stream or go live from your browser",
    },
    docs: { id: "docs", label: "Docs", href: docsLink(docsUrl, "/") },
    roadmap: {
      id: "roadmap",
      label: "Roadmap",
      href: docsLink(docsUrl, "/roadmap"),
      description: "What is planned, by area",
    },
    skipper: {
      id: "skipper",
      label: "Skipper",
      href: docsLink(docsUrl, "/agents/skipper"),
      description: "The platform agent in your dashboard",
    },
    blog: {
      id: "blog",
      label: "Blog",
      href: docsLink(docsUrl, "/blog"),
      description: "Release posts and engineering notes",
    },
    github: { id: "github", label: "GitHub", href: githubUrl, external: true },
    privacy: { id: "privacy", label: "Privacy", to: "/privacy" },
    terms: { id: "terms", label: "Terms", to: "/terms" },
    aup: { id: "aup", label: "AUP", to: "/aup" },
  };
}

// Header order. An entry with `children` is the Resources menu: a dropdown on
// desktop and a labelled group on mobile.
export function headerNav(config) {
  const l = siteLinks(config);
  return [
    l.home,
    l.about,
    l.showcase,
    l.pricing,
    l.network,
    l.docs,
    l.contact,
    {
      id: "resources",
      label: "Resources",
      children: [l.roadmap, l.streamTest, l.status, l.skipper, l.blog],
    },
    l.github,
  ];
}

export function footerColumns(config) {
  const l = siteLinks(config);
  return [
    {
      id: "product",
      title: "Product",
      links: [l.showcase, l.streamTest, l.analytics, l.network, l.pricing],
    },
    { id: "company", title: "Company", links: [l.about, l.contact] },
    {
      id: "resources",
      title: "Resources",
      links: [{ ...l.docs, label: "Documentation" }, l.roadmap, l.blog, l.status, l.github],
    },
    { id: "legal", title: "Legal", links: [l.privacy, l.terms, l.aup] },
  ];
}

// True when the current path is the link's route or, for a menu, one of its
// children's routes.
export function isActiveLink(link, pathname) {
  if (link.children) return link.children.some((child) => isActiveLink(child, pathname));
  return Boolean(link.to && link.to === pathname);
}
