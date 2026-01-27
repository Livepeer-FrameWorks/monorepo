import { motion } from "framer-motion";
import { Cable, Coins } from "lucide-react";
import config from "../../config";
import {
  ChartBarIcon,
  SparklesIcon,
  FilmIcon,
  GlobeAltIcon,
  CpuChipIcon,
} from "@heroicons/react/24/outline";
import { Section, SectionContainer } from "@/components/ui/section";
import StatusTag from "../shared/StatusTag";
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
  const team = [
    {
      name: "MistServer Team",
      role: "Video Infrastructure Pioneers",
      description:
        "The team behind MistServer, the media server powering streaming infrastructure worldwide. Over a decade of experience building reliable video technology.",
      avatar: "/mist.svg",
      href: "https://www.mistserver.com/",
    },
    {
      name: "Livepeer Network",
      role: "Decentralized Video Infrastructure",
      description:
        "A decentralized video network processing millions of minutes daily. Their backing enables FrameWorks to offer a free tier and feature-rich supporter tier.",
      avatar: "/livepeer-light.svg",
      href: "https://livepeer.org/",
    },
  ];

  const timeline = [
    {
      year: "Sep 2025",
      title: "IBC Demo Milestone",
      subtitle: "Amsterdam launch window (Sep 12–15)",
      icon: FilmIcon,
      badges: ["IBC 2025", "Live Demos"],
      summary:
        "FrameWorks ships the first public demo environment and onboarding flow in time for IBC Amsterdam.",
      points: [
        "IBC showcase highlights hybrid ingest + hosted GPU pipelines with real operator telemetry.",
        "Sales engineering loop formalized to convert demo interest into structured pilots.",
        "Partner roadmap aligned with MistServer + Livepeer field feedback captured during the conference.",
      ],
    },
    {
      year: "2025",
      title: "Public Beta Expansion",
      subtitle: "Hybrid operator cohorts live",
      icon: SparklesIcon,
      badges: ["Beta Access", "Hybrid Workflows"],
      summary:
        "Core platform is live with guarded capacity for hybrid operators, validating ingest, orchestration, and AI loops.",
      points: [
        "Operators onboard with guided runbooks pairing hosted control planes with self-managed nodes.",
        "Auto-discovery, compositing, and AI moderation launch under beta flags while telemetry hardening continues.",
        "Weekly release trains focus on operator feedback, console UX polish, and documentation depth.",
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
        "Broaden GPU orchestration and AI automation coverage for broadcast + defense workloads.",
        "Stand up regional control plane replicas with audit-grade tenancy boundaries.",
      ],
    },
    {
      year: "Future",
      title: "Federalized CDN Network",
      subtitle: "Long-term vision",
      icon: GlobeAltIcon,
      badges: ["Federated", "Tokenized Incentives"],
      summary:
        "Deliver a federated CDN and compute marketplace so operators can exchange capacity without lock-in.",
      points: [
        "Blend community-operated edge clusters with Livepeer incentives for pay-as-you-go streaming.",
        "Expose FrameWorks policy engine so operators trade bandwidth, GPU, and AI workloads securely.",
        "Maintain public-domain licensing so any team can extend, self-host, and interoperate without friction.",
      ],
    },
  ];

  const missionHighlights = [
    {
      title: "Developer-First Platform",
      description:
        "Relay-style GraphQL API with typed SDKs. Player and StreamCrafter components for React and Svelte. Build with confidence.",
      icon: null,
      tone: "accent",
    },
    {
      title: "Unmatched Analytics",
      description:
        "Routing decisions, QoE metrics, player telemetry. See exactly why viewer X connected to edge Y.",
      icon: null,
      tone: "green",
    },
    {
      title: "Sovereignty Without Pain",
      description:
        "Self-host the entire stack easily. Zero licensing fees. Hybrid mode when you need burst capacity.",
      icon: null,
      tone: "purple",
    },
    {
      title: "Agent-Native Platform",
      description: "MCP server, wallet auth, x402 payments. AI agents operate autonomously.",
      icon: null,
      tone: "cyan",
    },
  ];

  const missionStoryCopy = [
    "We're building the streaming infrastructure that doesn't lock you in. Need custom features? Build them yourself or let us help. Switch providers? Your infrastructure comes with you. Cloud bills spiraling? Run it yourself with our open source stack.",
    "Built by the MistServer team and backed by Livepeer, we're making enterprise-grade video accessible to everyone without surrendering control to cloud vendors.",
    "Run it yourself, use our hosted services, or mix and match. Uncloud your infrastructure.",
  ];

  const pipelineFeatures = [
    {
      title: "Auto-Discovery App",
      badge: "Industry First",
      icon: SparklesIcon,
      tone: "cyan",
      description:
        "A drop-in app that auto-discovers IP cameras, VISCA PTZ controls, NDI sources, USB webcams, and HDMI inputs.",
      status: "pipeline",
      statusNote: "In active development with limited pilot access.",
    },
    {
      title: "Multi-stream Compositing",
      badge: "Advanced Feature",
      icon: FilmIcon,
      tone: "purple",
      description:
        "Combine multiple input streams into one composite output with picture-in-picture, overlays, and mixing.",
      status: "pipeline",
      statusNote: "In limited internal demos and pilot hardening.",
    },
    {
      title: "Live AI Processing",
      badge: "AI Powered",
      icon: CpuChipIcon,
      tone: "orange",
      description:
        "AI-native live video: transcribe, analyze, automate, and transform streams in real time.",
      status: "pipeline",
      statusNote: "In development with limited pilot workloads.",
    },
  ];

  const pipelineCards = pipelineFeatures.map((item) => ({
    icon: item.icon,
    tone: item.tone,
    iconTone: item.tone,
    title: item.title,
    badge: item.badge,
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
        { icon: "/webrtc.svg", label: "WebRTC, RTMP, SRT, HLS, DASH - streaming protocols" },
      ],
    },
    {
      title: "Core Infrastructure",
      items: [
        { icon: "/go-lightblue.svg", label: "Go - service runtime" },
        { icon: "/kafka.svg", label: "Apache Kafka - event streaming" },
        { icon: "/zookeeper.png", label: "Zookeeper - Kafka coordination" },
        { icon: "/postgres.svg", label: "YugabyteDB - distributed SQL" },
        { icon: "/clickhouse.svg", label: "ClickHouse - analytics store" },
        { glyph: Cable, label: "gRPC - service RPC" },
        { icon: "/gql.svg", label: "GraphQL - API gateway schema" },
        { icon: "/wireguard.svg", label: "WireGuard - mesh networking" },
        { icon: "/powerdns.svg", label: "PowerDNS - authoritative anycast DNS" },
        { icon: "/lets-encrypt.svg", label: "ACME / Let's Encrypt - TLS certificates" },
        { icon: "/hashicorp-vault.svg", label: "HashiCorp Vault - secrets" },
        { icon: "/redis.svg", label: "Redis - caching" },
        { icon: "/geoip.svg", label: "MaxMind GeoIP - geo lookup" },
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
        title="About FrameWorks"
        description="The only streaming platform that combines full self-hosting capabilities with hosted processing, backed by unique features you won’t find anywhere else."
        align="center"
        surface="gradient"
        surfaceTone="accent"
        surfaceIntensity="raised"
        support="Open stack • Live video processing • Flexible deployments"
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
                <MarketingBand surface="panel" className="mission-pillars">
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
                          <h4>{highlight.title}</h4>
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

      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand surface="panel">
              <HeadlineStack
                eyebrow="Pipeline"
                title="Coming Soon"
                subtitle="Advanced features in active development for hybrid and self-hosted operators."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
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
            <MarketingBand surface="panel">
              <HeadlineStack
                eyebrow="Technology"
                title="Built on proven infrastructure"
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
              >
                <p className="marketing-tech__intro">
                  Production-ready components that scale with you, from ingest through analytics.
                  Proven by MistServer operators and the Livepeer network.
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
              headline="Powered by MistServer and Livepeer"
              eyebrow="Partners"
              subtitle="Video infrastructure expertise backed by the Livepeer treasury"
              variant="flush"
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
            title="Ready to get started?"
            description="Hosted, hybrid, or self-hosted. Mission-critical video infrastructure that fits how you work."
            variant="band"
            primaryAction={{
              label: "Start Free",
              href: config.appUrl,
              external: true,
            }}
            secondaryAction={[
              {
                label: "Talk to our team",
                to: "/contact",
              },
              {
                label: "View Open Source",
                href: config.githubUrl,
                icon: "auto",
                external: true,
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
