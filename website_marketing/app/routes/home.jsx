import LandingPage, { HOME_FAQS } from "../../src/components/pages/LandingPage";
import { baseMeta, faqJsonLd } from "../seo";

export function meta() {
  return [...baseMeta("home"), { "script:ld+json": faqJsonLd("/", HOME_FAQS) }];
}

export default LandingPage;
