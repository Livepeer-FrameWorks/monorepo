import { Section, SectionContainer } from "@/components/ui/section";
import config from "../../config";
import { MarketingHero, MarketingScrollProgress, SectionDivider } from "@/components/marketing";

const termsSections = [
  {
    title: "1. Accounts & Security",
    paragraphs: [
      "You are responsible for all activity under your account, including keeping your credentials secure and contact details up to date.",
      "API tokens, stream keys, and signing secrets grant access to your infrastructure and content. Treat them like passwords, rotate them if you suspect exposure, and scope them to the minimum access each integration needs.",
      "Please notify us promptly if you suspect unauthorized access or a security incident involving your account or infrastructure, so we can take steps to limit impact insofar possible.",
    ],
  },
  {
    title: "2. Acceptable Use",
    paragraphs: [
      <>
        You must comply with the{" "}
        <a href="/aup" className="text-accent underline">
          FrameWorks Acceptable Use Policy
        </a>
        .
      </>,
    ],
  },
  {
    title: "3. Fees & Billing",
    paragraphs: [
      "Recurring fees are charged up-front, and only become active once payment is completed successfully.",
      "For prepaid accounts, metered usage is deducted from your prepaid balance as usage is reported. For postpaid accounts, metered usage is invoiced after each billing period, with payment due 14 days after that period ends.",
      "If a payment is disputed or fails to go through, we will always reach out to you first. Should you fail to respond within two weeks, we may proceed to limit or suspend your account until settled.",
    ],
  },
  {
    title: "4. Warranties & Liability",
    paragraphs: [
      "We do our best to keep the platform online without interruptions, but it's possible that due to unforeseen circumstances downtime may occur. In such cases, we are not liable for any damages whatsoever.",
      "For subscription-based services, if there has been significant downtime, please reach out to us and we'll provide additional usage or services at no charge to compensate.",
      "For metered usage, during downtime no usage should be incurred. If somehow you were charged anyway, please reach out to us and we'll resolve the problem.",
    ],
  },
  {
    title: "5. Privacy & Data",
    paragraphs: [
      <>
        Your use of FrameWorks is also governed by{" "}
        <a href="/privacy" className="text-accent underline">
          our Privacy Policy
        </a>
        , which explains what we collect, how we use it, and the controls available to you.
      </>,
      "You retain ownership of the content you stream and the data you generate. We process your content as well as operational telemetry only to provide the service(s) requested by you.",
    ],
  },
  {
    title: "6. Changes & Contact",
    paragraphs: [
      "We may update these terms as FrameWorks evolves. Using the service means you agree to the version of these terms that is published at time of use.",
      `Questions about these terms? Reach out to us at ${config.contactEmail ?? "legal@frameworks.network"}.`,
    ],
  },
];

const termsHeroAccents = [
  {
    kind: "beam",
    x: 16,
    y: 42,
    width: "clamp(22rem, 42vw, 34rem)",
    height: "clamp(18rem, 30vw, 26rem)",
    rotate: -20,
    fill: "linear-gradient(135deg, rgba(90, 130, 210, 0.34), rgba(24, 32, 58, 0.24))",
    opacity: 0.54,
    radius: "48px",
  },
  {
    kind: "beam",
    x: 78,
    y: 28,
    width: "clamp(20rem, 38vw, 30rem)",
    height: "clamp(16rem, 26vw, 22rem)",
    rotate: 16,
    fill: "linear-gradient(150deg, rgba(65, 190, 245, 0.3), rgba(18, 24, 42, 0.2))",
    opacity: 0.5,
    radius: "42px",
  },
];

const TermsPage = () => (
  <div className="pt-16">
    <MarketingHero
      seed="/terms"
      eyebrow="Legal"
      title="Terms of Service"
      description="Terms governing your use of FrameWorks."
      align="center"
      accents={termsHeroAccents}
    />

    <SectionDivider />

    <Section className="bg-brand-surface">
      <SectionContainer className="max-w-3xl space-y-12">
        {termsSections.map((section) => (
          <div key={section.title} className="space-y-4">
            <h2 className="text-xl font-semibold text-foreground">{section.title}</h2>
            <div className="space-y-4 text-sm leading-relaxed text-muted-foreground">
              {section.paragraphs.map((paragraph, index) => (
                <p key={index}>{paragraph}</p>
              ))}
            </div>
          </div>
        ))}
      </SectionContainer>
    </Section>

    <MarketingScrollProgress />
  </div>
);

export default TermsPage;
