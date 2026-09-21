import { motion } from "framer-motion";
import { Cable, Coins } from "lucide-react";
import config from "../../config";
import { ChartBarIcon, SparklesIcon, FilmIcon, GlobeAltIcon } from "@heroicons/react/24/outline";
import { Section, SectionContainer } from "@/components/ui/section";
import StatusTag from "../shared/StatusTag";
import SovereigntyNote from "../shared/SovereigntyNote";
import {
  MarketingHero,
  MarketingBand,
  MarketingGridSplit,
  HeadlineStack,
  CTACluster,
  MarketingFeatureWall,
  MarketingPartnerSurface,
  TimelineBand,
  MarketingFinalCTA,
  MarketingScrollProgress,
  MarketingGridSeam,
  MarketingIconBadge,
  MarketingCTAButton,
  MarketingStackedSeam,
  SectionDivider,
} from "@/components/marketing";

const About = () => {
  const docsBase = (config.docsUrl ?? "/docs").replace(/\/+$/, "");

  const team = [
    {
      name: "MistServer Team",
      role: "Video Infrastructure Pioneers",
      description:
        "The same team building FrameWorks has developed MistServer for more than 15 years. It is the media engine at the core of FrameWorks, not a third-party service hidden behind our API.",
      avatar: "/mist.svg",
      href: "https://www.mistserver.com/",
    },
    {
      name: "Livepeer Network",
      role: "Decentralized Video Infrastructure",
      description:
        "Livepeer provides decentralized processing capacity and ecosystem backing, helping FrameWorks offer accessible transcoding, burst capacity, and a generous free tier.",
      avatar: "/livepeer-light.svg",
      href: "https://livepeer.org/",
    },
  ];

  const timeline = [
    {
      year: "15+ years",
      title: "Building the media engine",
      subtitle: "MistServer foundations",
      icon: FilmIcon,
      badges: ["Media Server", "Production Proven"],
      summary:
        "Our team develops MistServer, the media engine that now sits at the heart of FrameWorks.",
      points: [
        "Low-latency ingest, delivery, recording, and playback.",
        "Broad protocol support across broadcast, contribution, and web workflows.",
        "Production use by streaming operators around the world.",
      ],
    },
    {
      year: "Sep 2025",
      title: "From media server to video cloud",
      subtitle: "Amsterdam launch window (Sep 12–15)",
      icon: FilmIcon,
      badges: ["IBC 2025", "Live Demos"],
      summary:
        "FrameWorks brings a multi-tenant control plane, routing, analytics, billing, and automation to the proven MistServer media layer.",
      points: [
        "IBC showcase highlights CDN + hosted processing pipelines with telemetry.",
        "Sales-engineering loop; from demo interest into structured pilots.",
        "Partner roadmap aligned with MistServer + Livepeer field feedback captured during the conference.",
      ],
    },
    {
      year: "2026",
      title: "Public Beta Expansion",
      subtitle: "Selfhosted operators",
      icon: SparklesIcon,
      badges: ["Beta Access", "Hybrid Workflows"],
      summary: "Core platform is live with limited capacity and a semi-autonomous Media plane",
      points: [
        "Operators onboard quickly through the CLI.",
        "Advanced features start to roll out to round out platform featureset",
        "Code hardening, unit testing, UX polish, and documentation depth.",
      ],
    },
    {
      year: "2026+",
      title: "Scale and Expand",
      subtitle: "Staffing + product velocity",
      icon: ChartBarIcon,
      badges: ["Hiring", "Global Footprint"],
      summary:
        "Grow the core team, expand infrastructure regions, and deepen enterprise integrations.",
      points: [
        "Expand SRE and solutions engineering headcount to support multi-region customer rollouts.",
        "Broaden advanced-processing orchestration and AI automation capabilities.",
        "Acquire ASN and expand the network across various data centers.",
      ],
    },
    {
      year: "Future",
      title: "Federalized CDN Network",
      subtitle: "Long-term vision",
      icon: GlobeAltIcon,
      badges: ["Federated", "Tokenized Incentives"],
      summary:
        "Deliver a federated CDN and marketplace so operators can exchange bandwidth & compute.",
      points: [
        "Ensure the network is open & accessible, and lessen our role as the central gatekeeper.",
        "Expose FrameWorks policy engine so operators trade bandwidth and processing workloads securely.",
        "Maintain public-domain licensing so any team can extend, self-host, and interoperate without friction.",
      ],
    },
  ];

  const missionHighlights = [
    {
      title: "Media-Native Core",
      description:
        "MistServer carries the live media itself, with broad protocol support and more than 15 years of development behind it.",
      icon: null,
      tone: "accent",
    },
    {
      title: "One Operational System",
      description:
        "Ingest, routing, playback, recording, analytics, access, and billing share one tenant-aware platform.",
      icon: null,
      tone: "green",
    },
    {
      title: "Deployment Sovereignty",
      description:
        "Run on our infrastructure, your own hardware, or both. Change the deployment without rebuilding your application.",
      icon: null,
      tone: "yellow",
    },
    {
      title: "Open to People and Agents",
      description:
        "Operate through the dashboard, CLI, SDKs, GraphQL, or MCP. Human operators and autonomous tooling use the same platform capabilities.",
      icon: null,
      tone: "cyan",
    },
  ];

  const missionStoryCopy = [
    "FrameWorks exists because live video should not require surrendering the media path to a chain of managed services.",
    "Our team has spent more than 15 years developing MistServer, the media engine at the heart of FrameWorks. It handles ingest, protocol translation, low-latency delivery, recording, and playback without forcing every workflow through a proprietary video service.",
    "FrameWorks adds the cloud platform around that proven engine: tenancy, routing, authorization, analytics, billing, automation, and developer APIs.",
    "Use it as a hosted video cloud, connect your own edges, or run the complete stack yourself. The operating model changes; the product you build against does not.",
  ];

  const architectureProofs = [
    {
      title: "We develop the media engine",
      description:
        "MistServer is not an interchangeable upstream vendor. It is developed by the same team building FrameWorks and forms the core of the live media path.",
      tone: "accent",
    },
    {
      title: "We operate real infrastructure",
      description:
        "Hosted FrameWorks workloads run on infrastructure we operate, including bare metal. We control how streams are placed, routed, delivered, and observed.",
      tone: "green",
    },
    {
      title: "Processing has three paths",
      description:
        "Use FrameWorks edges for SLA-backed capacity, Livepeer for low-cost capacity that is quick to scale, or your own edge with no FrameWorks processing charge.",
      tone: "cyan",
    },
    {
      title: "You can take the stack with you",
      description:
        "The video layer, control plane, analytics, and operational model can run in your footprint. Storage and DNS remain external integrations today.",
      tone: "yellow",
    },
  ];

  const pipelineFeatures = [
    {
      title: "Auto-Discovery App",
      tone: "cyan",
      description:
        "A drop-in app that auto-discovers IP cameras, VISCA PTZ controls, NDI sources, USB webcams, and HDMI inputs.",
      status: "pipeline",
      statusNote: "Internal and pilot workloads only. Request access via Contact.",
    },
    {
      title: "Multi-stream Compositing",
      tone: "yellow",
      description:
        "Combine multiple input streams into one composite output with picture-in-picture, overlays, and mixing.",
      status: "pipeline",
      statusNote: "Internal and pilot workloads only. Request access via Contact.",
    },
    {
      title: "Live AI Processing",
      tone: "orange",
      description:
        "AI-native live video: transcribe, analyze, automate, and transform streams in real time.",
      status: "pipeline",
      statusNote: "Internal and pilot workloads only. Request access via Contact.",
    },
    {
      title: "DRM Content Protection",
      tone: "violet",
      description:
        "FairPlay, Widevine, and PlayReady protection for premium and licensed content, built natively into MistServer.",
      status: "pipeline",
      statusNote:
        "In development with the MistServer team; landing in our build ahead of upstream where it makes sense.",
    },
  ];

  const pipelineCards = pipelineFeatures.map((item) => ({
    tone: item.tone,
    title: item.title,
    description: item.description,
    meta: <StatusTag status={item.status} note={item.statusNote} />,
    hover: "subtle",
    stripe: true,
  }));

  const getTechInitial = (item) => {
    if (item?.initial) return item.initial;
    const match = item?.label?.match(/[A-Za-z0-9]/);
    return match ? match[0].toUpperCase() : "";
  };

  const techRows = [
    {
      title: "Broad Support",
      items: [
        { icon: "/mist.svg", label: "MistServer - media server" },
        { icon: "/livepeer-light.svg", label: "Livepeer Network - decentralized transcoding + AI" },
        { icon: "/webrtc.svg", label: "WebRTC, RTMP/E-RTMP, SRT, HLS, DASH - streaming protocols" },
      ],
    },
    {
      title: "Core Infrastructure",
      items: [
        { icon: "/go-lightblue.svg", label: "Go - service runtime" },
        { icon: "/kafka.svg", label: "Apache Kafka - event streaming" },
        { icon: "/postgres.svg", label: "YugabyteDB - distributed SQL" },
        { icon: "/clickhouse.svg", label: "ClickHouse - analytics store" },
        { glyph: Cable, label: "gRPC - service RPC" },
        { icon: "/gql.svg", label: "GraphQL - API gateway schema" },
        { icon: "/wireguard.svg", label: "WireGuard - mesh networking" },
        { icon: "/lets-encrypt.svg", label: "ACME / Let's Encrypt - TLS certificates" },
        { icon: "/hashicorp-vault.svg", label: "HashiCorp Vault - secrets" },
        { icon: "/redis.svg", label: "Redis - caching" },
        { icon: "/maxmind.png", label: "MaxMind GeoIP - geo lookup" },
      ],
    },
    {
      title: "Operations and Observability",
      items: [
        { icon: "/docker-mark-blue.svg", label: "Docker - containers" },
        { icon: "/nginx.svg", label: "Nginx - reverse proxy" },
        { icon: "/websocket.svg", label: "WebSockets - real-time transport" },
        { icon: "/prometheus.svg", label: "Prometheus - metrics" },
        { icon: "/grafana.svg", label: "Grafana - dashboards" },
        { icon: "/loki.svg", label: "Loki - logs" },
        { icon: "/victoriametrics.svg", label: "VictoriaMetrics - metrics storage" },
        { icon: "/metabase.svg", label: "Metabase - BI" },
      ],
    },
    {
      title: "Product and Ecosystem",
      items: [
        { icon: "/svelte.svg", label: "SvelteKit - web app" },
        { icon: "/reactjs.svg", label: "React - SDKs" },
        { icon: "/Astro.svg", label: "Astro - docs framework" },
        { icon: "/starlight.svg", label: "Starlight - docs theme" },
        { icon: "/stripe.svg", label: "Stripe - payments provider" },
        { icon: "/mollie.jpg", label: "Mollie - payments provider" },
        { glyph: Coins, label: "x402 - crypto payments + auth", initial: "x" },
        { icon: "/chatwoot.svg", label: "Chatwoot - support inbox" },
        { icon: "/listmonk.svg", label: "Listmonk - newsletter" },
      ],
    },
  ];

  const aboutHeroAccents = [
    {
      kind: "beam",
      x: 14,
      y: 34,
      width: "clamp(24rem, 46vw, 40rem)",
      height: "clamp(18rem, 34vw, 28rem)",
      rotate: -22,
      fill: "linear-gradient(145deg, rgba(92, 126, 216, 0.35), rgba(24, 30, 52, 0.26))",
      opacity: 0.58,
      radius: "52px",
    },
    {
      kind: "beam",
      x: 76,
      y: 24,
      width: "clamp(18rem, 36vw, 30rem)",
      height: "clamp(16rem, 28vw, 24rem)",
      rotate: 18,
      fill: "linear-gradient(160deg, rgba(53, 186, 255, 0.28), rgba(18, 22, 38, 0.18))",
      opacity: 0.46,
      radius: "44px",
    },
    {
      kind: "spot",
      x: 58,
      y: 84,
      width: "clamp(22rem, 48vw, 36rem)",
      height: "clamp(22rem, 48vw, 36rem)",
      fill: "radial-gradient(circle, rgba(125, 207, 255, 0.22) 0%, transparent 70%)",
      opacity: 0.32,
      blur: "95px",
    },
    {
      kind: "beam",
      x: 8,
      y: 78,
      width: "clamp(18rem, 32vw, 26rem)",
      height: "clamp(18rem, 32vw, 26rem)",
      rotate: -6,
      fill: "linear-gradient(140deg, rgba(147, 197, 114, 0.22), rgba(20, 26, 44, 0.18))",
      opacity: 0.34,
      radius: "42px",
    },
  ];

  return (
    <div className="pt-16">
      <MarketingHero
        seed="/about"
        className="about-hero"
        eyebrow="Our foundation"
        title="A new video cloud built on 15+ years of media engineering"
        description="FrameWorks turns MistServer's proven media engine into a complete hosted, hybrid, and self-hosted platform for live video."
        align="center"
        surface="gradient"
        surfaceTone="accent"
        surfaceIntensity="raised"
        support="We build the media server. We operate the cloud. You keep the option to run it yourself."
        accents={aboutHeroAccents}
      />

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <MarketingBand surface="none">
            <MarketingGridSplit align="start" stackAt="lg" gap="lg">
              <motion.div
                initial={{ opacity: 0, x: -26 }}
                whileInView={{ opacity: 1, x: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.55 }}
              >
                <HeadlineStack
                  eyebrow="Mission"
                  title="Why we built FrameWorks"
                  align="left"
                  underlineAlign="start"
                  className="mission-copy"
                >
                  <div className="flex flex-col gap-4">
                    {missionStoryCopy.map((paragraph) => (
                      <p
                        key={paragraph}
                        className="text-[1.05rem] leading-[1.68] text-muted-foreground"
                      >
                        {paragraph}
                      </p>
                    ))}
                  </div>
                </HeadlineStack>
                <CTACluster align="start" wrap className="mission-cta">
                  <MarketingCTAButton
                    intent="primary"
                    label="Start Free"
                    href={config.appUrl}
                    external
                  />
                  <MarketingCTAButton intent="secondary" label="Talk to Sales" to="/contact" />
                </CTACluster>
              </motion.div>

              <motion.div
                initial={{ opacity: 0, x: 26 }}
                whileInView={{ opacity: 1, x: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.55, delay: 0.1 }}
              >
                <MarketingBand preset="quiet" className="mission-pillars">
                  <HeadlineStack title="Core Pillars" align="left" underline={false} />
                  <MarketingStackedSeam gap="sm" className="mission-pillars__list">
                    {missionHighlights.map((highlight) => (
                      <div
                        key={highlight.title}
                        className="mission-pillars__entry"
                        data-tone={highlight.tone}
                      >
                        <span className="mission-pillars__dot" aria-hidden="true" />
                        <div className="mission-pillars__body">
                          <h3>
                            {highlight.title}{" "}
                            {highlight.title === "Deployment Sovereignty" ? (
                              <SovereigntyNote />
                            ) : null}
                          </h3>
                          <p>{highlight.description}</p>
                          {highlight.betaNote ? (
                            <span className="mission-pillars__note">{highlight.betaNote}</span>
                          ) : null}
                        </div>
                      </div>
                    ))}
                  </MarketingStackedSeam>
                </MarketingBand>
              </motion.div>
            </MarketingGridSplit>
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section id="architecture" className="bg-brand-surface-muted">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand
              preset="foundation"
              tone="cool"
              texturePattern="pinlines"
              textureNoise="film"
              textureBeam="soft"
              textureMotion="drift"
              textureStrength="soft"
            >
              <HeadlineStack
                eyebrow="Architecture"
                title="Not another API over someone else's video cloud"
                subtitle="FrameWorks uses external infrastructure where it makes sense, but the video product itself is not a resale of hyperscaler media services. You get cloud convenience without making a hyperscaler's product or pricing model the foundation of yours."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
              />
              <MarketingFeatureWall
                items={architectureProofs}
                columns={4}
                stackAt="md"
                hover="subtle"
                stripe
              />
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand
              preset="foundation"
              tone="cool"
              texturePattern="pinlines"
              textureNoise="film"
              textureBeam="soft"
              textureMotion="drift"
              textureStrength="soft"
            >
              <HeadlineStack
                eyebrow="Pipeline"
                title="Coming Soon"
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end">
                    <MarketingCTAButton
                      intent="secondary"
                      href={`${docsBase}/roadmap`}
                      label="See the full roadmap"
                      icon="book"
                    />
                  </CTACluster>
                }
              />
              <MarketingFeatureWall items={pipelineCards} columns={3} stackAt="md" />
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand preset="foundation">
              <HeadlineStack
                eyebrow="Technology"
                title="What runs the video cloud"
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
              >
                <p className="marketing-tech__intro">
                  At the core is MistServer, developed by our team for more than 15 years.
                  FrameWorks adds the distributed control plane, routing, analytics, billing, and
                  automation. Processing can run on our edges, on Livepeer, or on your own edge.
                </p>
              </HeadlineStack>
              <MarketingGridSeam columns={1} className="marketing-tech-rows">
                {techRows.map((row) => (
                  <div key={row.title} className="marketing-tech-row">
                    <div className="marketing-tech-row__header">
                      <span className="marketing-tech-row__title">{row.title}</span>
                      {row.description ? (
                        <p className="marketing-tech-row__description">{row.description}</p>
                      ) : null}
                    </div>
                    <ul className="marketing-tech-row__list">
                      {row.items?.map((item) => (
                        <li key={item.label} className="marketing-tech-row__item">
                          <MarketingIconBadge
                            variant="neutral"
                            className="marketing-tech-row__icon"
                          >
                            {item.icon ? (
                              <img
                                src={item.icon}
                                alt=""
                                aria-hidden="true"
                                className="marketing-tech-row__icon-image"
                              />
                            ) : item.glyph ? (
                              <item.glyph
                                aria-hidden="true"
                                className="marketing-tech-row__glyph"
                              />
                            ) : (
                              <span className="marketing-tech-row__initial" aria-hidden="true">
                                {getTechInitial(item)}
                              </span>
                            )}
                          </MarketingIconBadge>
                          <span className="marketing-tech-row__text">{item.label}</span>
                        </li>
                      ))}
                    </ul>
                  </div>
                ))}
              </MarketingGridSeam>
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <TimelineBand
              surface="panel"
              tone="steel"
              texture="beams"
              eyebrow="Timeline"
              title="Our journey"
              subtitle="How we got here and where we're headed."
              items={timeline}
            />
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface-strong">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingPartnerSurface
              partners={team}
              headline="Built by the MistServer team. Backed by Livepeer."
              eyebrow="Foundation and ecosystem"
              subtitle="A proven media engine, an open cloud platform, and additional processing capacity when workloads need it."
              variant="flush"
              surface="panel"
              surfaceTone="steel"
              surfaceTexture="none"
              texturePattern="scanlines"
              textureNoise="film"
              textureBeam="soft"
              textureMotion="drift"
              textureStrength="soft"
              surfaceDensity="compact"
            />
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="px-0">
        <motion.div
          initial={{ opacity: 0, y: 32 }}
          whileInView={{ opacity: 1, y: 0 }}
          viewport={{ once: true }}
          transition={{ duration: 0.6 }}
        >
          <MarketingFinalCTA
            eyebrow="Next steps"
            title="Three ways to deploy"
            description="Hosted, hybrid, or self-hosted. Video infrastructure that fits how you work."
            variant="band"
            primaryAction={{
              label: "Start Free",
              href: config.appUrl,
              external: true,
            }}
            secondaryAction={[
              {
                label: "Browse Docs",
                href: config.docsUrl,
                external: true,
              },
              {
                label: "Talk to our team",
                to: "/contact",
              },
            ]}
          />
        </motion.div>
      </Section>

      <MarketingScrollProgress />
    </div>
  );
};

export default About;
