import { Link } from "react-router";
import config from "../../config";
import { MarketingHero } from "@/components/marketing";
import { siteLinks } from "@/lib/siteLinks";

const NotFound = () => {
  const links = siteLinks(config);
  const suggestions = [links.pricing, links.streamTest, links.network, links.docs, links.contact];

  return (
    <div className="pt-16">
      <MarketingHero
        seed="/404"
        eyebrow="404"
        title="This page does not exist"
        description="The link may be old, or the address may have a typo. The showcase lists everything FrameWorks does today and what comes next."
        align="center"
        surface="gradient"
        surfaceTone="accent"
        surfaceIntensity="raised"
        primaryAction={{ label: "See the showcase", to: "/showcase", className: "cta-motion" }}
        secondaryAction={{
          label: "Go to the home page",
          to: "/",
          className: "cta-motion",
          variant: "secondary",
        }}
        support={
          <nav className="showcase-jump" aria-label="Popular pages">
            {suggestions.map((link) =>
              link.to ? (
                <Link key={link.id} to={link.to} className="showcase-jump__link">
                  {link.label}
                </Link>
              ) : (
                <a key={link.id} href={link.href} className="showcase-jump__link">
                  {link.label}
                </a>
              )
            )}
          </nav>
        }
      />
    </div>
  );
};

export default NotFound;
