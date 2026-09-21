import { motion } from "framer-motion";

import SdkCodePreview from "./SdkCodePreview";
import StreamJourney from "./StreamJourney";
import {
  MarketingFinalCTA,
  MarketingScrollProgress,
  MarketingBand,
  MarketingSlab,
  MarketingSlabHeader,
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
  SkipperConversationPreview,
  AgentPipelineStrip,
  DeploymentModes,
  MultistreamFanout,
  DashboardFrame,
  StatRow,
  RetentionCurve,
  TrendChart,
  BootWaterfall,
  analyticsFixtures as fx,
} from "@/components/marketing";
import { Section, SectionContainer } from "@/components/ui/section";
import SovereigntyNote from "../shared/SovereigntyNote";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { useState, useEffect, useMemo, useRef } from "react";
import config from "../../config";

const createSeededRandom = (seed) => {
  let state = seed;
  return () => {
    state = (state * 1664525 + 1013904223) >>> 0;
    return state / 4294967296;
  };
};

const generateGlitchStrips = () => {
  const random = createSeededRandom(421337);
  const strips = [];
  for (let i = 0; i < 15; i++) {
    strips.push({
      stripHeight: 20 + random() * 40,
      rawGlitchX1: (random() - 0.5) * 40,
      rawGlitchX2: (random() - 0.5) * 40,
      glitchHue1: (random() - 0.5) * 90,
      glitchHue2: (random() - 0.5) * 90,
      animationDelayFactor: random(),
      animationDuration: 2000 + random() * 3000,
      animationName: `glitch-${(i % 6) + 5}`,
    });
  }
  return strips;
};

function DeferredNetworkMap() {
  const containerRef = useRef(null);
  const [NetworkMapComponent, setNetworkMapComponent] = useState(null);

  useEffect(() => {
    if (!containerRef.current || NetworkMapComponent) return undefined;

    let active = true;
    const observer = new IntersectionObserver(
      ([entry]) => {
        if (!entry.isIntersecting) return;
        observer.disconnect();
        import("@/components/marketing/network/NetworkMap").then((module) => {
          if (active) setNetworkMapComponent(() => module.NetworkMap);
        });
      },
      { rootMargin: "800px 0px" }
    );
    observer.observe(containerRef.current);

    return () => {
      active = false;
      observer.disconnect();
    };
  }, [NetworkMapComponent]);

  return (
    <div
      ref={containerRef}
      className={`network-viz-deferred${NetworkMapComponent ? "" : " network-viz-pending"}`}
    >
      {NetworkMapComponent ? <NetworkMapComponent /> : null}
    </div>
  );
}

function MediaControlPreview() {
  return (
    <div className="media-control-preview" aria-label="Live media workflow">
      <div className="media-control-preview__bar">
        <span>LIVE PIPELINE</span>
        <span className="media-control-preview__live">CONTROLLED</span>
      </div>
      <div className="media-control-preview__grid">
        <div className="media-control-preview__column">
          <span className="media-control-preview__label">Sources</span>
          {[
            ["SRT · RIST", "available"],
            ["RTMP · WHIP", "available"],
            ["HLS · MPEG-TS", "available"],
            ["SDI · NDI", "next"],
          ].map(([label, status]) => (
            <div key={label} className="media-control-preview__node" data-status={status}>
              <span>{label}</span>
              <small>{status === "next" ? "NEXT" : "LIVE"}</small>
            </div>
          ))}
        </div>

        <div className="media-control-preview__engine">
          <span className="media-control-preview__label">Media engine</span>
          <div className="media-control-preview__engine-core">
            <img src="/mist.svg" alt="" aria-hidden="true" />
            <strong>MistServer</strong>
            <span>in-memory live stream</span>
          </div>
        </div>

        <div className="media-control-preview__column">
          <span className="media-control-preview__label">Live operations</span>
          {[
            ["Transmux + passthrough", "available"],
            ["SCTE-35 + HLS I/O", "expanding"],
            ["ABR processing", "expanding"],
            ["Composition + AI", "next"],
          ].map(([label, status]) => (
            <div key={label} className="media-control-preview__node" data-status={status}>
              <span>{label}</span>
              <small>
                {status === "available" ? "LIVE" : status === "expanding" ? "EXPANDING" : "NEXT"}
              </small>
            </div>
          ))}
        </div>
      </div>
      <div className="media-control-preview__footer">
        <span>FrameWorks carries and operates the media path, not just the API around it.</span>
      </div>
    </div>
  );
}

export const HOME_FAQS = [
  {
    question: "What is FrameWorks?",
    answer:
      "FrameWorks is a complete live video cloud built by the team behind MistServer. It covers ingest, protocol handling, processing, routing, delivery, recording, playback, analytics, and operations through one platform. Use our hosted infrastructure, attach your own edges, or run the full stack yourself.",
  },
  {
    question: "How is FrameWorks different from other cloud video platforms?",
    answer:
      "FrameWorks is not a resale of a hyperscaler's managed video APIs. Our team develops the MistServer media engine at its core and operates the platform around it. This gives you one system from ingest through playback, predictable infrastructure economics, and the freedom to move between hosted, hybrid, and self-hosted deployments.",
  },
  {
    question: "Can FrameWorks run fully self-hosted?",
    answer:
      "Yes. FrameWorks can run on bare metal, VMs, or Kubernetes with self-hosted ingest, playback, routing, analytics, and control-plane workflows. Current production deployments still use S3-compatible object storage and managed DNS integrations; native Ceph-backed storage and self-hosted/Anycast DNS are on the roadmap.",
  },
  {
    question: "What do MistServer and Livepeer do in FrameWorks?",
    answer:
      "FrameWorks and MistServer are developed by the same team. MistServer is the media engine at the heart of FrameWorks, handling ingest, protocol translation, delivery, recording, and playback. Processing can run on FrameWorks edges with SLA-backed capacity, on Livepeer for low-cost capacity that is quick to scale, or on your own edge with no FrameWorks processing charge.",
  },
  {
    question: "Who should use sovereign streaming infrastructure?",
    answer:
      "Sovereign streaming infrastructure is useful for broadcasters, community platforms, agencies, events, regulated teams, and builders who need predictable video operations, data control, or deployment freedom. It is especially valuable when a team cannot rely on a single third-party cloud video vendor for every audience, region, or compliance requirement.",
  },
  {
    question: "How do AI agents interact with FrameWorks?",
    answer:
      "AI agents can discover FrameWorks through llms.txt, skill.json, and .well-known MCP metadata, then use wallet authentication, x402 payments, MCP tools, or GraphQL to operate streams and inspect diagnostics. The same platform APIs are available to human operators and autonomous tooling.",
  },
];

const LandingPage = () => {
  const [showPlayer, setShowPlayer] = useState(false);
  const [logoAnimationComplete, setLogoAnimationComplete] = useState(false);
  const [PlayerComponent, setPlayerComponent] = useState(null);
  const [demoState, setDemoState] = useState("booting");
  const [viewportWidth, setViewportWidth] = useState(1024);
  const demoFixtures =
    config.demoFixtures && config.demoFixtures.length > 0
      ? config.demoFixtures
      : [{ id: config.demoStreamName, label: "Demo" }];
  const [activeFixtureId, setActiveFixtureId] = useState(demoFixtures[0].id);

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

  useEffect(() => {
    const updateViewportWidth = () => setViewportWidth(window.innerWidth);
    updateViewportWidth();
    window.addEventListener("resize", updateViewportWidth);

    return () => window.removeEventListener("resize", updateViewportWidth);
  }, []);

  useEffect(() => {
    let active = true;
    let idleId;
    let fallbackTimer;

    const loadPlayer = () => {
      Promise.all([
        import("@livepeer-frameworks/player-react"),
        import("@livepeer-frameworks/player-react/player.css"),
      ])
        .then(([module]) => {
          if (active) setPlayerComponent(() => module.Player);
        })
        .catch(() => {
          if (active) setDemoState("error");
        });
    };

    const schedulePlayer = () => {
      if ("requestIdleCallback" in window) {
        idleId = window.requestIdleCallback(loadPlayer, { timeout: 1500 });
      } else {
        fallbackTimer = window.setTimeout(loadPlayer, 250);
      }
    };

    if (document.readyState === "complete") {
      schedulePlayer();
    } else {
      window.addEventListener("load", schedulePlayer, { once: true });
    }

    return () => {
      active = false;
      window.removeEventListener("load", schedulePlayer);
      if (idleId !== undefined) window.cancelIdleCallback(idleId);
      if (fallbackTimer !== undefined) window.clearTimeout(fallbackTimer);
    };
  }, []);

  const operatingPoints = [
    {
      id: "one-control-plane",
      title: "One control plane",
      description:
        "Streams, assets, routing, access, analytics, and billing share the same tenant-scoped GraphQL model.",
      tone: "accent",
    },
    {
      id: "people-and-agents",
      title: "For people and agents",
      description:
        "Use the dashboard, CLI, SDKs, or MCP. Skipper works from the same documentation and operational signals.",
      tone: "green",
    },
    {
      id: "deployment",
      title: (
        <>
          Your infrastructure or ours <SovereigntyNote />
        </>
      ),
      description:
        "Use managed regions, enroll your own media edges, or self-host. The application-facing model stays the same.",
      tone: "yellow",
    },
  ];

  const nativeCloudProof = [
    {
      title: "Media-native core",
      description:
        "MistServer, developed by our team for more than 15 years, handles ingest, protocol conversion, low-latency delivery, recording, and playback.",
      tone: "accent",
    },
    {
      title: "Operated infrastructure",
      description:
        "Use FrameWorks as a hosted service on infrastructure we operate, including bare metal, without assembling a chain of separate cloud-media products.",
      tone: "green",
    },
    {
      title: "Portable by design",
      description:
        "Move between hosted, hybrid, and self-hosted deployments without changing APIs or rebuilding the product on top.",
      tone: "yellow",
    },
    {
      title: "Processing on your terms",
      description:
        "Use FrameWorks edges for SLA-backed capacity, Livepeer for low-cost capacity that is quick to scale, or your own edge with no FrameWorks processing charge.",
      tone: "cyan",
    },
  ];

  const multistreamPoints = [
    {
      id: "push",
      title: "Native RTMP and SRT push",
      description:
        "MistServer pushes from the origin node itself, so there is no relay hop, no added latency, and no proxy bandwidth bill.",
    },
    {
      id: "presets",
      title: "Platform presets",
      description:
        "Twitch, YouTube, Facebook, Kick, and X are built in. Paste a stream key, or point at any custom RTMP, RTMPS, or SRT endpoint.",
    },
    {
      id: "lifecycle",
      title: "Automatic lifecycle",
      description:
        "Targets activate when the stream goes live and stop when it ends, with status, error reporting, and a per-target toggle.",
    },
  ];

  const freeTierFeatures = [
    "All self-hosted features",
    "Shared bandwidth pool",
    "Livepeer-backed compute",
    "Open source & permissive licenses",
    "No proprietary video-cloud dependency",
    "Web dashboard with analytics included",
  ];

  const paidPlanHighlights = [
    "Custom subdomains and hosted load balancers",
    "Reserved processing capacity and bandwidth pools",
    "Team collaboration and advanced analytics",
    "Priority support with 24/7 options",
  ];

  const pricingPlans = [
    {
      id: "free",
      tone: "green",
      badge: "Complete Open Stack",
      name: "Free Tier",
      price: "Free",
      period: "",
      description:
        "Complete self-hosting stack with shared pool access. Open source with permissive licenses: deploy it anywhere.",
      features: freeTierFeatures,
      ctaType: "external",
      ctaLabel: "Start Free",
      ctaHref: config.appUrl,
      note: "No credit card required · Deploy in minutes",
    },
    {
      id: "payg",
      tone: "cyan",
      badge: "Agent-Ready",
      name: "Pay As You Go",
      price: "€0.00055",
      period: "/delivered min",
      description:
        "Pay only when you use it. Wallet-friendly, with account controls for operators.",
      features: [
        "No subscription, no commitment",
        "Wallet auth with no email or signup form",
        "Top up via card, crypto, or gasless USDC",
        "Same rates as subscription tiers",
      ],
      ctaType: "internal",
      ctaLabel: "How wallet pay works",
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
        "Run on FrameWorks infrastructure, connect your own edges, or combine both. Add reserved capacity, advanced analytics, and hands-on support as you scale.",
      features: paidPlanHighlights,
      ctaType: "internal",
      ctaLabel: "Compare paid tiers",
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
        eyebrow="The independent video cloud"
        title="Run the whole live video path"
        description="Ingest, process, route, deliver, record, and analyze live video on media-server technology we have developed for more than 15 years. Run it on our infrastructure, your own, or both."
        support="MistServer at the core. Bare-metal capacity. Open APIs. Hosted, hybrid, or fully self-hosted."
        primaryAction={{
          label: "Start Free",
          href: config.appUrl,
          external: true,
          className: "cta-motion",
        }}
        secondaryAction={{
          label: "See pricing",
          to: "/pricing",
          icon: "auto",
          className: "cta-motion",
          variant: "secondary",
        }}
        footnote="Not a resale of hyperscaler media APIs. One video stack, operated your way."
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
                    Adaptive playback across WASM, WebCodecs, and every protocol
                  </p>
                  <p className="hero-player-card__caption">
                    Auto-negotiates transport and codec per device. Fully embeddable.
                  </p>
                </div>

                {/* Video Player - takes up most space */}
                <div className="hero-player-card__viewport">
                  <div className="hero-player-card__screen">
                    <div className="hero-player-card__stage">
                      {PlayerComponent ? (
                        <PlayerComponent
                          key={activeFixtureId}
                          contentId={activeFixtureId}
                          contentType="live"
                          options={{
                            autoplay: true,
                            muted: true,
                            controls: false,
                            playbackMode: "quality",
                            gatewayUrl: config.gatewayUrl,
                            // Opt in to diagnostic playback telemetry (default-off in the
                            // player); the hero demo feeds boot + viewer-QoE beacons too.
                            telemetry: { boot: true, session: true },
                          }}
                          onStateChange={(st) => setDemoState(st)}
                        />
                      ) : (
                        <div className="hero-player-card__standby" aria-hidden="true">
                          <img
                            src="/frameworks-dark-vertical-lockup.svg"
                            alt=""
                            width="300"
                            height="300"
                          />
                        </div>
                      )}
                    </div>
                  </div>
                  {demoFixtures.length > 1 && (
                    <div className="hero-player-card__fixture-toggle">
                      {demoFixtures.map((f) => (
                        <button
                          key={f.id}
                          type="button"
                          className={`hero-player-card__fixture-toggle-btn${
                            f.id === activeFixtureId ? " is-active" : ""
                          }`}
                          onClick={() => setActiveFixtureId(f.id)}
                        >
                          {f.label}
                        </button>
                      ))}
                    </div>
                  )}
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
                              willChange: "transform, filter",
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
        <Section id="why-frameworks" className="bg-brand-surface landing-section--proof">
          <SectionContainer>
            <MarketingBand preset="foundation" texturePattern="pinlines" textureNoise="film">
              <HeadlineStack
                eyebrow="Why FrameWorks"
                title="The media layer is ours. The deployment choice is yours."
                subtitle="FrameWorks is a complete video platform, not an API stitched across separate managed-media products."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end">
                    <MarketingCTAButton
                      intent="secondary"
                      to="/about#architecture"
                      label="See how FrameWorks is built"
                    />
                  </CTACluster>
                }
              />
              <MarketingFeatureWall
                items={nativeCloudProof}
                columns={4}
                stackAt="md"
                hover="subtle"
                stripe
              />
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        <Section id="journey" className="bg-brand-surface-muted landing-section--journey">
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
                eyebrow="One continuous system"
                title="Follow one stream through FrameWorks"
                subtitle="The sources and destinations change. The path stays legible: bring video in, shape it, send it, keep it, and know what happened."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end">
                    <MarketingCTAButton
                      intent="secondary"
                      href={`${docsBase}/platform/feature-matrix`}
                      label="All capabilities"
                      icon="book"
                    />
                  </CTACluster>
                }
              />
              <StreamJourney />
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        <Section id="media" className="bg-brand-surface landing-section--media">
          <SectionContainer>
            <MarketingBand preset="foundation" texturePattern="seams" textureNoise="film">
              <HeadlineStack
                eyebrow="01 · Bring it in / Shape it"
                title="The media path starts with the signal you already have"
                subtitle="Connect contribution equipment, browsers, upstream streams, and files. At the core of FrameWorks, MistServer handles the live signal while the platform places, authorizes, routes, and observes it from ingest through playback."
                align="left"
                underlineAlign="start"
              />
              <MarketingGridSplit align="stretch" stackAt="lg" seam ratio="narrow-wide">
                <div className="slab-zone">
                  <div className="slab-zone__body">
                    <IconList
                      items={[
                        {
                          title: "Keep the contribution path open",
                          description:
                            "Push over RTMP, E-RTMP, SRT, or WHIP; pull RTSP, RIST, HLS, MPEG-TS, SRT, and Mist sources.",
                        },
                        {
                          title: "Preserve broadcast meaning",
                          description:
                            "Carry signal metadata through the media path, including the expanding SCTE-35 and HLS input/output work.",
                        },
                        {
                          title: "Process where it makes sense",
                          description:
                            "Use FrameWorks edges for SLA-backed processing, Livepeer for low-cost capacity that is quick to scale, or your own edge with no FrameWorks processing charge.",
                        },
                        {
                          title: "Grow into physical production",
                          description:
                            "NDI, SDI, capture devices, PTZ control, composition, and server-side ad workflows join the same managed model as they land.",
                        },
                      ]}
                      variant="list"
                      indicator="dot"
                      headingLevel="h3"
                    />
                  </div>
                  <div className="slab-zone__actions">
                    <MarketingCTAButton
                      intent="secondary"
                      href={`${docsBase}/builders/streams`}
                      label="Ingest and stream docs"
                      icon="book"
                    />
                  </div>
                </div>
                <MediaControlPreview />
              </MarketingGridSplit>
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        <Section id="delivery" className="bg-brand-surface-muted landing-section--delivery">
          <SectionContainer>
            <MarketingBand
              preset="foundation"
              tone="cool"
              texturePattern="pinlines"
              textureNoise="film"
            >
              <HeadlineStack
                eyebrow="02 · Send it"
                title="One live source, routed to every destination"
                subtitle="Fan out to partner platforms and serve viewers from healthy, eligible edges. Routing, failover, playback policy, and delivery telemetry stay part of the same decision."
                align="left"
                underlineAlign="start"
              />
              <MarketingGridSplit align="stretch" stackAt="lg" seam ratio="wide-narrow">
                <div className="marketing-figure-cell">
                  <MultistreamFanout />
                </div>
                <div className="slab-zone">
                  <div className="slab-zone__body">
                    <IconList
                      items={multistreamPoints}
                      variant="list"
                      indicator="dot"
                      headingLevel="h3"
                    />
                  </div>
                  <div className="slab-zone__actions">
                    <MarketingCTAButton
                      intent="secondary"
                      href={`${docsBase}/builders/multistreaming`}
                      label="Multistreaming docs"
                      icon="book"
                    />
                  </div>
                </div>
              </MarketingGridSplit>
              <div className="journey-map-block">
                <div className="journey-map-block__copy">
                  <span>Routing is observable</span>
                  <p>
                    See viewer demand, eligible clusters, and the path chosen for each session,
                    rather than merely a CDN hostname.
                  </p>
                </div>
                <DeferredNetworkMap />
              </div>
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        <Section id="experience" className="bg-brand-surface landing-section--experience">
          <SectionContainer>
            <MarketingBand preset="foundation" texturePattern="seams" textureNoise="film">
              <HeadlineStack
                eyebrow="03–05 · Play it / Keep it / Know it"
                title="Live, replay, and insight stay on one timeline"
                subtitle="Playback and recording are not separate products, and analytics is not a detached reporting layer. The same stream identity connects delivery, viewer experience, durable media, and replay behavior."
                align="left"
                underlineAlign="start"
              />

              <div className="journey-outcomes">
                <div className="journey-outcome">
                  <div className="journey-outcome__header">
                    <span>03 · Play it</span>
                    <h3>Playback is part of operations</h3>
                    <p>
                      Adapt to the browser, enforce access, and return viewer evidence to the same
                      route and edge that served the session.
                    </p>
                  </div>
                  <IconList
                    items={[
                      {
                        title: "One adaptive player",
                        description:
                          "HLS, DASH, WebRTC, WebCodecs, WASM, and progressive playback selected per browser and stream.",
                      },
                      {
                        title: "Access at the boundary",
                        description:
                          "Public, JWT, or webhook authorization for live streams, recordings, clips, and VOD.",
                      },
                      {
                        title: "Close the delivery loop",
                        description:
                          "Startup, buffering, bitrate, frame drops, geography, and routing decisions share one session timeline.",
                      },
                    ]}
                    variant="list"
                    indicator="dot"
                    headingLevel="h4"
                  />
                  <MarketingCTAButton
                    intent="secondary"
                    href={`${docsBase}/builders/playback`}
                    label="Playback docs"
                    icon="book"
                    className="journey-outcome__action"
                  />
                </div>

                <div id="library" className="journey-outcome">
                  <div className="journey-outcome__header">
                    <span>04 · Keep it</span>
                    <h3>The live stream becomes durable media</h3>
                    <p>
                      Move from the live buffer to seekable replay, durable chapters, clips, and VOD
                      without creating a second workflow.
                    </p>
                  </div>
                  <IconList
                    items={[
                      {
                        title: "Record continuously",
                        description:
                          "Keep bounded live seekback while long-running recordings finalize into replayable chapters.",
                      },
                      {
                        title: "Cut what already exists",
                        description:
                          "Create clips from live buffers, DVR windows, or finalized media through one artifact lifecycle.",
                      },
                      {
                        title: "Retain with intent",
                        description:
                          "Apply asset policies, freeze durable copies, and understand the storage consequence.",
                      },
                    ]}
                    variant="list"
                    indicator="dot"
                    headingLevel="h4"
                  />
                  <MarketingCTAButton
                    intent="secondary"
                    href={`${docsBase}/builders/recordings`}
                    label="Recording docs"
                    icon="book"
                    className="journey-outcome__action"
                  />
                </div>
              </div>

              <DashboardFrame
                title="Stream intelligence"
                badge="Live + replay"
                tone="cyan"
                className="journey-insights"
              >
                <div className="journey-insights__header">
                  <div>
                    <span>05 · Know it</span>
                    <h3>One evidence model, not a separate analytics story</h3>
                  </div>
                  <p>
                    Viewer, route, rendition, and asset context survive from the first frame to the
                    most-replayed moment.
                  </p>
                </div>

                <StatRow stats={fx.liveVodStats} />

                <div className="journey-insights__grid">
                  <section
                    className="journey-insight-panel"
                    aria-labelledby="playback-insight-title"
                  >
                    <div className="journey-insight-panel__header">
                      <span>Delivery evidence</span>
                      <h4 id="playback-insight-title">What viewers actually experienced</h4>
                    </div>
                    <TrendChart
                      data={fx.qoeTrend}
                      series={fx.qoeSeries}
                      height={220}
                      leftTitle="Rebuffer / frame-drop %"
                      rightTitle="Bitrate (Mbps)"
                    />
                  </section>

                  <section className="journey-insight-panel" aria-labelledby="replay-insight-title">
                    <div className="journey-insight-panel__header">
                      <span>Content evidence</span>
                      <h4 id="replay-insight-title">What audiences kept watching</h4>
                    </div>
                    <div className="media-library-flow" aria-label="Live media library lifecycle">
                      <span>Live buffer</span>
                      <i aria-hidden="true" />
                      <span>DVR chapters</span>
                      <i aria-hidden="true" />
                      <span>Clips + VOD</span>
                    </div>
                    <RetentionCurve {...fx.retention} />
                  </section>
                </div>

                <div className="journey-insights__boot">
                  <div className="journey-insight-panel__header">
                    <span>Startup trace</span>
                    <h4>See where the first frame spent its time</h4>
                  </div>
                  <BootWaterfall
                    stages={fx.bootWaterfall.stages}
                    cacheHitRatio={fx.bootWaterfall.cacheHitRatio}
                  />
                </div>
              </DashboardFrame>
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        <Section id="operate" className="bg-brand-surface landing-section--operate">
          <SectionContainer>
            <MarketingBand
              preset="foundation"
              texturePattern="pinlines"
              textureNoise="film"
              textureBeam="soft"
            >
              <HeadlineStack
                eyebrow="06 · Operate it"
                title="The controls follow the same system"
                subtitle="People, applications, and agents operate the same tenant-scoped resources. The deployment can change without changing the product you build on top."
                align="left"
                underlineAlign="start"
              />
              <MarketingGridSplit align="stretch" stackAt="lg" seam ratio="narrow-wide">
                <div className="slab-zone">
                  <div className="slab-zone__body">
                    <IconList
                      items={operatingPoints}
                      variant="list"
                      indicator="dot"
                      headingLevel="h3"
                    />
                  </div>
                </div>
                <div className="marketing-figure-cell">
                  <DeploymentModes />
                </div>
              </MarketingGridSplit>
              <div className="journey-control-grid">
                <SdkCodePreview variant="flush" className="min-h-[420px]" />
                <SkipperConversationPreview />
              </div>
              <div className="skipper-agent-strip">
                <div className="skipper-agent-strip__header">
                  <span className="skipper-agent-strip__eyebrow">Agent-native operations</span>
                  <span className="skipper-agent-strip__title">
                    Discover via{" "}
                    <a
                      href={`https://${config.domain}/SKILL.md`}
                      className="skipper-agent-strip__link"
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      SKILL.md
                    </a>
                    , authenticate, fund, inspect, and operate through the same platform APIs.
                  </span>
                </div>
                <AgentPipelineStrip headingLevel="h3" />
              </div>
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        <Section className="bg-brand-surface landing-section--faq">
          <SectionContainer>
            <MarketingSlab variant="feature-panel">
              <MarketingSlabHeader
                as="h2"
                eyebrow="FAQ"
                title="The complete path, plainly answered"
                subtitle="Short answers about the media engine, control plane, deployment boundary, and developer surface."
              />
              <Accordion type="single" collapsible>
                {HOME_FAQS.map((faq, index) => (
                  <AccordionItem key={faq.question} value={`home-faq-${index}`}>
                    <AccordionTrigger>{faq.question}</AccordionTrigger>
                    <AccordionContent>
                      <div className="marketing-accordion__answer">
                        <p>{faq.answer}</p>
                      </div>
                    </AccordionContent>
                  </AccordionItem>
                ))}
              </Accordion>
            </MarketingSlab>
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
                subtitle="Start free with self-hosting. Upgrade for advanced processing, hosted services, and enterprise support when you need more."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end">
                    <MarketingCTAButton intent="secondary" to="/pricing" label="Compare plans" />
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
              title="Three ways to ship"
              description="Use our hosted video cloud, connect your own edge capacity, or deploy the full stack yourself. The same media engine and APIs follow you."
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
      </div>

      <MarketingScrollProgress />
    </div>
  );
};

export default LandingPage;
