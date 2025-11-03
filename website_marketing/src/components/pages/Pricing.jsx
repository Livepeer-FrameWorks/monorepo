import { useEffect, useState } from 'react'
import { motion } from 'framer-motion'
import { Link } from 'react-router-dom'
import config from '../../config'
import InfoTooltip from '../shared/InfoTooltip'
import SavingsCalculator from '../shared/SavingsCalculator'
import { Section, SectionContainer } from '@/components/ui/section'
import {
  MarketingHero,
  MarketingSlab,
  MarketingSlabHeader,
  MarketingIconBadge,
  IconList,
  MarketingFinalCTA,
  MarketingScrollProgress,
  MarketingBand,
  MarketingComparisonGrid,
  MarketingComparisonCard,
  MarketingCTAButton,
  HeadlineStack,
  MarketingStack,
  MarketingFeatureWall,
  MarketingFeatureCard,
  CTACluster,
  SectionDivider,
  ComparisonTable,
  PricingTierOutline,
} from '@/components/marketing'
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from '@/components/ui/accordion'
import { cn } from '@/lib/utils'
import {
  HomeIcon,
  GlobeAltIcon,
  CloudIcon,
  BuildingOfficeIcon,
  ArrowTopRightOnSquareIcon,
  BanknotesIcon,
} from '@heroicons/react/24/outline'

const freeTier = {
  id: 'free',
  name: 'Free Tier',
  price: 'Free',
  period: '',
  description: 'Self-hosted features & free access to transcoding and shared bandwidth pool.',
  features: [
    'All self-hosted features',
    'Livepeer-backed compute',
    'Seamless failover and load balancing',
    'Unified dashboard',
    'Basic analytics'
  ],
  limitations: ['Self-host only (no SLA)', 'No GPU hours for AI or multiview', 'Watermarked playback in player'],
  badge: 'No Surprise Bills',
  ctaLabel: 'Start Free',
  ctaLink: config.appUrl,
}

const paidTiers = [
  {
    id: 'supporter',
    tone: 'accent',
    name: 'Supporter',
    price: '€79',
    period: '/month',
    dailyCost: '~€2.63/day',
    description: 'Starter allowances with hosted load balancer and custom subdomain.',
    features: [
      <span key="delivery">150,000 delivered minutes included <InfoTooltip>Overage €0.00049/min</InfoTooltip></span>,
      <span key="gpu">10 GPU-hours (shared) <InfoTooltip>Shared GPU fair-use during beta</InfoTooltip></span>,
      'Hosted load balancer',
      'Custom subdomain (*.frameworks.network)',
      'Transparent usage reporting',
    ],
    limitations: ['Suitable for ~100–300 concurrent viewers (adaptive)', 'Best-effort; no SLA during beta'],
    note: 'Includes hosted LB + subdomain',
    ctaLabel: 'Get Started',
    ctaLink: config.appUrl,
  },
  {
    id: 'developer',
    tone: 'purple',
    name: 'Developer',
    price: '€249',
    period: '/month',
    dailyCost: '~€8.30/day',
    description: 'Expanded allowances, collaboration tooling, and shared GPU priority.',
    features: [
      <span key="delivery">500,000 delivered minutes included <InfoTooltip>Overage €0.00047/min</InfoTooltip></span>,
      <span key="gpu">50 GPU-hours (shared, priority) <InfoTooltip>Shared GPU fair-use during beta</InfoTooltip></span>,
      'Team collaboration features',
      'Advanced analytics',
      'Priority support',
    ],
    limitations: ['Suitable for ~500–1,000 concurrent viewers (adaptive)', 'Standard SLA at GA'],
    note: 'Priority support & analytics included',
    ctaLabel: 'Get Started',
    ctaLink: config.appUrl,
  },
  {
    id: 'production',
    tone: 'yellow',
    name: 'Production',
    price: '€999',
    period: '/month',
    dailyCost: '~€33.30/day',
    description: 'High allowances, dedicated options, and enterprise support.',
    features: [
      <span key="delivery">2,000,000 delivered minutes included <InfoTooltip>Overage €0.00045/min</InfoTooltip></span>,
      <span key="gpu">250 GPU-hours <InfoTooltip>Dedicated options quoted</InfoTooltip></span>,
      'SLA & 24/7 support',
      'Dedicated capacity options',
      'Live dashboard',
    ],
    limitations: ['Suitable for ~2,000–5,000 concurrent viewers (adaptive)'],
    note: 'Reserved capacity & SLA coverage available',
    ctaLabel: 'Talk to us',
    ctaLink: '/contact',
  },
]

const enterpriseTier = {
  name: 'Enterprise',
  price: 'Custom pricing',
  headline: 'For high-bandwidth projects and fully custom deployments.',
  description: "Ready to be the next Netflix, Twitch, or YouTube when you're building at massive scale.",
  bullets: [
    'Custom feature development and white-label solutions',
    'Private deployments or co-managed operations with our engineers',
    'Unlimited bandwidth backed by dedicated GPU infrastructure',
    'Custom SLAs with training, consulting, and on-call support',
    'Flexible billing arrangements tailored to your organization',
  ],
}

const gpuFeatureMatrix = [
  {
    feature: 'Transcoding',
    description: 'Real-time video transcoding to multiple formats and bitrates.',
    tiers: {
      free: 'Powered by Livepeer network',
      supporter: 'Powered by Livepeer network',
      developer: 'Powered by Livepeer network',
      production: 'Dedicated processing allocation',
    },
  },
  {
    feature: 'AI processing',
    description: 'Advanced video analysis, clipping, and inference workloads.',
    tiers: {
      free: 'Only self-hosting',
      supporter: 'Only self-hosting',
      developer: 'Rate-limited access',
      production: 'Dedicated allocation',
    },
  },
  {
    feature: 'Multi-stream compositing',
    description: 'Combine multiple streams with studio-style mixing and effects.',
    tiers: {
      free: 'Only self-hosting',
      supporter: 'Only self-hosting',
      developer: 'Rate-limited access',
      production: 'Dedicated allocation',
    },
  },
]

const tierColumns = [
  { key: 'free', label: 'Free' },
  { key: 'supporter', label: 'Supporter' },
  { key: 'developer', label: 'Developer' },
  { key: 'production', label: 'Production' },
]

const deploymentOptions = [
  {
    id: 'self-hosted',
    title: 'Self-hosted edge network',
    tagline: 'Own every node',
    tone: 'accent',
    icon: HomeIcon,
    summary: 'Operate ingest, control plane, and delivery on your hardware with FrameWorks automation.',
    modal: {
      description:
        'Keep everything sovereign by running Mist ingest, delivery, and the FrameWorks control plane inside your footprint. We provide the automation, observability, and guardrails—you keep the keys.',
      bullets: [
        'Declarative configs for bare metal, VMs, or Kubernetes with drift detection and safe rollbacks.',
        'Joint dashboards, runbooks, and on-call assistance without surrendering shell access.',
        'Optional burst into hosted GPU, CDN, or orchestration capacity when traffic surges.',
      ],
    },
  },
  {
    id: 'hybrid-network',
    title: 'Hybrid network federation',
    tagline: 'Blend your POPs with ours',
    tone: 'green',
    icon: GlobeAltIcon,
    summary: 'Blend your POPs with FrameWorks GPU, AI, and CDN services for elastic failover.',
    modal: {
      description:
        'Federate your existing POPs with FrameWorks so you can shift workloads between your sites and ours with one control plane and one set of dashboards.',
      bullets: [
        'Automatic failover and traffic steering with full audit trails and policy controls.',
        'Unified dashboards for bandwidth, viewer load, GPU draw, and AI usage across every region.',
        'Usage-based pricing that keeps burst capacity transparent for finance and network ops.',
      ],
    },
  },
  {
    id: 'managed-pipeline',
    title: 'Managed FrameWorks pipeline',
    tagline: 'We operate the stack',
    tone: 'purple',
    icon: CloudIcon,
    summary: 'Hand ingest, load balancing, delivery, and observability to our SRE crews.',
    modal: {
      description:
        'Let FrameWorks SREs run ingest, delivery, observability, and GPU orchestration while your team focuses on product velocity—you keep full visibility and control.',
      bullets: [
        'SLO-backed operations with shared runbooks, paging rotations, and direct-to-engineer escalation.',
        'Managed load balancers, CDN federation, GPU scheduling, and AI pipelines with per-tier usage breakdowns.',
        'Service credits to expand into new regions, workloads, or codecs without retooling pipelines.',
      ],
    },
  },
  {
    id: 'enterprise-custom',
    title: 'Enterprise & regulated',
    tagline: 'Co-managed scale',
    tone: 'yellow',
    icon: BuildingOfficeIcon,
    summary: 'Reserved clusters, private consoles, and compliance workflows tailored to regulated industries.',
    modal: {
      description:
        'Design bespoke deployments alongside our engineers when you need dedicated capacity, deep compliance, and co-managed operations across regulated environments.',
      bullets: [
        'Security and compliance reviews aligned to your policies with artifact-ready evidence packs.',
        'Custom SLAs, reserved GPU/edge pools, and direct engineer-to-engineer escalation.',
        'Automation, training, billing, and reporting tailored to your internal tooling and finance flows.',
      ],
    },
  },
]

const faqs = [
  {
    question: 'What does beta pricing include?',
    answer:
      'Generous allowances, transparent overages, and shared GPU capacity. As we scale, pricing may adjust, but you keep the allowances you signed up for throughout beta.',
  },
  {
    question: 'Can I mix self-hosted and hosted workloads?',
    answer:
      'Yes. Every tier includes self-hosting. Supporter and above add hosted load balancers and GPU capacity. You can operate your own edge while tapping FrameWorks or Livepeer compute on demand.',
  },
  {
    question: 'When should I upgrade to Production?',
    answer:
      'Upgrade once you have steady audiences, need dedicated GPU time, or require SLA-backed response. Production is tuned for 2,000–5,000 concurrent viewers with reserved capacity.',
  },
  {
    question: 'How does the hosted load balancer work?',
    answer:
      'From Supporter tier onward, we run Foghorn load balancers for you. They handle routing, failover, certificates, and scaling so your ingest infrastructure stays resilient without extra ops.',
  },
  {
    question: 'What if we outgrow the published tiers?',
    answer:
      'Enterprise engagements unlock private deployments, custom SLAs, security reviews, and co-managed operations. We scope the stack with you so you retain control while leaning on our crews.',
  },
]

const pricingHeroAccents = [
  {
    kind: 'beam',
    x: 14,
    y: 32,
    width: 'clamp(28rem, 46vw, 36rem)',
    height: 'clamp(18rem, 32vw, 26rem)',
    rotate: -16,
    fill: 'linear-gradient(150deg, rgba(122, 162, 247, 0.32), rgba(18, 22, 38, 0.18))',
    opacity: 0.5,
    radius: '44px',
  },
  {
    kind: 'beam',
    x: 82,
    y: 24,
    width: 'clamp(18rem, 34vw, 28rem)',
    height: 'clamp(16rem, 28vw, 22rem)',
    rotate: 24,
    fill: 'linear-gradient(155deg, rgba(125, 207, 255, 0.26), rgba(24, 30, 48, 0.16))',
    opacity: 0.46,
    radius: '36px',
  },
  {
    kind: 'spot',
    x: 58,
    y: 78,
    width: 'clamp(26rem, 52vw, 40rem)',
    height: 'clamp(26rem, 52vw, 40rem)',
    fill: 'radial-gradient(circle, rgba(63, 78, 150, 0.26) 0%, transparent 68%)',
    opacity: 0.32,
    blur: '110px',
  },
  {
    kind: 'beam',
    x: 8,
    y: 80,
    width: 'clamp(18rem, 32vw, 26rem)',
    height: 'clamp(16rem, 30vw, 24rem)',
    rotate: -8,
    fill: 'linear-gradient(165deg, rgba(59, 117, 214, 0.22), rgba(16, 20, 32, 0.12))',
    opacity: 0.36,
    radius: '38px',
  },
]

const pricingHeroHighlights = [
  {
    title: 'Self-hosting included',
    description: 'Deploy the full FrameWorks control plane on your infrastructure with no licensing fees.',
    tone: 'accent',
    icon: HomeIcon,
  },
  {
    title: 'Hosted capacity on demand',
    description: 'Burst into Livepeer-backed GPU and bandwidth pools whenever viewership spikes.',
    tone: 'green',
    icon: CloudIcon,
  },
  {
    title: 'Transparent overages',
    description: 'Published beta rates for GPU minutes and delivered bandwidth—no hidden multipliers.',
    tone: 'yellow',
    icon: BanknotesIcon,
  },
]

const Pricing = () => {
  const [activeOption, setActiveOption] = useState(null)

  useEffect(() => {
    if (!activeOption) return

    const handleKeyDown = (event) => {
      if (event.key === 'Escape') {
        setActiveOption(null)
      }
    }

    const originalOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    document.addEventListener('keydown', handleKeyDown)

    return () => {
      document.body.style.overflow = originalOverflow
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [activeOption])

  const tierCards = paidTiers.map((plan, index) => {
    const isInternalLink = plan.ctaLink.startsWith('/')
    const actionProps = isInternalLink
      ? { to: plan.ctaLink }
      : { href: plan.ctaLink, external: true }

    return {
      id: plan.id,
      tone: plan.tone ?? 'accent',
      title: plan.name,
      price: plan.price,
      period: plan.period,
      meta: plan.dailyCost,
      description: plan.description,
      features: plan.features,
      limitations: plan.limitations,
      footnote: plan.note,
      featured: plan.id === 'developer',
      action: (
        <MarketingCTAButton
          intent={plan.id === 'production' ? 'secondary' : 'primary'}
          label={plan.ctaLabel}
          className="w-full justify-center"
          {...actionProps}
        />
      ),
      motionDelay: index * 0.08,
    }
  })

  const paidCards = tierCards

  const gpuColumns = tierColumns.map((tier) => ({
    key: tier.key,
    label: tier.label,
  }))

  const gpuRows = gpuFeatureMatrix.map((entry) => {
    const tierCells = tierColumns.reduce(
      (acc, tier) => ({
        ...acc,
        [tier.key]: entry.tiers[tier.key],
      }),
      {}
    )

    return {
      key: entry.feature,
      label: (
        <div className="pricing-gpu-feature">
          <span className="pricing-gpu-feature__title">{entry.feature}</span>
          <p className="pricing-gpu-feature__copy">{entry.description}</p>
        </div>
      ),
      ...tierCells,
    }
  })

  return (
    <div className="pt-16">
      <MarketingHero
        seed="/pricing"
        align="left"
        layout="split"
        mediaPosition="right"
        surface="gradient"
        surfaceTone="accent"
        surfaceIntensity="raised"
        accents={pricingHeroAccents}
        title={
          <>
            <span className="transparent-word" data-text="Transparent">
              Transparent
            </span>{' '}
            pricing
          </>
        }
        description="Start free with full self-hosting capabilities. Upgrade as you grow for hosted services, dedicated bandwidth, and GPU processing across FrameWorks and the Livepeer network."
        support={
          <IconList
            items={pricingHeroHighlights.map((highlight) => {
              const Icon = highlight.icon
              return {
                title: highlight.title,
                description: highlight.description,
                icon: <Icon className="h-5 w-5 text-foreground/90" />,
                tone: highlight.tone,
              }
            })}
            columns={3}
            stackAt="md"
          />
        }
      />

      <SectionDivider />

      <Section className="panel">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <HeadlineStack
              eyebrow="Free tier"
              title="Start free with the self-hosted control plane"
              subtitle="Deploy FrameWorks on your own infrastructure and keep sovereignty over your workloads."
              align="left"
              underlineAlign="start"
            />
            <PricingTierOutline
              tone="accent"
              badge={freeTier.badge}
              name={freeTier.name}
              price={freeTier.price}
              period={freeTier.period}
              description={freeTier.description}
              actions={
                <CTACluster align="start">
                  <MarketingCTAButton intent="primary" label={freeTier.ctaLabel} href={freeTier.ctaLink} external />
                </CTACluster>
              }
              sections={[
                {
                  title: "What’s included",
                  items: freeTier.features,
                },
                {
                  title: 'Limitations',
                  items: freeTier.limitations,
                  bullet: 'dash',
                },
              ]}
              className="mt-8"
            />
          </motion.div>
        </SectionContainer>

        <SectionContainer className="mt-6">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand surface="panel" contentClassName="marketing-band__inner--flush">
              <HeadlineStack
                eyebrow="Paid tiers"
                title="Upgrade for more"
                subtitle="Add hosted services, GPU allowances, and enterprise capabilities as your workloads scale."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end" wrap>
                    <MarketingCTAButton intent="secondary" to="/docs" label="View documentation" />
                    <MarketingCTAButton intent="secondary" to="/contact" label="Talk to sales" />
                  </CTACluster>
                }
              />
              <MarketingComparisonGrid
                columns={3}
                stackAt="md"
                gap="tight"
                items={paidCards}
                renderCard={(item, index) => (
                  <motion.div
                    key={item.id ?? index}
                    initial={{ opacity: 0, y: 24 }}
                    whileInView={{ opacity: 1, y: 0 }}
                    viewport={{ once: true }}
                    transition={{ duration: 0.55, delay: item.motionDelay ?? index * 0.08 }}
                  >
                    <MarketingComparisonCard {...item} />
                  </motion.div>
                )}
              />
            </MarketingBand>
          </motion.div>
        </SectionContainer>

        <SectionContainer className="mt-6">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand surface="none">
              <HeadlineStack
                eyebrow="Need more?"
                title="Go Enterprise"
                subtitle={enterpriseTier.headline}
                align="left"
                underlineAlign="start"
              />
              <PricingTierOutline
                tone="amber"
                name={enterpriseTier.name}
                price={enterpriseTier.price}
                description={enterpriseTier.description}
                actions={
                  <CTACluster align="start">
                    <MarketingCTAButton intent="primary" to="/contact" label="Schedule call" />
                  </CTACluster>
                }
                className="mt-8"
                sections={[
                  {
                    title: "What's included",
                    items: enterpriseTier.bullets,
                  },
                ]}
              />
            </MarketingBand>
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
            <MarketingBand surface="panel" contentClassName="marketing-band__inner--flush">
              <HeadlineStack
                eyebrow="GPU-powered features"
                title="Advanced processing across every tier"
                subtitle="FrameWorks infrastructure and the Livepeer network unlock GPU workflows. Compare what each tier includes."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
              />
              <ComparisonTable columns={gpuColumns} rows={gpuRows} tone="accent" />
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
            <MarketingBand surface="panel" contentClassName="marketing-band__inner--flush">
              <HeadlineStack
                eyebrow="Hybrid model"
                title="Build your own network, or use ours"
                subtitle="Mix self-hosted infrastructure with FrameWorks hosted services and Livepeer-backed GPU capacity."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={<MarketingCTAButton intent="primary" to="/contact" label="Talk to our team" />}
              />
              <MarketingStack gap="none" className="deployment-stack">
                <MarketingFeatureWall
                  items={deploymentOptions}
                  columns={4}
                  flush
                  variant="grid"
                  className="marketing-feature-grid--deployment"
                  renderItem={(option, index) => {
                    const Icon = option.icon
                    const isActive = activeOption?.id === option.id
                    return (
                      <motion.button
                        key={option.id ?? index}
                        type="button"
                        className={cn('deployment-option', isActive && 'deployment-option--active')}
                        onClick={() => setActiveOption(option)}
                        aria-pressed={isActive}
                        initial={{ opacity: 0, y: 12 }}
                        whileInView={{ opacity: 1, y: 0 }}
                        viewport={{ once: true }}
                        transition={{ duration: 0.35, delay: index * 0.05 }}
                      >
                        <MarketingFeatureCard
                          icon={Icon}
                          iconTone={option.tone}
                          tone={option.tone}
                          title={option.title}
                          subtitle={option.tagline}
                          hover="none"
                          flush
                          meta={<span className="deployment-option__indicator" aria-hidden="true">+</span>}
                          metaAlign="end"
                          className="deployment-option__card"
                        >
                          <p className="deployment-option__quote">{option.summary}</p>
                        </MarketingFeatureCard>
                      </motion.button>
                    )
                  }}
                />
                <div className="deployment-calculator">
                  <SavingsCalculator variant="compact" />
                </div>
              </MarketingStack>
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
            <MarketingSlab variant="feature-panel">
              <MarketingSlabHeader
                eyebrow="FAQ"
                title="Frequently asked questions"
                subtitle="Everything you need to know about FrameWorks pricing before you launch."
              />
              <Accordion type="single" collapsible>
                {faqs.map((faq, index) => (
                  <AccordionItem key={faq.question} value={`faq-${index}`}>
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
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="px-0 bg-brand-surface-strong">
        <motion.div
          initial={{ opacity: 0, y: 32 }}
          whileInView={{ opacity: 1, y: 0 }}
          viewport={{ once: true }}
          transition={{ duration: 0.6 }}
        >
          <MarketingFinalCTA
            eyebrow="Next steps"
            title="Ready to start building?"
            description="Launch the FrameWorks stack on your own hardware, or partner with us for managed deployments and GPU capacity."
            variant="band"
            primaryAction={{
              label: 'Start Free',
              href: config.appUrl,
              icon: ArrowTopRightOnSquareIcon,
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

      <MarketingScrollProgress />

      {activeOption ? (
        <div className="pricing-architecture-modal" role="dialog" aria-modal="true" aria-label={`${activeOption.title} details`}>
          <div className="pricing-architecture-modal__backdrop" onClick={() => setActiveOption(null)} />
          <div className="pricing-architecture-modal__panel">
            <button
              type="button"
              className="pricing-architecture-modal__close"
              onClick={() => setActiveOption(null)}
              aria-label="Close deployment option details"
            >
              ×
            </button>
            <div className="pricing-architecture-modal__header">
              <MarketingIconBadge tone={activeOption.tone} variant="neutral" className="pricing-architecture-modal__icon">
                {(() => {
                  const Icon = activeOption.icon
                  return <Icon className="pricing-architecture-modal__icon-symbol" />
                })()}
              </MarketingIconBadge>
              <div className="pricing-architecture-modal__meta">
                <h3>{activeOption.title}</h3>
                {activeOption.tagline ? <p>{activeOption.tagline}</p> : null}
              </div>
            </div>
            {activeOption.modal?.description ? (
              <p className="pricing-architecture-modal__description">{activeOption.modal.description}</p>
            ) : null}
            {activeOption.modal?.bullets?.length ? (
              <ul className="pricing-architecture-modal__list">
                {activeOption.modal.bullets.map((item) => (
                  <li key={`${activeOption.id}-detail-${item}`}>{item}</li>
                ))}
              </ul>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  )
}

export default Pricing
