import config from '../../config'
import { RocketLaunchIcon, ArrowTopRightOnSquareIcon, VideoCameraIcon, NoSymbolIcon } from '@heroicons/react/24/outline'
import { Section, SectionContainer } from '@/components/ui/section'
import {
  MarketingHero,
  MarketingBand,
  MarketingFeatureWall,
  MarketingSlab,
  HeadlineStack,
  MarketingFinalCTA,
  MarketingScrollProgress,
  SectionDivider,
  MarketingCodePanel,
  MarketingFeatureCard
} from '@/components/marketing'

const Documentation = () => {
  const sections = [
    {
      title: 'Getting Started',
      description: 'Quick setup and deployment guide.',
      items: [
        { title: 'Installation', description: 'Deploy FrameWorks with Docker Compose.' },
        { title: 'Configuration', description: 'Environment variables and base settings.' },
        { title: 'First Stream', description: 'Create and publish your first live stream.' },
      ],
    },
    {
      title: 'API Reference',
      description: 'Complete API surface area with examples.',
      items: [
        { title: 'Authentication', description: 'JWT tokens and API key management.' },
        { title: 'Streams', description: 'Create, update, and orchestrate live streams.' },
        { title: 'Analytics', description: 'Fetch viewer metrics and infrastructure insights.' },
      ],
    },
    {
      title: 'Architecture',
      description: 'System components and deployment guides.',
      items: [
        { title: 'Components', description: 'APIs, MistServer, Livepeer, YugabyteDB, ClickHouse.' },
        { title: 'Deployment', description: 'Central, regional, and edge footprints.' },
        { title: 'Scaling', description: 'Multi-region deployment patterns and tooling.' },
      ],
    },
  ]

  const highlightLinks = [
    {
      title: 'Architecture TL;DR',
      description: 'High-level overview of FrameWorks architecture and components.',
      icon: RocketLaunchIcon,
      tone: 'accent',
      link: {
        label: 'View Architecture',
        href: `${config.githubUrl}/blob/master/docs/TLDR.md`,
        external: true,
      },
    },
    {
      title: 'Roadmap',
      description: 'Upcoming features and current status by area.',
      icon: VideoCameraIcon,
      tone: 'purple',
      link: {
        label: 'See Roadmap',
        href: `${config.githubUrl}/blob/master/docs/ROADMAP.md`,
        external: true,
      },
    },
  ]

  const apiSnippet = [
    "// Create a new stream",
    "const response = await fetch('http://localhost:18090/api/streams', {",
    "  method: 'POST',",
    "  headers: {",
    "    'Content-Type': 'application/json',",
    "    'Authorization': 'Bearer YOUR_TOKEN'",
    "  },",
    "  body: JSON.stringify({",
    "    title: 'My Live Stream',",
    "    description: 'Live streaming with FrameWorks'",
    "  })",
    "});",
    'const stream = await response.json();',
    "console.log('Ingest URL:', stream.ingest_url);",
    "console.log('Playback URL:', stream.playback_url);",
  ]

  const docsHeroAccents = [
    {
      kind: 'beam',
      x: 12,
      y: 32,
      width: 'clamp(28rem, 42vw, 36rem)',
      height: 'clamp(18rem, 30vw, 26rem)',
      rotate: -18,
      fill: 'linear-gradient(145deg, rgba(125, 207, 255, 0.32), rgba(18, 24, 42, 0.18))',
      opacity: 0.58,
      radius: '48px',
    },
    {
      kind: 'spot',
      x: 68,
      y: 24,
      width: 'clamp(24rem, 38vw, 32rem)',
      height: 'clamp(20rem, 34vw, 28rem)',
      fill: 'radial-gradient(circle, rgba(93, 196, 240, 0.26) 0%, transparent 70%)',
      opacity: 0.4,
      blur: '110px',
    },
    {
      kind: 'beam',
      x: 74,
      y: 68,
      width: 'clamp(22rem, 36vw, 30rem)',
      height: 'clamp(18rem, 32vw, 26rem)',
      rotate: 16,
      fill: 'linear-gradient(140deg, rgba(138, 180, 248, 0.24), rgba(20, 26, 44, 0.2))',
      opacity: 0.36,
      radius: '44px',
    },
  ]

  return (
    <div className="pt-16">
      <MarketingHero
        title="Documentation"
        description="Browse repo docs, use the in-app API Explorer, and ship production-ready pipelines with the quick CLI start below."
        align="left"
        surface="gradient"
        support="Repo docs • API explorer • Self-hosting guides"
        accents={docsHeroAccents}
        primaryAction={{
          label: 'Browse Repo Docs',
          href: `${config.githubUrl}/tree/master/docs`,
          icon: ArrowTopRightOnSquareIcon,
          external: true,
        }}
        secondaryAction={{
          label: 'Open API Explorer',
          href: `${config.appUrl.replace(/\/+$/, '')}/developer/api`,
          icon: ArrowTopRightOnSquareIcon,
          external: true,
          variant: 'secondary',
        }}
        seed="/docs"
      />

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <MarketingBand surface="none">
            <HeadlineStack
              title="Highlights"
              subtitle="Start with the architecture snapshot or see which features are shipping next."
              align="left"
            />
            <MarketingFeatureWall
              items={highlightLinks}
              columns={2}
              stackAt="md"
              flush
              renderItem={(item, index) => {
                const Icon = item.icon
                const body = (
                  <MarketingFeatureCard
                    tone={item.tone}
                    icon={Icon}
                    iconTone={item.tone}
                    title={item.title}
                    description={item.description}
                    hover="lift"
                    flush
                    metaAlign="end"
                    meta={
                      <span className="docs-highlight-meta">
                        {item.link.label}
                        <ArrowTopRightOnSquareIcon className="h-4 w-4" aria-hidden="true" />
                      </span>
                    }
                  />
                )

                if (item.link?.href) {
                  return (
                    <a
                      key={item.title ?? index}
                      href={item.link.href}
                      target={item.link.external ? '_blank' : undefined}
                      rel={item.link.external ? 'noreferrer noopener' : undefined}
                      className="docs-highlight-link"
                    >
                      {body}
                    </a>
                  )
                }

                return body
              }}
            />
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <HeadlineStack
            eyebrow="Overview"
            title="Documentation at a glance"
            subtitle="Key areas to help you install, integrate, and scale FrameWorks."
            align="left"
          />
          <MarketingFeatureWall
            stripe={false}
            items={sections.map((section, index) => ({
              title: section.title,
              description: section.description,
              hover: 'none',
              className: 'docs-overview-card docs-overview-card--disabled',
              meta: (
                <span className="docs-overview-status">
                  <NoSymbolIcon className="h-4 w-4" aria-hidden="true" />
                  In progress
                </span>
              ),
              children: (
                <ul className="docs-overview-list">
                  {section.items.map((item) => (
                    <li key={item.title} className="docs-overview-list__item">
                      <span className="docs-overview-list__title">{item.title}</span>
                      <span className="docs-overview-list__description">{item.description}</span>
                    </li>
                  ))}
                </ul>
              ),
            }))}
            columns={3}
            stackAt="md"
          />
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <MarketingSlab>
            <HeadlineStack
              eyebrow="Quick start"
              title="Get FrameWorks running in under five minutes"
              subtitle="Use the CLI or Docker Compose to boot the control plane locally and generate API tokens for your first stream."
              align="left"
            />
            <MarketingCodePanel
              badge="Node.js"
              badgeTone="accent"
              language="JavaScript"
              code={apiSnippet.join('\n')}
            />
          </MarketingSlab>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="px-0">
        <MarketingFinalCTA
          variant="band"
          eyebrow="Next steps"
          title="Ship dependable video infrastructure"
          description="From capture to delivery, we architect, deploy, and support media pipelines that stay online."
          primaryAction={{
            label: 'View Developer Docs',
            href: `${config.appUrl.replace(/\/+$/, '')}/developer/api`,
            icon: 'auto',
            external: true,
          }}
          secondaryAction={[
            {
              label: 'Clone the repo',
              href: config.githubUrl,
              icon: 'auto',
              external: true,
            },
            {
              label: 'Talk to our team',
              to: '/contact',
            },
          ]}
        />
      </Section>

      <MarketingScrollProgress />
    </div>
  )
}

export default Documentation
