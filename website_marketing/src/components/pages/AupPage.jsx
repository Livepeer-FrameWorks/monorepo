import { Section, SectionContainer } from "@/components/ui/section";
import config from "../../config";
import { MarketingHero, MarketingScrollProgress, SectionDivider } from "@/components/marketing";

const rules = [
  "Do not upload, stream, or distribute unlawful, infringing, or abusive content.",
  "Do not probe, overload, or disrupt FrameWorks infrastructure, APIs, or other customers.",
  "Do not attempt to bypass security controls or access data that is not yours.",
  "Respect shared beta resources—GPU and bandwidth allocations are provided on a fair-use basis.",
  "Do not re-sell or share accounts without written permission.",
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
      description="Use FrameWorks responsibly during the public beta. These placeholder rules explain what is and is not allowed until the production policy ships."
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
