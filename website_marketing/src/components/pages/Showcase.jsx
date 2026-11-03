import { createElement } from "react";
import { motion } from "framer-motion";
import config from "../../config";
import status from "@/data/status.json";
import { Section, SectionContainer } from "@/components/ui/section";
import StatusTag from "../shared/StatusTag";
import * as figureKit from "@/components/marketing/figures";
import {
  MarketingHero,
  MarketingBand,
  HeadlineStack,
  CTACluster,
  MarketingScrollProgress,
  MarketingCTAButton,
  MarketingGridSplit,
  IconList,
  SectionDivider,
  StudioComposite,
} from "@/components/marketing";
import { featureOf, roadmapHref, showcaseLink, showcaseTiers } from "@/lib/featureStatus";
import { docsLink, signupUrl } from "@/lib/siteLinks";
import { contactPath } from "@/lib/contactTopics";

const LEAD_ID = "streamcrafter";
const CLOUD_PRODUCTION_ID = "composition";

// Group intros keyed by horizon. The group titles are the status.json labels.
const TIER_INTRO = {
  now: "Available on the hosted platform and in the self-hosted release. A badge or note marks work that is in beta or still expanding.",
  next: "Next on the roadmap, in no fixed order and with no dates. Each entry links to its roadmap section.",
  later: "Planned work that follows the items above.",
};

// Renders the figures-kit drawing a status.json row names; an unknown name is
// a build error rather than a silently missing diagram.
const figureElement = (name) => {
  if (!name) return null;
  const component = figureKit[name];
  if (!component) {
    throw new Error(`status.json names figure "${name}", which the figures kit does not export`);
  }
  return createElement(component, { spot: true });
};

const FeatureAction = ({ link, className }) => {
  const resolved = showcaseLink(link, config.docsUrl);
  if (!resolved) return null;
  return (
    <MarketingCTAButton
      intent="secondary"
      label={resolved.label}
      className={className}
      {...(resolved.to ? { to: resolved.to } : { href: resolved.href })}
    />
  );
};

const Bullets = ({ items }) =>
  items?.length ? (
    <ul className="showcase-bullets">
      {items.map((item) => (
        <li key={item}>{item}</li>
      ))}
    </ul>
  ) : null;

// One shipping feature: copy on one side, its diagram (or its bullets when it
// has no diagram) on the other. Diagram rows alternate sides; `flip` puts the
// diagram first.
const FeatureRow = ({ feature, flip }) => {
  const { showcase } = feature;
  const figure = figureElement(showcase.figure);
  const media = figure ?? (showcase.bullets.length ? <Bullets items={showcase.bullets} /> : null);
  const copy = (
    <div className="showcase-row__copy">
      <StatusTag feature={feature.id} />
      <h3 className="showcase-row__title">{showcase.title}</h3>
      <p className="showcase-row__summary">{showcase.summary}</p>
      {figure ? <Bullets items={showcase.bullets} /> : null}
      {feature.note ? <p className="showcase-row__note">{feature.note}</p> : null}
      <FeatureAction link={showcase.link} className="showcase-row__action" />
    </div>
  );

  return (
    <article
      id={feature.id}
      className="showcase-row"
      data-media={figure ? "figure" : media ? "bullets" : "none"}
      data-flip={figure && flip ? "true" : "false"}
    >
      {copy}
      {media ? <div className="showcase-row__media">{media}</div> : null}
    </article>
  );
};

// A planned feature: tag, title, summary and its roadmap entry.
const PlannedCard = ({ feature }) => {
  const { showcase } = feature;
  const roadmap = roadmapHref(feature, config.docsUrl);
  return (
    <article id={feature.id} className="showcase-card">
      <div className="showcase-card__body">
        <StatusTag feature={feature.id} />
        <h3 className="showcase-card__title">{showcase.title}</h3>
        <p className="showcase-card__summary">{showcase.summary}</p>
        <Bullets items={showcase.bullets} />
      </div>
      {roadmap ? (
        <a className="showcase-card__link" href={roadmap}>
          Roadmap entry <span aria-hidden="true">→</span>
        </a>
      ) : null}
    </article>
  );
};

const TierHeader = ({ tier }) => (
  <HeadlineStack
    title={tier.label}
    subtitle={TIER_INTRO[tier.horizon]}
    align="left"
    underlineAlign="start"
  />
);

const reveal = {
  initial: { opacity: 0, y: 24 },
  whileInView: { opacity: 1, y: 0 },
  viewport: { once: true, amount: 0.1 },
  transition: { duration: 0.6 },
};

const Showcase = () => {
  const tiers = showcaseTiers(status);
  const [now, next, later] = tiers;
  const lead = featureOf(status, LEAD_ID);
  const cloudProduction = featureOf(status, CLOUD_PRODUCTION_ID);
  const shippingRows = now.features.filter((feature) => feature.id !== LEAD_ID);
  const figureRows = shippingRows
    .filter((feature) => feature.showcase.figure)
    .map((feature) => feature.id);

  const launchOptions = [
    {
      id: "hosted",
      title: "Hosted",
      description: "Sign up and stream on FrameWorks infrastructure. The Free plan needs no card.",
      action: {
        label: "Start Free",
        href: signupUrl(config.appUrl, "showcase-launch-hosted"),
        external: true,
      },
    },
    {
      id: "browser",
      title: "In your browser",
      description: "Play an HLS, DASH, WHEP or MistServer stream, or go live with StreamCrafter.",
      action: { label: "Open the stream test", to: "/stream-test" },
    },
    {
      id: "edge",
      title: "Your own edge",
      description: "Attach edge nodes you run to the hosted platform with the frameworks CLI.",
      action: { label: "Bring your own edge", href: docsLink(config.docsUrl, "/hybrid/overview") },
    },
    {
      id: "self-host",
      title: "Self-hosted",
      description: "Run the whole platform on your own hardware, deployed and upgraded by the CLI.",
      action: {
        label: "Self-hosting guide",
        href: docsLink(config.docsUrl, "/operators/deployment"),
      },
    },
  ];

  return (
    <div className="pt-16">
      <MarketingHero
        seed="/showcase"
        eyebrow="Showcase"
        title="What FrameWorks does today, and what comes next"
        description="A plain view of what you can use now, what is being built next, and what is further out. Statuses come from the same feature registry as the docs and the roadmap."
        align="center"
        surface="gradient"
        surfaceTone="accent"
        surfaceIntensity="raised"
        primaryAction={{
          label: "Start Free",
          href: signupUrl(config.appUrl, "showcase-hero"),
          external: true,
          className: "cta-motion",
        }}
        secondaryAction={{
          label: "Try StreamCrafter",
          to: "/stream-test",
          className: "cta-motion",
          variant: "secondary",
        }}
        support={
          <nav className="showcase-jump" aria-label="Showcase groups">
            {tiers.map((tier) => (
              <a key={tier.horizon} href={`#${tier.anchor}`} className="showcase-jump__link">
                <span>{tier.label}</span>
                <span className="showcase-jump__count">{tier.features.length}</span>
              </a>
            ))}
          </nav>
        }
      />

      <SectionDivider />

      <Section id={now.anchor} className="bg-brand-surface">
        <SectionContainer>
          <motion.div id={lead.id} className="showcase-lead-anchor" {...reveal}>
            <MarketingBand
              preset="foundation"
              tone="cool"
              texturePattern="pinlines"
              textureNoise="film"
              contentClassName="showcase-lead"
            >
              <HeadlineStack
                eyebrow={`${now.label} · ${lead.tag}`}
                title={lead.showcase.title}
                subtitle={lead.showcase.summary}
                align="left"
                underlineAlign="start"
              />
              <MarketingGridSplit align="stretch" stackAt="lg" seam ratio="narrow-wide">
                <div className="slab-zone">
                  <div className="slab-zone__body showcase-lead__body">
                    <IconList
                      items={lead.showcase.bullets.map((bullet) => ({ title: bullet }))}
                      variant="list"
                      indicator="dot"
                      headingLevel="h3"
                    />
                    {lead.note ? <p className="showcase-lead__note">{lead.note}</p> : null}
                  </div>
                  <div className="slab-zone__actions">
                    <MarketingCTAButton intent="primary" to="/stream-test" label="Try it now" />
                    <MarketingCTAButton
                      intent="secondary"
                      href={docsLink(config.docsUrl, "/builders/go-live-browser")}
                      label="Go live guide"
                    />
                    <MarketingCTAButton
                      intent="secondary"
                      href={docsLink(config.docsUrl, "/builders/ingest")}
                      label="SDK docs"
                    />
                  </div>
                </div>
                <div className="marketing-figure-cell">
                  <StudioComposite />
                </div>
              </MarketingGridSplit>
              <a className="showcase-lead__next" href={`#${cloudProduction.id}`}>
                <StatusTag feature={cloudProduction.id} />
                <span className="showcase-lead__next-copy">
                  <strong>{cloudProduction.showcase.title}</strong>
                  <span>{cloudProduction.showcase.summary}</span>
                </span>
                <span aria-hidden="true" className="showcase-lead__next-arrow">
                  →
                </span>
              </a>
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <MarketingBand preset="quiet" contentClassName="showcase-group">
            <TierHeader tier={now} />
            <div className="showcase-rows">
              {shippingRows.map((feature) => (
                <FeatureRow
                  key={feature.id}
                  feature={feature}
                  flip={figureRows.indexOf(feature.id) % 2 === 0}
                />
              ))}
            </div>
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      {[next, later].map((tier) => (
        <div key={tier.horizon}>
          <Section id={tier.anchor} className="bg-brand-surface">
            <SectionContainer>
              <motion.div {...reveal}>
                <MarketingBand preset="quiet" contentClassName="showcase-group">
                  <TierHeader tier={tier} />
                  <div className="showcase-cards" data-count={tier.features.length}>
                    {tier.features.map((feature) => (
                      <PlannedCard key={feature.id} feature={feature} />
                    ))}
                  </div>
                </MarketingBand>
              </motion.div>
            </SectionContainer>
          </Section>
          <SectionDivider />
        </div>
      ))}

      <Section className="bg-brand-surface-muted" id="launch">
        <SectionContainer>
          <motion.div {...reveal}>
            <MarketingBand preset="foundation" texturePattern="seams" textureNoise="film">
              <HeadlineStack
                eyebrow="Get started"
                title="Launch FrameWorks your way"
                subtitle="The same media engine and APIs in every option. Move between them without rebuilding."
                align="left"
                underlineAlign="start"
              />
              <div className="showcase-launch">
                {launchOptions.map((option) => (
                  <div key={option.id} className="slab-zone showcase-launch__option">
                    <div className="slab-zone__body showcase-launch__body">
                      <h3>{option.title}</h3>
                      <p>{option.description}</p>
                    </div>
                    <div className="slab-zone__actions">
                      <MarketingCTAButton
                        intent={option.id === "hosted" ? "primary" : "secondary"}
                        {...option.action}
                      />
                    </div>
                  </div>
                ))}
              </div>
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section>
        <SectionContainer>
          <MarketingBand preset="quiet">
            <HeadlineStack
              eyebrow="Talk to the engineers"
              title="Tell us what you are building"
              subtitle="You work directly with the people who have built MistServer for more than 15 years. That experience covers your whole pipeline, not just our part of it: encoders and contribution before FrameWorks, players and delivery after it. Ask about trial usage, custom pricing, or a shared channel."
              align="left"
              underlineAlign="start"
              actionsPlacement="inline"
              actions={
                <CTACluster align="end" wrap>
                  <MarketingCTAButton
                    intent="primary"
                    to={contactPath("engineering")}
                    label="Talk to the engineers"
                  />
                  <MarketingCTAButton
                    intent="secondary"
                    href={docsLink(config.docsUrl, "/")}
                    label="Read the docs"
                  />
                </CTACluster>
              }
            />
          </MarketingBand>
        </SectionContainer>
      </Section>

      <MarketingScrollProgress />
    </div>
  );
};

export default Showcase;
