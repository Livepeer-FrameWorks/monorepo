import { motion } from "framer-motion";

import SdkCodePreview from "./SdkCodePreview";
// Demo player wrapper with status/health integration
import { Player as FrameworksPlayer } from "@livepeer-frameworks/player-react";
import {
  MarketingFinalCTA,
  MarketingScrollProgress,
  MarketingBand,
  HeadlineStack,
  CTACluster,
  MarketingCTAButton,
  MarketingComparisonGrid,
  MarketingComparisonCard,
  MarketingFeatureWall,
  MarketingHero,
  MarketingGridSplit,
  IconList,
  SectionDivider,
} from "@/components/marketing";
import { Section, SectionContainer } from "@/components/ui/section";
import { useState, useEffect, useMemo } from "react";
import config from "../../config";
import {
  ServerStackIcon,
  CodeBracketIcon,
  ChartBarIcon,
  GlobeAltIcon,
  CpuChipIcon,
  BanknotesIcon,
} from "@heroicons/react/24/outline";

const generateGlitchStrips = () => {
  const strips = [];
  for (let i = 0; i < 15; i++) {
    strips.push({
      stripHeight: 20 + Math.random() * 40,
      rawGlitchX1: (Math.random() - 0.5) * 40,
      rawGlitchX2: (Math.random() - 0.5) * 40,
      glitchHue1: (Math.random() - 0.5) * 90,
      glitchHue2: (Math.random() - 0.5) * 90,
      animationDelayFactor: Math.random(),
      animationDuration: 2000 + Math.random() * 3000,
      animationName: `glitch-${(i % 6) + 5}`,
    });
  }
  return strips;
};

const LandingPage = () => {
  const [showPlayer, setShowPlayer] = useState(false);
  const [logoAnimationComplete, setLogoAnimationComplete] = useState(false);
  const [demoState, setDemoState] = useState("booting");

  const glitchStripData = useMemo(() => generateGlitchStrips(), []);

  const demoStatusMap = {
    booting: { label: "INITIALIZING", tone: "muted" },
    gateway_loading: { label: "RESOLVING GATEWAY", tone: "muted" },
    gateway_ready: { label: "GATEWAY READY", tone: "active" },
    gateway_error: { label: "RECONNECTING", tone: "degraded" },
    no_endpoint: { label: "STANDBY", tone: "muted" },
    selecting_player: { label: "SELECTING PLAYER", tone: "active" },
    connecting: { label: "CONNECTING", tone: "active" },
    buffering: { label: "BUFFERING", tone: "warn" },
    playing: { label: "LIVE", tone: "live" },
    paused: { label: "PAUSED", tone: "muted" },
    ended: { label: "STANDBY", tone: "muted" },
    error: { label: "DEGRADED", tone: "degraded" },
    destroyed: { label: "STOPPED", tone: "muted" },
  };
  const demoStatus = demoStatusMap[demoState] || demoStatusMap.booting;

  useEffect(() => {
    // Preload logo image so glitch strips can start immediately
    const img = new Image();
    img.src = "/frameworks-dark-vertical-lockup.svg";

    // Logo enters with glitch, then reveals player
    const prefersReduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    const revealDelay = prefersReduced ? 0 : 800;
    const fadeMs = prefersReduced ? 0 : 1200;

    const playerTimer = setTimeout(() => {
      setShowPlayer(true);
    }, revealDelay);

    const cleanupTimer = setTimeout(() => {
      setLogoAnimationComplete(true);
    }, revealDelay + fadeMs);

    return () => {
      clearTimeout(playerTimer);
      clearTimeout(cleanupTimer);
    };
  }, []);

  const corePillars = [
    {
      title: "Developer-First Platform",
      description:
        "Relay-style GraphQL API with typed SDKs. Agent-native: skill.json, MCP, wallet auth, and x402 payments. Agents discover, operate, and pay autonomously.",
      icon: CodeBracketIcon,
      tone: "accent",
      badge: "Core",
    },
    {
      title: "Unmatched Analytics",
      description:
        "Routing decisions, QoE metrics, player telemetry. See exactly why viewer X connected to edge Y.",
      icon: ChartBarIcon,
      tone: "green",
      badge: "Core",
    },
    {
      title: "Sovereignty Without Pain",
      description:
        "Self-host the entire stack easily. Zero licensing fees. Hybrid mode when you need burst capacity.",
      icon: ServerStackIcon,
      tone: "yellow",
      badge: "Core",
    },
  ];

  const pillarCards = corePillars.map((pillar) => ({
    icon: pillar.icon,
    iconTone: pillar.tone,
    tone: pillar.tone,
    badge: pillar.badge,
    title: pillar.title,
    description: pillar.description,
    hover: "subtle",
    stripe: true,
    flush: true,
  }));

  const agentCards = [
    {
      icon: GlobeAltIcon,
      iconTone: "accent",
      tone: "accent",
      badge: "Open Standards",
      title: "Discover",
      description:
        "skill.json, llms.txt, MCP discovery, W3C DID, and OAuth metadata. Compatible with OpenClaw, Claude Code, Cursor, Gemini CLI, and 25+ agent frameworks.",
      hover: "subtle",
      stripe: true,
      flush: true,
    },
    {
      icon: CpuChipIcon,
      iconTone: "green",
      tone: "green",
      badge: "Zero Friction",
      title: "Authenticate",
      description:
        "Wallet signature auto-provisions a prepaid tenant. No email, no registration, no API key application.",
      hover: "subtle",
      stripe: true,
      flush: true,
    },
    {
      icon: BanknotesIcon,
      iconTone: "yellow",
      tone: "yellow",
      badge: "Inline",
      title: "Pay",
      description:
        "x402 gasless USDC on Base and Arbitrum. FrameWorks pays the gas. Balance credits instantly. Card and crypto deposits also supported.",
      hover: "subtle",
      stripe: true,
      flush: true,
    },
    {
      icon: ServerStackIcon,
      iconTone: "yellow",
      tone: "yellow",
      badge: "Full Stack",
      title: "Operate",
      description:
        "MCP tools for streams, recordings, analytics, and QoE diagnostics. GraphQL as an alternative. Agent-operated edge nodes coming soon.",
      hover: "subtle",
      stripe: true,
      flush: true,
    },
  ];

  const freeTierFeatures = [
    "All self-hosted features",
    "Shared bandwidth pool",
    "Livepeer-backed compute",
    "Open source & permissive licenses",
    "No cloud dependencies - runs anywhere",
    "Web dashboard with analytics included",
  ];

  const paidPlanHighlights = [
    "Custom subdomains and hosted load balancers",
    "Reserved GPU hours and bandwidth pools",
    "Team collaboration and advanced analytics",
    "Priority support with 24/7 options",
  ];

  const pricingPlans = [
    {
      id: "free",
      tone: "green",
      badge: "Backed by Livepeer",
      name: "Free Tier",
      price: "Free",
      period: "",
      description:
        "Complete self-hosting stack with shared pool access. Open source with permissive licenses: deploy it anywhere.",
      features: freeTierFeatures,
      ctaType: "external",
      ctalabel: "Start Free",
      ctaHref: config.appUrl,
      note: "No credit card required · Deploy in minutes",
    },
    {
      id: "payg",
      tone: "cyan",
      badge: "Agent-Ready",
      name: "Pay As You Go",
      price: "Usage-based",
      period: "",
      description:
        "Connect a wallet, fund your balance, and go. Wallet auth, x402 payments, and full MCP access. Built for agents and automation.",
      features: [
        "Wallet auth (no email required)",
        "Fund via card, crypto, or x402 USDC",
        "Full platform access via MCP or GraphQL",
        "Same usage rates as subscription tiers",
      ],
      ctaType: "internal",
      ctaLabel: "View Details",
      ctaTo: "/pricing",
      note: "No subscription · No minimum · Agent-native",
    },
    {
      id: "paid",
      tone: "cyan",
      badge: "Paid plans",
      name: "Hybrid & Hosted",
      price: "€50+",
      period: "/month",
      description:
        "GPU-intensive features like AI processing and multi-stream compositing, plus hosted services and enterprise support.",
      features: paidPlanHighlights,
      ctaType: "internal",
      ctaLabel: "View All Plans",
      ctaTo: "/pricing",
      note: "Supporter · Developer · Production · Enterprise",
    },
  ];

  const docsBase = (config.docsUrl ?? "/docs").replace(/\/+$/, "");

  const landingHeroAccents = [
    {
      kind: "beam",
      x: 18,
      y: 34,
      width: "clamp(28rem, 52vw, 44rem)",
      height: "clamp(18rem, 32vw, 28rem)",
      rotate: -14,
      fill: "linear-gradient(130deg, rgba(122, 162, 247, 0.38), rgba(30, 42, 84, 0.18))",
      opacity: 0.55,
      radius: "48px",
    },
    {
      kind: "beam",
      x: 78,
      y: 28,
      width: "clamp(22rem, 42vw, 34rem)",
      height: "clamp(16rem, 26vw, 22rem)",
      rotate: 16,
      fill: "linear-gradient(150deg, rgba(69, 208, 255, 0.3), rgba(18, 24, 48, 0.16))",
      opacity: 0.52,
      radius: "42px",
    },
    {
      kind: "spot",
      x: 24,
      y: 78,
      width: "clamp(22rem, 46vw, 34rem)",
      height: "clamp(22rem, 46vw, 34rem)",
      fill: "radial-gradient(circle, rgba(125, 207, 255, 0.26) 0%, transparent 68%)",
      opacity: 0.4,
      blur: "90px",
    },
  ];

  return (
    <div className="pt-16">
      <MarketingHero
        align="left"
        mediaPosition="right"
        seed="landing"
        className="landing-hero"
        surface="gradient"
        accents={landingHeroAccents}
        title="Sovereign Video Infrastructure"
        description="Most streaming platforms lock you into their ecosystem. We give you the keys."
        support="SaaS → Hybrid (self-hosted edge) → Fully self-hosted • One platform, three modes • Public domain licensed."
        primaryAction={{
          label: "Start Free",
          href: config.appUrl,
          external: true,
          className: "cta-motion",
        }}
        secondaryAction={{
          label: "View Pricing",
          to: "/pricing",
          icon: "auto",
          className: "cta-motion",
          variant: "secondary",
        }}
        footnote="Free tier includes self-hosting + access to shared bandwidth pool"
        mediaSurface="none"
        media={
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.8, delay: 0.2 }}
            className="hero-visual"
          >
            <div className="hero-player-card">
              <div className="hero-player-card__layout relative z-10">
                {/* Frame Top */}
                <div className="hero-player-card__header">
                  <div className="hero-player-card__title-row">
                    <div className="hero-player-card__title-group">
                      <span className="hero-player-card__dot" aria-hidden="true" />
                      <h2 className="hero-player-card__title">FrameWorks Demo</h2>
                    </div>
                    <span className={`hero-demo-status hero-demo-status--${demoStatus.tone}`}>
                      {demoStatus.label}
                    </span>
                  </div>
                  <p className="hero-player-card__subhead">
                    Live stream path with automatic recovery
                  </p>
                  <p className="hero-player-card__caption">
                    Watch our streaming infrastructure in action.
                  </p>
                </div>

                {/* Video Player - takes up most space */}
                <div className="hero-player-card__viewport">
                  <div className="hero-player-card__screen">
                    <div className="hero-player-card__stage">
                      <FrameworksPlayer
                        contentId={config.demoStreamName}
                        contentType="live"
                        options={{
                          autoplay: true,
                          muted: true,
                          controls: false,
                          gatewayUrl: config.gatewayUrl || undefined,
                        }}
                        onStateChange={(st) => setDemoState(st)}
                      />
                    </div>
                  </div>
                </div>
              </div>

              {/* Logo Overlay - dissolves to reveal player */}
              {!logoAnimationComplete && (
                <motion.div
                  className="hero-player-card__overlay"
                  initial={{ opacity: 1 }}
                  animate={{
                    opacity: showPlayer ? 0 : 1,
                    scale: showPlayer ? 1.05 : 1,
                  }}
                  transition={{
                    duration: 1.2,
                    ease: [0.25, 0.46, 0.45, 0.94],
                    opacity: { duration: 1.2 },
                    scale: { duration: 1.4 },
                  }}
                >
                  {/* Logo Entry Animation */}
                  <motion.div
                    className="relative w-full h-full"
                    initial={{ scale: 0.8, opacity: 0, y: 20 }}
                    animate={{
                      scale: 1,
                      opacity: 1,
                      y: 0,
                    }}
                    transition={{
                      duration: 0.3,
                      ease: [0.25, 0.46, 0.45, 0.94],
                    }}
                  >
                    {/* Main logo - centered vertical lockup */}
                    <div className="hero-player-card__overlay-logo neon-glow">
                      <img
                        src="/frameworks-dark-vertical-lockup.svg"
                        alt="FrameWorks"
                        className="w-2/3 max-w-[300px] h-auto"
                      />
                    </div>
                  </motion.div>

                  {/* Glitch Effect */}
                  <div
                    className="hero-player-card__overlay-glitch"
                    style={{
                      overflow: "visible",
                      transform: "translateZ(0)",
                      willChange: "transform",
                    }}
                  >
                    {(() => {
                      const viewportWidth =
                        typeof window !== "undefined" ? window.innerWidth : 1024;
                      let maxSafeTranslation, stripExtension;

                      if (showPlayer) {
                        maxSafeTranslation = viewportWidth < 640 ? 2 : viewportWidth < 1024 ? 3 : 5;
                        stripExtension = 0;
                      } else {
                        if (viewportWidth < 640) {
                          maxSafeTranslation = 6;
                          stripExtension = 15;
                        } else {
                          maxSafeTranslation = 10;
                          stripExtension = 20;
                        }
                      }

                      let currentPosition = 0;
                      return glitchStripData.map((data, i) => {
                        const glitchX1 = Math.max(
                          -maxSafeTranslation,
                          Math.min(maxSafeTranslation, data.rawGlitchX1)
                        );
                        const glitchX2 = Math.max(
                          -maxSafeTranslation,
                          Math.min(maxSafeTranslation, data.rawGlitchX2)
                        );
                        const animationDelay =
                          i < 3
                            ? 0
                            : i < 8
                              ? data.animationDelayFactor * 0.5
                              : data.animationDelayFactor * 1.5;
                        const top = currentPosition;
                        currentPosition += data.stripHeight;

                        return (
                          <div
                            key={i}
                            className="absolute"
                            style={{
                              left: `-${stripExtension}px`,
                              right: `-${stripExtension}px`,
                              top: `${top}px`,
                              height: `${data.stripHeight}px`,
                              backgroundImage: "url(/frameworks-dark-vertical-lockup.svg)",
                              backgroundSize: `calc(100% - ${stripExtension * 2}px) auto`,
                              backgroundPosition: `${stripExtension}px -${top}px`,
                              backgroundRepeat: "no-repeat",
                              overflow: "visible",
                              "--glitch-x-1": `${glitchX1}px`,
                              "--glitch-x-2": `${glitchX2}px`,
                              "--glitch-hue-1": `${data.glitchHue1}deg`,
                              "--glitch-hue-2": `${data.glitchHue2}deg`,
                              animationName: data.animationName,
                              animationDuration: `${data.animationDuration}ms`,
                              animationDelay: `${animationDelay}s`,
                              animationIterationCount: "infinite",
                              animationDirection: "alternate",
                              animationTimingFunction: "linear",
                              imageRendering: "pixelated",
                            }}
                          />
                        );
                      });
                    })()}
                  </div>
                </motion.div>
              )}
            </div>
          </motion.div>
        }
      />

      <SectionDivider />

      <div className="flex flex-col">
        {/* Rest of the sections remain the same */}
        <Section className="bg-brand-surface-muted landing-section--platform">
          <SectionContainer>
            <MarketingBand
              preset="beam"
              texturePattern="seams"
              textureNoise="film"
              textureBeam="soft"
              textureMotion="drift"
              textureStrength="soft"
            >
              <HeadlineStack
                eyebrow="Platform"
                title="Built Different"
                subtitle="Great DX. Deep analytics. Total sovereignty."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end">
                    <MarketingCTAButton intent="secondary" to="/about" label="Learn More" />
                  </CTACluster>
                }
              />
              <MarketingFeatureWall items={pillarCards} columns={3} />
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        <Section className="bg-brand-surface landing-section--sdk">
          <SectionContainer>
            <MarketingBand preset="foundation" texturePattern="seams" textureNoise="film">
              <MarketingGridSplit align="stretch" stackAt="lg" seam>
                {/* Left: Text slab with header/body/actions zones */}
                <div className="slab-zone">
                  <div className="slab-zone__header">
                    <HeadlineStack
                      eyebrow="Developer First"
                      title="Code is the Content"
                      subtitle="Stop fighting with FFmpeg flags. Our SDKs give you drop-in components and hooks for playback and broadcast."
                      align="left"
                      underlineAlign="start"
                    />
                  </div>

                  <div className="slab-zone__body">
                    <IconList
                      items={[
                        {
                          title: "Universal Playback",
                          description:
                            "One player, every device. Auto-selects the best transport for the browser and network conditions.",
                        },
                        {
                          title: "OBS in the Browser",
                          description:
                            "StreamCrafter gives you compositing, encoding, and multi-source mixing. Drop in and go live.",
                        },
                        {
                          title: "WebRTC-First",
                          description:
                            "Sub-second latency by default. Real-time streaming without the complexity.",
                        },
                      ]}
                      variant="list"
                      indicator="check"
                      gap="md"
                    />
                  </div>

                  <div className="slab-zone__actions">
                    <MarketingCTAButton
                      intent="primary"
                      href={`${docsBase}/streamers/playback`}
                      label="Read the Docs"
                      icon="book"
                    />
                    <MarketingCTAButton
                      intent="secondary"
                      href="https://github.com/livepeer/frameworks"
                      label="View on GitHub"
                      icon="github"
                      external
                    />
                  </div>
                </div>

                {/* Right: Code zone - full bleed */}
                <SdkCodePreview variant="flush" className="min-h-[400px] lg:min-h-[500px]" />
              </MarketingGridSplit>
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        {/* Agent-Native Section */}
        <Section className="bg-brand-surface-muted landing-section--agents">
          <SectionContainer>
            <MarketingBand
              surface="panel"
              tone="steel"
              texturePattern="seams"
              textureNoise="film"
              textureBeam="soft"
              textureMotion="drift"
              textureStrength="soft"
              density="spacious"
              flush
            >
              <HeadlineStack
                eyebrow="Agent-Native"
                title="Built for Autonomous Agents"
                subtitle="Agents discover the platform via open standards, authenticate with a wallet, pay inline with USDC, and operate the full stack — no human in the loop."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end" wrap>
                    <MarketingCTAButton
                      intent="primary"
                      href={`${docsBase}/agents/overview`}
                      label="Agent Docs"
                      external
                    />
                    <MarketingCTAButton
                      intent="secondary"
                      href="https://frameworks.network/skill.json"
                      label="View skill.json"
                      external
                    />
                  </CTACluster>
                }
              />
              <MarketingFeatureWall columns={4} items={agentCards} />
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        {/* Pricing Preview */}
        <Section className="landing-section--pricing">
          <SectionContainer>
            <MarketingBand preset="quiet">
              <HeadlineStack
                eyebrow="Pricing"
                title={
                  <>
                    <span className="transparent-word" data-text="Transparent">
                      Transparent
                    </span>{" "}
                    Pricing
                  </>
                }
                subtitle="Start free with self-hosting. Upgrade for GPU features, hosted services, and enterprise support when you need more."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end">
                    <MarketingCTAButton intent="secondary" to="/pricing" label="View Plans" />
                  </CTACluster>
                }
              />
              <MarketingComparisonGrid
                columns={3}
                stackAt="md"
                className="landing-pricing-grid"
                items={pricingPlans.map((plan, index) => {
                  const ctaProps =
                    plan.ctaType === "external"
                      ? { href: plan.ctaHref, external: true }
                      : { to: plan.ctaTo };

                  return {
                    id: plan.id,
                    tone: plan.tone,
                    badge: plan.badge,
                    title: plan.name,
                    description: plan.description,
                    price: plan.price,
                    period: plan.period,
                    features: plan.features,
                    action: (
                      <MarketingCTAButton
                        intent={plan.ctaType === "external" ? "primary" : "secondary"}
                        label={plan.ctaLabel}
                        className="w-full justify-center"
                        {...ctaProps}
                      />
                    ),
                    footnote: plan.note,
                    motionDelay: index * 0.12,
                  };
                })}
                renderCard={(item, index) => (
                  <motion.div
                    key={item.id ?? index}
                    initial={{ opacity: 0, y: 24 }}
                    whileInView={{ opacity: 1, y: 0 }}
                    viewport={{ once: true }}
                    transition={{ duration: 0.55, delay: item.motionDelay ?? index * 0.12 }}
                  >
                    <MarketingComparisonCard {...item} />
                  </motion.div>
                )}
              />
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        {/* CTA Section */}
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
              description="Deploy the full stack yourself, point your agent at skill.json, or let us run everything. Your call."
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
                {
                  label: "Agent Docs",
                  href: `${docsBase}/agents/overview`,
                  icon: "auto",
                  external: true,
                },
              ]}
            />
          </motion.div>
        </Section>
      </div>

      <MarketingScrollProgress />
    </div>
  );
};

export default LandingPage;
