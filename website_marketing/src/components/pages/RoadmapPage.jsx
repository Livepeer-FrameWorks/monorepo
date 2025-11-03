import { useState, useEffect } from 'react'
import BetaBadge from '../shared/BetaBadge'
import MarkdownView from '../shared/MarkdownView'
import config from '../../config'
import { MarketingHero, MarketingBand, MarketingSlab, MarketingFinalCTA, MarketingScrollProgress, SectionDivider } from '@/components/marketing'
import { Skeleton } from '@/components/ui/skeleton'
import { Section, SectionContainer } from '@/components/ui/section'

const roadmapHeroAccents = [
  {
    kind: 'beam',
    x: 14,
    y: 38,
    width: 'clamp(26rem, 48vw, 38rem)',
    height: 'clamp(20rem, 34vw, 28rem)',
    rotate: -17,
    fill: 'linear-gradient(142deg, rgba(102, 148, 220, 0.34), rgba(25, 33, 58, 0.22))',
    opacity: 0.55,
    radius: '49px',
  },
  {
    kind: 'beam',
    x: 80,
    y: 30,
    width: 'clamp(22rem, 40vw, 32rem)',
    height: 'clamp(18rem, 30vw, 24rem)',
    rotate: 18,
    fill: 'linear-gradient(154deg, rgba(66, 192, 246, 0.3), rgba(20, 26, 45, 0.2))',
    opacity: 0.51,
    radius: '45px',
  },
]

const RoadmapPage = () => {
  const [roadmapMd, setRoadmapMd] = useState('')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch('https://raw.githubusercontent.com/Livepeer-FrameWorks/monorepo/refs/heads/master/docs/ROADMAP.md')
      .then(res => res.text())
      .then(text => {
        setRoadmapMd(text)
        setLoading(false)
      })
      .catch(err => {
        console.error('Failed to load roadmap:', err)
        setRoadmapMd('# Roadmap\n\nFailed to load roadmap content.')
        setLoading(false)
      })
  }, [])
  return (
    <div className="pt-16">
      <MarketingHero
        seed="/roadmap"
        title="Roadmap"
        description="High-level roadmap from our repository docs."
        align="center"
        surface="gradient"
        support="Public beta • Active development • Community-driven"
        accents={roadmapHeroAccents}
      >
        <div className="flex flex-wrap items-center justify-center gap-2 text-xs text-muted-foreground">
          <BetaBadge />
          <span>Public Beta — features marked accordingly</span>
        </div>
      </MarketingHero>

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer className="max-w-4xl">
          {loading ? (
            <div className="space-y-4">
              <Skeleton className="h-8 w-3/4" />
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-5/6" />
              <Skeleton className="h-6 w-2/3 mt-6" />
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-full" />
              <Skeleton className="h-4 w-4/5" />
            </div>
          ) : (
            <MarkdownView markdown={roadmapMd} />
          )}
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="px-0">
        <MarketingFinalCTA
          variant="band"
          eyebrow="Next steps"
          title="Shape the future of FrameWorks"
          description="Join our community to influence the roadmap and get early access to new features."
          primaryAction={{
            label: 'Start Free',
            href: config.appUrl,
            external: true,
          }}
          secondaryAction={[
            {
              label: 'Join Discord',
              href: config.discordUrl,
              icon: 'auto',
              external: true,
            },
            {
              label: 'View GitHub',
              href: config.githubUrl,
              icon: 'auto',
              external: true,
            },
          ]}
        />
      </Section>

      <MarketingScrollProgress />
    </div>
  )
}

export default RoadmapPage
