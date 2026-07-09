import { Section, SectionContainer } from "@/components/ui/section";
import config from "../../config";
import { MarketingHero, MarketingScrollProgress, SectionDivider } from "@/components/marketing";

const termsSections = [
  {
    title: "1. Beta Service",
    paragraphs: [
      "FrameWorks is currently in public beta. Features, performance, and availability may change without notice while we prepare the platform for general availability.",
      "Because the service is still maturing, you should not rely on the beta as the sole delivery path for business-critical or revenue-generating streams without an independent fallback. We publish current platform and edge-cluster health on our status page so you can make that judgement before you go live.",
      "We may suspend, limit, or discontinue the beta at any time. Continuing to use FrameWorks means you accept the most recent version of these terms.",
    ],
  },
  {
    title: "2. Accounts & Security",
    paragraphs: [
      "You are responsible for all activity under your account, including keeping your credentials secure and contact details up to date.",
      "API tokens, stream keys, and signing secrets grant access to your infrastructure and content. Treat them like passwords, rotate them if you suspect exposure, and scope them to the minimum access each integration needs.",
      "Please notify us promptly if you suspect unauthorized access or a security incident involving your account or infrastructure.",
    ],
  },
  {
    title: "3. Acceptable Use",
    paragraphs: [
      "You must comply with all applicable laws, respect third-party rights, and follow the Acceptable Use Policy. Prohibited behaviour includes distributing illegal content, attempting to disrupt FrameWorks services, or interfering with other users.",
      "You are responsible for holding the rights to everything you ingest, transcode, record, and redistribute through FrameWorks, including any third-party music, footage, or trademarks contained in your streams.",
      "Violations may result in suspension or termination without refund.",
    ],
  },
  {
    title: "4. Fees & Beta Billing",
    paragraphs: [
      "During the public beta, metered streaming usage for bandwidth, processing, and storage is provided at no charge while we tune pricing for general availability. Any subscription or add-on fees are billed as described at checkout before you incur them.",
      "Account balances and top-ups are real and remain yours; they are applied against future usage and are not forfeited when the beta ends. We will give reasonable notice before metered usage becomes billable.",
    ],
  },
  {
    title: "5. No Warranties & Liability",
    paragraphs: [
      "The beta is provided “as is” and “as available.” We disclaim all warranties, express or implied, including fitness for a particular purpose, merchantability, and non-infringement.",
      "We do not warrant uninterrupted or error-free delivery during beta, and we are not liable for indirect, incidental, or consequential losses arising from your use of the service. Any liability we have to you is limited to the maximum extent permitted by law.",
    ],
  },
  {
    title: "6. Privacy & Data",
    paragraphs: [
      "Your use of FrameWorks is also governed by our Privacy Policy, which explains what we collect, how we use it, and the controls available to you. FrameWorks is built so you can keep ownership of your media and operate hosted, hybrid, or fully self-hosted without surrendering control of your pipeline.",
      "You retain ownership of the content you stream and the data you generate. We process operational telemetry only to run, secure, and improve the platform.",
    ],
  },
  {
    title: "7. Changes & Contact",
    paragraphs: [
      "We may update these terms as FrameWorks evolves. Using the service after updates are published means you agree to the revised version. Material changes will be reflected by an updated revision on this page.",
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
      description="Beta terms outlining how you can use FrameWorks today."
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
