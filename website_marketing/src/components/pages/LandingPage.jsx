import { motion } from 'framer-motion'
// Demo player wrapper with status/health integration
import { Player as FrameworksPlayer } from '@livepeer-frameworks/player-react'
import StatusTag from '../shared/StatusTag'
import {
  MarketingIconBadge,
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
  MarketingStackedSeam,
  IconList,
  SectionDivider
} from '@/components/marketing'
import { Section, SectionContainer } from '@/components/ui/section'
import { useState, useEffect } from 'react'
import config from '../../config'
import {
  VideoCameraIcon,
  FilmIcon,
  GlobeAltIcon,
  LockOpenIcon,
  CloudIcon,
  ServerStackIcon,
  ArrowPathIcon,
} from '@heroicons/react/24/outline'

const LandingPage = () => {
  const [showDemo, setShowDemo] = useState(false)
  const [showPlayer, setShowPlayer] = useState(false)
  const [logoAnimationComplete, setLogoAnimationComplete] = useState(false)
  const [demoState, setDemoState] = useState('booting')

  useEffect(() => {
    // Preload logo image so glitch strips can start immediately
    const img = new Image()
    img.src = '/frameworks-dark-vertical-lockup.svg'

    // Logo enters with glitch, then reveals player
    const playerTimer = setTimeout(() => {
      console.log('revealing player')
      setShowPlayer(true)
    }, 2000)

    // Remove logo element after fade animation completes
    const cleanupTimer = setTimeout(() => {
      setLogoAnimationComplete(true)
    }, 4200) // 2000ms delay + 2200ms animation duration

    const demoTimer = setTimeout(() => {
      setShowDemo(true)
    }, 1000)

    return () => {
      clearTimeout(playerTimer)
      clearTimeout(cleanupTimer)
      clearTimeout(demoTimer)
    }
  }, [])

  const uniqueFeatures = [
    {
      title: 'Drop-in AV Device Discovery',
      description:
        'Our binary automatically discovers and connects IP cameras, USB webcams, HDMI inputs, and other AV devices. Zero configuration required.',
      icon: VideoCameraIcon,
      tone: 'accent',
      badge: 'Industry First',
      status: 'soon',
      statusNote: 'In pipeline: shipping to alpha cohorts; discovery matrix still expanding.',
    },
    {
      title: 'Multi-stream Compositing',
      description:
        'Combine multiple input streams into one composite output with picture-in-picture, overlays, and OBS-style mixing capabilities.',
      icon: FilmIcon,
      tone: 'purple',
      badge: 'Advanced Feature',
      status: 'soon',
      statusNote: 'In pipeline: limited internal demos; capacity and UX hardening underway.',
    },
    {
      title: 'Hybrid Cloud + Self-hosted',
      description:
        'Combine our hosted service with your own nodes. One console to manage all your edge nodes worldwide.',
      icon: GlobeAltIcon,
      tone: 'green',
      badge: 'Unique Model',
      status: 'soon',
      statusNote: 'Invite-only: attaching your own edge nodes is being rolled out to pilots.',
    },
    {
      title: 'Public Domain Licensed',
      description:
        "No attribution required, no copyleft restrictions. Unlike typical 'open source' licenses, you truly own what you deploy.",
      icon: LockOpenIcon,
      tone: 'yellow',
      badge: 'Open Source',
    },
  ]

  const featureCards = uniqueFeatures.map((feature) => ({
    icon: feature.icon,
    iconTone: feature.tone,
    tone: feature.tone,
    badge: feature.badge,
    title: feature.title,
    description: feature.description,
    meta: feature.status ? (
      <StatusTag status={feature.status} note={feature.statusNote} className="justify-end" />
    ) : null,
    hover: 'subtle',
    stripe: true,
    flush: true,
    metaAlign: 'end',
  }))

  const freeTierFeatures = [
    'All self-hosted features',
    'Shared bandwidth pool',
    'Livepeer-backed compute',
    'Open source & permissive licenses',
    'No cloud dependencies - runs anywhere',
    'Web dashboard with analytics included',
  ]

  const paidPlanHighlights = [
    'Custom subdomains and hosted load balancers',
    'Reserved GPU hours and bandwidth pools',
    'Team collaboration and advanced analytics',
    'Priority support with 24/7 options',
  ]

  const pricingPlans = [
    {
      id: 'free',
      tone: 'green',
      badge: 'Backed by Livepeer',
      name: 'Free Tier',
      price: 'Free',
      period: '',
      description:
        'Complete self-hosting stack with shared pool access. Open source with permissive licenses: deploy it anywhere.',
      features: freeTierFeatures,
      ctaType: 'external',
      ctalabel: 'Start Free',
      ctaHref: config.appUrl,
      note: 'No credit card required · Deploy in minutes',
    },
    {
      id: 'paid',
      tone: 'purple',
      badge: 'Paid plans',
      name: 'Hybrid & Hosted',
      price: '€50+',
      period: '/month',
      description:
        'GPU-intensive features like AI processing and multi-stream compositing, plus hosted services and enterprise support.',
      features: paidPlanHighlights,
      ctaType: 'internal',
      ctaLabel: 'View All Plans',
      ctaTo: '/pricing',
      note: 'Supporter · Developer · Production · Enterprise',
    },
  ]

  const hybridBenefits = [
    {
      title: 'One Console for Everything',
      description:
        'Manage self-hosted nodes, hosted processing, and hybrid deployments from a single dashboard.',
      tone: 'accent',
    },
    {
      title: 'Seamless Failover',
      description:
        'Automatic failover between your nodes and FrameWorks hosted services keeps streams resilient.',
      tone: 'green',
    },
    {
      title: 'Cost Optimization',
      description: 'Run base load on your hardware and burst into our network—or Livepeer’s—for peak demand.',
      tone: 'yellow',
    },
  ]

  const deploymentOptions = [
    {
      title: 'Fully Hosted',
      description:
        'Use our global infrastructure with generous free tier capacity and turnkey operations.',
      tone: 'accent',
      icon: CloudIcon,
    },
    {
      title: 'Self-Hosted',
      description:
        'Deploy FrameWorks on your own infrastructure and manage everything through one console.',
      tone: 'green',
      icon: ServerStackIcon,
    },
    {
      title: 'Hybrid',
      description: 'Blend hosted processing with your nodes for optimal cost, performance, and redundancy.',
      tone: 'yellow',
      icon: ArrowPathIcon,
    },
  ]

  const landingHeroAccents = [
    {
      kind: 'beam',
      x: 18,
      y: 34,
      width: 'clamp(28rem, 52vw, 44rem)',
      height: 'clamp(18rem, 32vw, 28rem)',
      rotate: -14,
      fill: 'linear-gradient(130deg, rgba(122, 162, 247, 0.38), rgba(30, 42, 84, 0.18))',
      opacity: 0.55,
      radius: '48px',
    },
    {
      kind: 'beam',
      x: 78,
      y: 28,
      width: 'clamp(22rem, 42vw, 34rem)',
      height: 'clamp(16rem, 26vw, 22rem)',
      rotate: 16,
      fill: 'linear-gradient(150deg, rgba(69, 208, 255, 0.3), rgba(18, 24, 48, 0.16))',
      opacity: 0.52,
      radius: '42px',
    },
    {
      kind: 'spot',
      x: 24,
      y: 78,
      width: 'clamp(22rem, 46vw, 34rem)',
      height: 'clamp(22rem, 46vw, 34rem)',
      fill: 'radial-gradient(circle, rgba(125, 207, 255, 0.26) 0%, transparent 68%)',
      opacity: 0.4,
      blur: '90px',
    },
  ]

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
        support="Self-hosted or cloud • No vendor lock-in • Public domain licensed • True ownership"
        primaryAction={{
          label: 'Start Free',
          href: config.appUrl,
          external: true,
          className: 'cta-motion',
        }}
        secondaryAction={{
          label: 'View Pricing',
          to: '/pricing',
          icon: 'auto',
          className: 'cta-motion',
          variant: 'secondary',
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
            <div className="hero-player-card relative overflow-hidden">
              <div className="relative z-10 flex flex-col min-h-[420px]">
                {/* Frame Top */}
                <div className="frame-top text-center mb-6">
                  <div className="inline-flex items-center gap-3 mb-2">
                    <div className="w-3 h-3 bg-brand-comment rounded-full"></div>
                    <h2 className="text-xl sm:text-2xl font-bold text-foreground">
                      FrameWorks Demo
                    </h2>
                  </div>
                  <div className="flex justify-center">
                    {(() => {
                      const s = demoState
                      const map = {
                        booting: { label: 'BOOTING', cls: 'bg-brand-muted-soft text-brand-muted border-[hsl(var(--brand-comment)/0.4)]' },
                        gateway_loading: { label: 'RESOLVING', cls: 'bg-brand-muted-soft text-brand-muted border-[hsl(var(--brand-comment)/0.4)]' },
                        gateway_ready: { label: 'ENDPOINT READY', cls: 'bg-primary/20 text-primary border-primary/40' },
                        gateway_error: { label: 'GATEWAY ERROR', cls: 'bg-red-500/20 text-red-400 border-red-500/40' },
                        no_endpoint: { label: 'WAITING FOR ENDPOINT', cls: 'bg-brand-muted-soft text-brand-muted border-[hsl(var(--brand-comment)/0.4)]' },
                        selecting_player: { label: 'SELECTING PLAYER', cls: 'bg-primary/20 text-primary border-primary/40' },
                        connecting: { label: 'CONNECTING', cls: 'bg-primary/20 text-primary border-primary/40' },
                        buffering: { label: 'BUFFERING', cls: 'bg-yellow-500/20 text-yellow-400 border-yellow-500/40' },
                        playing: { label: 'STREAMING', cls: 'bg-green-500/20 text-green-400 border-green-500/40' },
                        paused: { label: 'PAUSED', cls: 'bg-brand-muted-soft text-brand-muted border-[hsl(var(--brand-comment)/0.4)]' },
                        ended: { label: 'ENDED', cls: 'bg-brand-muted-soft text-brand-muted border-[hsl(var(--brand-comment)/0.4)]' },
                        error: { label: 'ERROR', cls: 'bg-red-500/20 text-red-400 border-red-500/40' },
                        destroyed: { label: 'STOPPED', cls: 'bg-brand-muted-soft text-brand-muted border-[hsl(var(--brand-comment)/0.4)]' }
                      }
                      const m = map[s] || map.booting
                      return <span className={`hero-demo-status ${m.cls}`}>{m.label}</span>
                    })()}
                  </div>
                  <p className="text-muted-foreground text-sm mt-3">
                    Watch our streaming infrastructure in action.
                  </p>
                </div>

                {/* Video Player - takes up most space */}
                <div className="relative flex-1 mb-6">
                  <div className="w-full h-full rounded-xl overflow-hidden bg-brand-surface-strong shadow-2xl border border-brand-surface">
                    <div className="relative w-full h-[320px] sm:h-[380px]">
                      <FrameworksPlayer
                        contentId={config.demoStreamName}
                        contentType="live"
                        options={{ autoplay: true, muted: true, controls: false, gatewayUrl: config.gatewayUrl || undefined }}
                        onStateChange={(st) => setDemoState(st)}
                      />
                    </div>
                  </div>
                </div>
              </div>

              {/* Logo Overlay - dissolves to reveal player */}
              {!logoAnimationComplete && (
                <motion.div
                  className="absolute inset-0 max-w-full max-h-full flex items-center justify-center bg-background rounded-xl z-50"
                  initial={{ opacity: 1 }}
                  animate={{
                    opacity: showPlayer ? 0 : 1,
                    scale: showPlayer ? 1.05 : 1
                  }}
                  transition={{
                    duration: 2,
                    ease: [0.25, 0.46, 0.45, 0.94],
                    opacity: { duration: 2 },
                    scale: { duration: 2.2 }
                  }}
                >
                  {/* Logo Entry Animation */}
                  <motion.div
                    className="relative w-full h-full"
                    initial={{ scale: 0.8, opacity: 0, y: 20 }}
                    animate={{
                      scale: 1,
                      opacity: 1,
                      y: 0
                    }}
                    transition={{
                      duration: 0.3,
                      ease: [0.25, 0.46, 0.45, 0.94]
                    }}
                  >
                    {/* Main logo - centered vertical lockup */}
                    <div className="absolute inset-0 w-full h-full rounded-xl shadow-2xl neon-glow flex items-center justify-center overflow-hidden bg-black">
                      <img
                        src="/frameworks-dark-vertical-lockup.svg"
                        alt="FrameWorks"
                        className="w-2/3 max-w-[300px] h-auto"
                      />
                    </div>
                  </motion.div>

                  {/* Glitch Effect */}
                  <div
                    className="absolute inset-0 w-full h-full rounded-xl"
                    style={{
                      overflow: 'visible',
                      transform: 'translateZ(0)',
                      willChange: 'transform'
                    }}
                  >
                    {(() => {
                      const strips = [];
                      let currentPosition = 0;

                      const viewportWidth = typeof window !== 'undefined' ? window.innerWidth : 1024;
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

                      for (let i = 0; i < 15; i++) {
                        const stripHeight = 20 + Math.random() * 40;
                        const rawGlitchX1 = (Math.random() - 0.5) * 40;
                        const rawGlitchX2 = (Math.random() - 0.5) * 40;
                        const glitchX1 = Math.max(-maxSafeTranslation, Math.min(maxSafeTranslation, rawGlitchX1));
                        const glitchX2 = Math.max(-maxSafeTranslation, Math.min(maxSafeTranslation, rawGlitchX2));
                        const glitchHue1 = (Math.random() - 0.5) * 90;
                        const glitchHue2 = (Math.random() - 0.5) * 90;
                        const animationDelay = i < 3 ? 0 : i < 8 ? Math.random() * 0.5 : Math.random() * 1.5;
                        const animationDuration = 2000 + Math.random() * 3000;
                        const animationName = `glitch-${(i % 6) + 5}`;

                        strips.push(
                          <div
                            key={i}
                            className={`absolute${currentPosition === 0 ? ' rounded-t-xl' : i === 14 ? ' rounded-b-xl' : ''}`}
                            style={{
                              left: `-${stripExtension}px`,
                              right: `-${stripExtension}px`,
                              top: `${currentPosition}px`,
                              height: `${stripHeight}px`,
                              backgroundImage: 'url(/frameworks-dark-vertical-lockup.svg)',
                              backgroundSize: `calc(100% - ${stripExtension * 2}px) auto`,
                              backgroundPosition: `${stripExtension}px -${currentPosition}px`,
                              backgroundRepeat: 'no-repeat',
                              overflow: currentPosition === 0 || i === 14 ? 'hidden' : 'visible',
                              '--glitch-x-1': `${glitchX1}px`,
                              '--glitch-x-2': `${glitchX2}px`,
                              '--glitch-hue-1': `${glitchHue1}deg`,
                              '--glitch-hue-2': `${glitchHue2}deg`,
                              animationName: animationName,
                              animationDuration: `${animationDuration}ms`,
                              animationDelay: `${animationDelay}s`,
                              animationIterationCount: 'infinite',
                              animationDirection: 'alternate',
                              animationTimingFunction: 'linear',
                              imageRendering: 'pixelated'
                            }}
                          />
                        );

                        currentPosition += stripHeight;
                      }

                      return strips;
                    })()}
                  </div>
                </motion.div>
              )}
            </div>
          </motion.div>
        }
      />

      {/* CSS for glitch animations */}
      <style>{`
        @keyframes glitch-5 {
          0.00%, 33.33%, 43.33%, 66.67%, 76.67%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          33.43%, 43.23% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff0040);
          }
          66.77%, 76.57% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-2px 0 0 #00ffff);
          }
        }
        
        @keyframes glitch-6 {
          0.00%, 25.00%, 35.00%, 50.00%, 60.00%, 75.00%, 85.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          25.10%, 34.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(1px 0 0 #ff0040);
          }
          50.10%, 59.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-1px 0 0 #00ffff);
          }
          75.10%, 84.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff4000);
          }
        }
        
        @keyframes glitch-7 {
          0.00%, 20.00%, 30.00%, 40.00%, 50.00%, 70.00%, 80.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          20.10%, 29.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(3px 0 0 #ff0040);
          }
          40.10%, 49.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-3px 0 0 #00ffff);
          }
          70.10%, 79.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(1px 0 0 #ff4000);
          }
        }
        
        @keyframes glitch-8 {
          0.00%, 15.00%, 25.00%, 45.00%, 55.00%, 65.00%, 75.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          15.10%, 24.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff0040);
          }
          45.10%, 54.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-2px 0 0 #00ffff);
          }
          65.10%, 74.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(3px 0 0 #ff4000);
          }
        }
        
        @keyframes glitch-9 {
          0.00%, 10.00%, 20.00%, 60.00%, 70.00%, 90.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          10.10%, 19.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(1px 0 0 #ff0040);
          }
          60.10%, 69.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-1px 0 0 #00ffff);
          }
          90.10%, 99.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff4000);
          }
        }
        
        @keyframes glitch-10 {
          0.00%, 5.00%, 15.00%, 35.00%, 45.00%, 55.00%, 65.00%, 85.00%, 95.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          5.10%, 14.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(4px 0 0 #ff0040);
          }
          35.10%, 44.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-4px 0 0 #00ffff);
          }
          55.10%, 64.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff4000);
          }
          85.10%, 94.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-2px 0 0 #40ff00);
          }
        }
      `}</style>

      <SectionDivider />

      <div className="flex flex-col">
        {/* Rest of the sections remain the same */}
        <Section className="bg-brand-surface-muted">
          <SectionContainer>
            <MarketingBand surface="panel" contentClassName="marketing-band__inner--flush">
              <HeadlineStack
                eyebrow="Platform"
                title="Key Platform Features"
                subtitle="Advanced streaming capabilities with hybrid deployment and full self-hosting support."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end">
                    <MarketingCTAButton intent="secondary" to="/about" label="Explore More" />
                  </CTACluster>
                }
              />
              <MarketingFeatureWall
                items={featureCards}
                columns={2}
              />
            </MarketingBand>
          </SectionContainer>
        </Section>

        <SectionDivider />

        {/* Pricing Preview */}
        <Section>
          <SectionContainer>
            <MarketingBand surface="panel">
              <HeadlineStack
                eyebrow="Pricing"
                title={
                  <>
                    <span className="transparent-word" data-text="Transparent">
                      Transparent
                    </span>{' '}
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
                columns={2}
                stackAt="md"
                className="landing-pricing-grid"
                items={pricingPlans.map((plan, index) => {
                  const ctaProps =
                    plan.ctaType === 'external'
                      ? { href: plan.ctaHref, external: true }
                      : { to: plan.ctaTo }

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
                        intent={plan.ctaType === 'external' ? 'primary' : 'secondary'}
                        label={plan.ctaLabel}
                        className="w-full justify-center"
                        {...ctaProps}
                      />
                    ),
                    footnote: plan.note,
                    motionDelay: index * 0.12,
                  }
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

        {/* Hybrid Deployment */}
        <Section className="bg-brand-surface-soft">
          <SectionContainer>
            <MarketingBand surface="none" contentClassName="marketing-band__inner--flush">
              <MarketingGridSplit align="start" stackAt="lg" gap="lg">
                <motion.div
                  initial={{ opacity: 0, x: -26 }}
                  whileInView={{ opacity: 1, x: 0 }}
                  viewport={{ once: true }}
                  transition={{ duration: 0.55 }}
                >
                  <HeadlineStack
                    title="Hybrid: Cloud + Self-Hosted"
                    subtitle="Why choose between cloud and self-hosted? Operate on your own terms and shift workloads whenever it makes sense."
                    align="left"
                  />
                  <IconList
                    items={hybridBenefits}
                    variant="list"
                    indicator="dot"
                  />
                </motion.div>

                <motion.div
                  initial={{ opacity: 0, x: 26 }}
                  whileInView={{ opacity: 1, x: 0 }}
                  viewport={{ once: true }}
                  transition={{ duration: 0.55, delay: 0.1 }}
                >
                  <MarketingBand surface="panel" className="deployment-panel">
                    <div className="deployment-panel__header">
                      <h3 className="deployment-panel__title">Deployment Options</h3>
                    </div>
                    <MarketingFeatureWall
                      items={deploymentOptions.map((option) => ({
                        title: option.title,
                        description: option.description,
                        icon: option.icon,
                        tone: option.tone,
                        iconTone: option.tone,
                      }))}
                      columns={1}
                      className="marketing-feature-wall--single marketing-feature-wall--deployment"
                    />
                  </MarketingBand>
                </motion.div>
              </MarketingGridSplit>
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
              description="Full SaaS when you want it easy. Full self-hosting when you want control. Your call."
              variant="band"
              primaryAction={{
                label: 'Start Free',
                href: config.appUrl,
                external: true,
              }}
              secondaryAction={[
                {
                  label: 'Talk to our team',
                  to: '/contact',
                },
                {
                  label: 'View Open Source',
                  href: config.githubUrl,
                  icon: 'auto',
                  external: true,
                },
              ]}
            />
          </motion.div>
        </Section>

      </div>

      <MarketingScrollProgress />
    </div>
  )
}

export default LandingPage 
