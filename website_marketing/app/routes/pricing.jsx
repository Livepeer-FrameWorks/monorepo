import Pricing, { PRICING_FAQS } from "../../src/components/pages/Pricing";
import { baseMeta, faqJsonLd } from "../seo";

export function meta() {
  return [...baseMeta("pricing"), { "script:ld+json": faqJsonLd("/pricing", PRICING_FAQS) }];
}

export default Pricing;
