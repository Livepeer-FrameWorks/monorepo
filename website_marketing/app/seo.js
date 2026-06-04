import {
  API_SITE_URL,
  APP_SITE_URL,
  BRAND_NAME,
  DISCORD_URL,
  DOCS_SITE_URL,
  FORUM_URL,
  GITHUB_URL,
  LIVEPEER_URL,
  MARKETING_ROUTES,
  MARKETING_SITE_URL,
  TWITTER_URL,
} from "@frameworks/site-config";

const socialImagePath = "/og-card.png";
const logoPath = "/frameworks-dark-logomark.png";

export const routeSeo = Object.fromEntries(MARKETING_ROUTES.map((route) => [route.id, route]));

export function absoluteUrl(path = "/") {
  return new URL(path, MARKETING_SITE_URL).toString();
}

function socialImageUrl() {
  return absoluteUrl(socialImagePath);
}

function routeUrl(route) {
  return absoluteUrl(route.path);
}

function organizationJsonLd() {
  return {
    "@context": "https://schema.org",
    "@type": "Organization",
    "@id": `${MARKETING_SITE_URL}/#organization`,
    name: BRAND_NAME,
    url: MARKETING_SITE_URL,
    logo: absoluteUrl(logoPath),
    sameAs: [GITHUB_URL, DISCORD_URL, FORUM_URL, TWITTER_URL, LIVEPEER_URL],
  };
}

function serviceJsonLd() {
  return {
    "@context": "https://schema.org",
    "@type": "SoftwareApplication",
    "@id": `${MARKETING_SITE_URL}/#software`,
    name: BRAND_NAME,
    applicationCategory: "MultimediaApplication",
    operatingSystem: "Linux, Web",
    url: MARKETING_SITE_URL,
    image: socialImageUrl(),
    description: routeSeo.home.description,
    offers: {
      "@type": "Offer",
      url: absoluteUrl("/pricing"),
      priceCurrency: "EUR",
      availability: "https://schema.org/InStock",
    },
    publisher: {
      "@id": `${MARKETING_SITE_URL}/#organization`,
    },
  };
}

function websiteJsonLd() {
  return {
    "@context": "https://schema.org",
    "@type": "WebSite",
    "@id": `${MARKETING_SITE_URL}/#website`,
    name: BRAND_NAME,
    url: MARKETING_SITE_URL,
    publisher: {
      "@id": `${MARKETING_SITE_URL}/#organization`,
    },
  };
}

export function faqJsonLd(path, faqs) {
  return {
    "@context": "https://schema.org",
    "@type": "FAQPage",
    "@id": `${absoluteUrl(path)}#faq`,
    mainEntity: faqs.map((faq) => ({
      "@type": "Question",
      name: faq.question,
      acceptedAnswer: {
        "@type": "Answer",
        text: faq.answer,
      },
    })),
  };
}

export function baseMeta(routeId) {
  const route = routeSeo[routeId] ?? routeSeo.home;
  const url = routeUrl(route);
  const image = socialImageUrl();

  const descriptors = [
    { title: route.title },
    { name: "description", content: route.description },
    { tagName: "link", rel: "canonical", href: url },
    { property: "og:site_name", content: BRAND_NAME },
    { property: "og:type", content: "website" },
    { property: "og:url", content: url },
    { property: "og:title", content: route.title },
    { property: "og:description", content: route.description },
    { property: "og:image", content: image },
    { property: "og:image:width", content: "1200" },
    { property: "og:image:height", content: "630" },
    { name: "twitter:card", content: "summary_large_image" },
    { name: "twitter:title", content: route.title },
    { name: "twitter:description", content: route.description },
    { name: "twitter:image", content: image },
  ];

  if (routeId === "home") {
    descriptors.push(
      { "script:ld+json": organizationJsonLd() },
      { "script:ld+json": serviceJsonLd() },
      { "script:ld+json": websiteJsonLd() }
    );
  }

  return descriptors;
}

export const discoveryLinks = {
  app: APP_SITE_URL,
  api: API_SITE_URL,
  docs: DOCS_SITE_URL,
  marketing: MARKETING_SITE_URL,
};
