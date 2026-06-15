import { Section, SectionContainer } from "@/components/ui/section";
import config from "../../config";
import { MarketingHero, MarketingScrollProgress, SectionDivider } from "@/components/marketing";

const privacySections = [
  {
    title: "Information we collect",
    items: [
      "Account details you provide such as name, email address, company, and authentication metadata.",
      "Operational telemetry—usage logs, health metrics, and error diagnostics—to keep the platform reliable.",
      "Streaming metadata such as stream configuration, routing decisions, and aggregate viewer and quality-of-experience analytics generated as your streams are delivered.",
      "Billing information needed to manage subscriptions and usage; payment card details are handled by our payment processor and are not stored on FrameWorks servers.",
      "Support communications and feedback you submit through tickets, email, or chat.",
    ],
  },
  {
    title: "What we do not collect",
    items: [
      "We do not inspect or mine the contents of your live or recorded media for advertising or profiling.",
      "We do not sell, rent, or trade your personal data or your viewers' data to anyone.",
      "Self-hosted and hybrid deployments keep your media and much of your operational data on infrastructure you control—by design, we never need a copy to deliver the service.",
    ],
  },
  {
    title: "How we use it",
    items: [
      "To operate and secure FrameWorks, troubleshoot issues, and improve performance.",
      "To produce the analytics and routing visibility you see in your own dashboards.",
      "To send product or policy updates you opt into during beta.",
      "To enforce our terms and acceptable use policy, and to detect and prevent abuse.",
    ],
  },
  {
    title: "How we share it",
    items: [
      "We do not sell personal data.",
      "We may share it with trusted vendors who help us run FrameWorks (for example hosting, analytics, email) under confidentiality agreements, limited to what they need to provide their service.",
      "We may disclose it if required by law or to protect the safety and rights of our users.",
    ],
  },
  {
    title: "Cookies & analytics",
    items: [
      "We use a small number of strictly necessary cookies to keep you signed in and to remember your preferences.",
      "Any product analytics we collect are aggregated and used to understand how features are used, not to build advertising profiles.",
      "You can block non-essential cookies in your browser; the core application will continue to work.",
    ],
  },
  {
    title: "Your rights & control",
    items: [
      "Depending on your location, you may have rights to access, correct, export, or delete your personal data, and to object to or restrict certain processing.",
      `You can exercise these rights, or request access or deletion, by emailing ${config.contactEmail ?? "privacy@frameworks.network"}. Deleting certain data may disable your account.`,
      "We keep data only as long as necessary for the purposes above or to comply with legal requirements, after which it is deleted or anonymised.",
    ],
  },
  {
    title: "Security & data sovereignty",
    items: [
      "We protect data in transit and at rest, scope internal access to what each role needs, and follow the principle of least privilege across our services.",
      "FrameWorks is built around sovereignty: you can run hosted, hybrid, or fully self-hosted, choosing where your media and control plane live so your data stays under your jurisdiction and ownership.",
      "We will update this policy as the product evolves; continuing to use FrameWorks means you accept the latest version.",
    ],
  },
];

const privacyHeroAccents = [
  {
    kind: "beam",
    x: 22,
    y: 38,
    width: "clamp(20rem, 40vw, 32rem)",
    height: "clamp(16rem, 28vw, 24rem)",
    rotate: -18,
    fill: "linear-gradient(140deg, rgba(100, 140, 220, 0.32), rgba(26, 34, 60, 0.22))",
    opacity: 0.52,
    radius: "46px",
  },
  {
    kind: "beam",
    x: 72,
    y: 32,
    width: "clamp(18rem, 34vw, 28rem)",
    height: "clamp(14rem, 24vw, 20rem)",
    rotate: 14,
    fill: "linear-gradient(155deg, rgba(60, 180, 240, 0.28), rgba(20, 26, 44, 0.18))",
    opacity: 0.48,
    radius: "40px",
  },
];

const PrivacyPage = () => (
  <div className="pt-16">
    <MarketingHero
      seed="/privacy"
      eyebrow="Legal"
      title="Privacy Policy"
      description="How we handle data during the FrameWorks public beta."
      align="center"
      accents={privacyHeroAccents}
    />

    <SectionDivider />

    <Section className="bg-brand-surface">
      <SectionContainer className="max-w-3xl space-y-12">
        {privacySections.map((section) => (
          <div key={section.title} className="space-y-4">
            <h2 className="text-xl font-semibold text-foreground">{section.title}</h2>
            <ul className="space-y-3 text-sm leading-relaxed text-muted-foreground">
              {section.items.map((item) => (
                <li key={item} className="flex items-start gap-3">
                  <span className="mt-[6px] h-2 w-2 rounded-full bg-accent flex-shrink-0" />
                  <span>{item}</span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </SectionContainer>
    </Section>

    <MarketingScrollProgress />
  </div>
);

export default PrivacyPage;
