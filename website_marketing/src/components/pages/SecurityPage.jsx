import { Section, SectionContainer } from "@/components/ui/section";
import { GITHUB_URL } from "@frameworks/site-config";
import { MarketingHero, MarketingScrollProgress, SectionDivider } from "@/components/marketing";

const SECURITY_EMAIL = "security@frameworks.network";
const ADVISORIES_URL = `${GITHUB_URL}/security/advisories/new`;

const securitySections = [
  {
    title: "Reporting a vulnerability",
    paragraphs: [
      "Please do not open a public issue for a security vulnerability. Report it privately so we can fix it before disclosure.",
      "Preferred: open a private advisory via GitHub Private Vulnerability Reporting. Alternatively, email us with a description, steps to reproduce, and the potential impact.",
      "We acknowledge reports within 48 hours. When an issue is resolved we credit reporters in our release notes, unless you prefer to stay anonymous.",
    ],
  },
  {
    title: "Safe harbor",
    paragraphs: [
      "We consider security research conducted in good faith under this policy to be authorized, and we will not pursue or support legal action against you for accidental, good-faith violations, including under anti-hacking or anti-circumvention laws.",
      "If a third party brings action against you for activity that complied with this policy, we will make our authorization known. This safe harbor covers claims under our control and cannot bind third parties.",
    ],
  },
  {
    title: "Scope and rules of engagement",
    paragraphs: [
      "In scope: our web surfaces, the API gateway, and the open-source services in our repository. You may also test self-hosted deployments that you operate yourself.",
      "Never access, modify, or exfiltrate data belonging to any tenant other than a test account you control. Confirm access-control findings with your own resources. No denial of service, load testing, or disruption of live streams. No pushing media to ingest endpoints you do not own.",
      "Limit any data access to the minimum proof-of-concept needed to demonstrate an issue. If you encounter credentials, personal data, or keys, stop, do not save them, and tell us in your report.",
    ],
  },
  {
    title: "Disclosure",
    paragraphs: [
      "We practice coordinated disclosure: give us a reasonable window to remediate and check with us on timing before publishing. We are happy to credit your work once a fix is out.",
      "The machine-readable version of this policy is published at /.well-known/security.txt.",
    ],
  },
];

const securityHeroAccents = [
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

const SecurityPage = () => (
  <div className="pt-16">
    <MarketingHero
      seed="/security"
      eyebrow="Security"
      title="Security & Responsible Disclosure"
      description="How to report a vulnerability, what's in scope, and the safe harbor we extend to good-faith research."
      align="center"
      accents={securityHeroAccents}
    />

    <SectionDivider />

    <Section className="bg-brand-surface">
      <SectionContainer className="max-w-3xl space-y-12">
        <div className="flex flex-wrap gap-4 text-sm">
          <a
            href={ADVISORIES_URL}
            className="font-medium text-primary underline underline-offset-4"
            target="_blank"
            rel="noreferrer"
          >
            Report via GitHub advisory
          </a>
          <a
            href={`mailto:${SECURITY_EMAIL}`}
            className="font-medium text-primary underline underline-offset-4"
          >
            {SECURITY_EMAIL}
          </a>
        </div>

        {securitySections.map((section) => (
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

export default SecurityPage;
