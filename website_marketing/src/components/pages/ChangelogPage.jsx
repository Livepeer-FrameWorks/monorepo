import { useState, useEffect } from 'react'
import MarkdownView from '../shared/MarkdownView'
import config from '../../config'
import { MarketingHero, MarketingFinalCTA, MarketingScrollProgress, SectionDivider } from '@/components/marketing'
import { Skeleton } from '@/components/ui/skeleton'
import { Section, SectionContainer } from '@/components/ui/section'

const changelogHeroAccents = [
  {
    kind: 'beam',
    x: 16,
    y: 36,
    width: 'clamp(25rem, 46vw, 37rem)',
    height: 'clamp(19rem, 33vw, 27rem)',
    rotate: -20,
    fill: 'linear-gradient(136deg, rgba(96, 142, 216, 0.35), rgba(23, 31, 57, 0.23))',
    opacity: 0.56,
    radius: '50px',
  },
  {
    kind: 'beam',
    x: 82,
    y: 32,
    width: 'clamp(21rem, 39vw, 31rem)',
    height: 'clamp(17rem, 29vw, 25rem)',
    rotate: 19,
    fill: 'linear-gradient(151deg, rgba(68, 194, 248, 0.31), rgba(21, 27, 46, 0.21))',
    opacity: 0.52,
    radius: '46px',
  },
]

const ChangelogPage = () => {
  const [changelogMd, setChangelogMd] = useState('')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    fetch('https://raw.githubusercontent.com/Livepeer-FrameWorks/monorepo/refs/heads/master/docs/CHANGELOG.md')
      .then(res => res.text())
      .then(text => {
        setChangelogMd(text)
        setLoading(false)
      })
      .catch(err => {
        console.error('Failed to load changelog:', err)
        setChangelogMd('# Changelog\n\nFailed to load changelog content.')
        setLoading(false)
      })
  }, [])

  return (
    <div className="pt-16">
      <MarketingHero
        seed="/changelog"
        title="Changelog"
        description="Recent updates and releases"
        align="center"
        surface="gradient"
        support="Release notes • Feature updates • Bug fixes"
        accents={changelogHeroAccents}
      />

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
            <MarkdownView markdown={changelogMd} />
          )}
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="px-0">
        <MarketingFinalCTA
          variant="band"
          eyebrow="Next steps"
          title="Start building with FrameWorks"
          description="Deploy your own streaming infrastructure or partner with us for managed deployments and support."
          primaryAction={{
            label: 'Start Free',
            href: config.appUrl,
            external: true,
          }}
          secondaryAction={[
            {
              label: 'View Documentation',
              to: '/docs',
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

export default ChangelogPage
