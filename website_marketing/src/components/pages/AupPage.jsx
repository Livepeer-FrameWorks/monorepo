import { Section, SectionContainer } from "@/components/ui/section";
import config from "../../config";
import { MarketingHero, MarketingScrollProgress, SectionDivider } from "@/components/marketing";

const rules = [
  "Do not upload, stream, or distribute unlawful, infringing, or abusive content, including content you do not hold the rights to.",
  "Do not stream content that is illegal where you or your viewers are located, or that depicts or promotes serious harm.",
  "Do not probe, overload, or disrupt FrameWorks infrastructure, APIs, or other customers, and do not run denial-of-service or load-testing against the platform without prior written agreement.",
  "Do not attempt to bypass security controls, authentication, or rate limits, or access data, streams, or infrastructure that is not yours.",
  "Do not use FrameWorks to send spam, distribute malware, or facilitate phishing or other fraud.",
  "Respect shared beta resources—processing and bandwidth allocations are provided on a fair-use basis and are not intended for unattended bulk re-encoding or crypto-style workloads.",
  "Do not re-sell or share accounts, API tokens, or stream keys without written permission.",
];

const fairUse = [
  "Beta capacity is shared across everyone trying FrameWorks, so we ask that you size your usage to a genuine streaming workload rather than synthetic load.",
  "If you expect a large event, sustained 24/7 ingest, or unusually high concurrency, tell us in advance so we can make sure there is headroom for you and for other operators.",
  "We monitor for patterns that degrade the experience for others—runaway retries, abandoned high-bitrate ingests, or automated abuse—and may rate-limit or pause them to protect the network.",
];

const aupHeroAccents = [
  {
    kind: "beam",
    x: 20,
    y: 36,
    width: "clamp(21rem, 41vw, 33rem)",
    height: "clamp(17rem, 29vw, 25rem)",
    rotate: -16,
    fill: "linear-gradient(142deg, rgba(95, 135, 215, 0.33), rgba(25, 33, 59, 0.23))",
    opacity: 0.53,
    radius: "44px",
  },
  {
    kind: "beam",
    x: 74,
    y: 30,
    width: "clamp(19rem, 36vw, 29rem)",
    height: "clamp(15rem, 25vw, 21rem)",
    rotate: 15,
    fill: "linear-gradient(152deg, rgba(62, 185, 242, 0.29), rgba(19, 25, 43, 0.19))",
    opacity: 0.49,
    radius: "41px",
  },
];

const AupPage = () => (
  <div className="pt-16">
    <MarketingHero
      seed="/aup"
      eyebrow="Legal"
      title="Acceptable Use Policy"
      description="Use FrameWorks responsibly during the public beta. These rules explain what is and is not allowed."
      align="center"
      accents={aupHeroAccents}
    />

    <SectionDivider />

    <Section className="bg-brand-surface">
      <SectionContainer className="max-w-3xl space-y-12">
        <div className="space-y-4">
          <h2 className="text-xl font-semibold text-foreground">What is not allowed</h2>
          <div className="space-y-4 text-sm leading-relaxed text-muted-foreground">
            <ul className="space-y-3">
              {rules.map((rule) => (
                <li key={rule} className="flex items-start gap-3">
                  <span className="mt-[6px] h-2 w-2 rounded-full bg-rose-400 flex-shrink-0" />
                  <span>{rule}</span>
                </li>
              ))}
            </ul>
            <p>
              We may throttle, suspend, or terminate accounts that violate these rules. Serious
              violations may be reported to the relevant authorities.
            </p>
          </div>
        </div>
        <div className="space-y-4">
          <h2 className="text-xl font-semibold text-foreground">Fair use of beta resources</h2>
          <div className="space-y-4 text-sm leading-relaxed text-muted-foreground">
            <ul className="space-y-3">
              {fairUse.map((item) => (
                <li key={item} className="flex items-start gap-3">
                  <span className="mt-[6px] h-2 w-2 rounded-full bg-accent flex-shrink-0" />
                  <span>{item}</span>
                </li>
              ))}
            </ul>
          </div>
        </div>
        <div className="space-y-4">
          <h2 className="text-xl font-semibold text-foreground">Enforcement</h2>
          <div className="space-y-3 text-sm leading-relaxed text-muted-foreground">
            <p>
              Where we can, we will warn you and give you a chance to fix a problem before taking
              action. For clear or repeated violations—or anything that puts other operators,
              viewers, or the platform at risk—we may act immediately, including suspending streams
              or terminating the account without refund.
            </p>
            <p>
              This policy works alongside our Terms of Service and Privacy Policy. If they appear to
              conflict for a specific situation, contact us and we will clarify how it applies.
            </p>
          </div>
        </div>
        <div className="space-y-4">
          <h2 className="text-xl font-semibold text-foreground">Need clarification?</h2>
          <div className="space-y-3 text-sm leading-relaxed text-muted-foreground">
            <p>
              Unsure whether your use case complies with the policy? Contact us before you deploy.
              We are happy to confirm whether a workload is acceptable or suggest alternatives.
            </p>
            <p>
              Report suspected abuse to{" "}
              <a href={`mailto:${config.contactEmail}`} className="text-accent underline">
                {config.contactEmail}
              </a>
              .
            </p>
          </div>
        </div>
      </SectionContainer>
    </Section>

    <MarketingScrollProgress />
  </div>
);

export default AupPage;
