import { Section, SectionContainer } from '@/components/ui/section'
import config from '../../config'
import { MarketingHero, MarketingScrollProgress, SectionDivider } from '@/components/marketing'

const termsSections = [
  {
    title: '1. Beta Service',
    paragraphs: [
      'FrameWorks is currently in public beta. Features, performance, and availability may change without notice while we prepare the platform for general availability.',
      'We may suspend, limit, or discontinue the beta at any time. Continuing to use FrameWorks means you accept the most recent version of these terms.'
    ]
  },
  {
    title: '2. Accounts & Security',
    paragraphs: [
      'You are responsible for all activity under your account, including keeping your credentials secure and contact details up to date.',
      'Please notify us promptly if you suspect unauthorized access or a security incident involving your account or infrastructure.'
    ]
  },
  {
    title: '3. Acceptable Use',
    paragraphs: [
      'You must comply with all applicable laws, respect third-party rights, and follow the Acceptable Use Policy. Prohibited behaviour includes distributing illegal content, attempting to disrupt FrameWorks services, or interfering with other users.',
      'Violations may result in suspension or termination without refund.'
    ]
  },
  {
    title: '4. No Warranties',
    paragraphs: [
      'The beta is provided “as is” and “as available.” We disclaim all warranties, express or implied, including fitness for a particular purpose, merchantability, and non-infringement.',
      'Any liability we have to you is limited to the maximum extent permitted by law.'
    ]
  },
  {
    title: '5. Changes & Contact',
    paragraphs: [
      'We may update these terms as FrameWorks evolves. Using the service after updates are published means you agree to the revised version.',
      `Questions about these terms? Reach out to us at ${config.contactEmail ?? 'legal@frameworks.network'}.`
    ]
  }
]

const termsHeroAccents = [
  {
    kind: 'beam',
    x: 16,
    y: 42,
    width: 'clamp(22rem, 42vw, 34rem)',
    height: 'clamp(18rem, 30vw, 26rem)',
    rotate: -20,
    fill: 'linear-gradient(135deg, rgba(90, 130, 210, 0.34), rgba(24, 32, 58, 0.24))',
    opacity: 0.54,
    radius: '48px',
  },
  {
    kind: 'beam',
    x: 78,
    y: 28,
    width: 'clamp(20rem, 38vw, 30rem)',
    height: 'clamp(16rem, 26vw, 22rem)',
    rotate: 16,
    fill: 'linear-gradient(150deg, rgba(65, 190, 245, 0.3), rgba(18, 24, 42, 0.2))',
    opacity: 0.5,
    radius: '42px',
  },
]

const TermsPage = () => (
  <div className="pt-16">
    <MarketingHero
      seed="/terms"
      eyebrow="Legal"
      title="Terms of Service"
      description="These placeholder beta terms outline how you can use FrameWorks until the production policy is finalized."
      align="center"
      accents={termsHeroAccents}
    />

    <SectionDivider />

    <Section className="bg-brand-surface">
      <SectionContainer className="max-w-3xl space-y-12">
        {termsSections.map((section) => (
          <div key={section.title} className="space-y-4">
            <h2 className="text-xl font-semibold text-foreground">{section.title}</h2>
            <div className="space-y-4 text-sm leading-relaxed text-muted-foreground">
              {section.paragraphs.map((paragraph, index) => (
                <p key={index}>{paragraph}</p>
              ))}
            </div>
          </div>
        ))}
      </SectionContainer>
    </Section>

    <MarketingScrollProgress />
  </div>
)

export default TermsPage
