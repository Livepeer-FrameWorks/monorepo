import { Section, SectionContainer } from '@/components/ui/section'
import config from '../../config'
import { MarketingHero, MarketingSlab, MarketingSlabHeader, MarketingScrollProgress, SectionDivider } from '@/components/marketing'

const privacySections = [
  {
    title: 'Information we collect',
    items: [
      'Account details you provide such as name, email address, company, and authentication metadata.',
      'Operational telemetry—usage logs, health metrics, and error diagnostics—to keep the platform reliable.',
      'Support communications and feedback you submit through tickets, email, or chat.'
    ]
  },
  {
    title: 'How we use it',
    items: [
      'To operate and secure FrameWorks, troubleshoot issues, and improve performance.',
      'To send product or policy updates you opt into during beta.',
      'To enforce our terms and acceptable use policy.'
    ]
  },
  {
    title: 'How we share it',
    items: [
      'We do not sell personal data.',
      'We may share it with trusted vendors who help us run FrameWorks (for example hosting, analytics, email) under confidentiality agreements.',
      'We may disclose it if required by law or to protect the safety and rights of our users.'
    ]
  },
  {
    title: 'Retention & control',
    items: [
      'We keep data only as long as necessary for the purposes above or to comply with legal requirements.',
      `You can request access or deletion by emailing ${config.contactEmail ?? 'privacy@frameworks.network'}. Deleting certain data may disable your account.`,
      'We will update this placeholder policy as the product evolves; continuing to use FrameWorks means you accept the latest version.'
    ]
  }
]

const privacyHeroAccents = [
  {
    kind: 'beam',
    x: 22,
    y: 38,
    width: 'clamp(20rem, 40vw, 32rem)',
    height: 'clamp(16rem, 28vw, 24rem)',
    rotate: -18,
    fill: 'linear-gradient(140deg, rgba(100, 140, 220, 0.32), rgba(26, 34, 60, 0.22))',
    opacity: 0.52,
    radius: '46px',
  },
  {
    kind: 'beam',
    x: 72,
    y: 32,
    width: 'clamp(18rem, 34vw, 28rem)',
    height: 'clamp(14rem, 24vw, 20rem)',
    rotate: 14,
    fill: 'linear-gradient(155deg, rgba(60, 180, 240, 0.28), rgba(20, 26, 44, 0.18))',
    opacity: 0.48,
    radius: '40px',
  },
]

const PrivacyPage = () => (
  <div className="pt-16">
    <MarketingHero
      seed="/privacy"
      eyebrow="Legal"
      title="Privacy Policy"
      description="This placeholder covers how we handle data during the FrameWorks public beta while we finalize the production policy."
      align="center"
      accents={privacyHeroAccents}
    />

    <SectionDivider />

    <Section className="bg-brand-surface">
      <SectionContainer className="max-w-3xl space-y-12">
        {privacySections.map((section) => (
          <div key={section.title} className="space-y-4">
            <h2 className="text-xl font-semibold text-foreground">{section.title}</h2>
            <ul className="space-y-3 text-sm leading-relaxed text-muted-foreground">
              {section.items.map((item) => (
                <li key={item} className="flex items-start gap-3">
                  <span className="mt-[6px] h-2 w-2 rounded-full bg-accent flex-shrink-0" />
                  <span>{item}</span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </SectionContainer>
    </Section>

    <MarketingScrollProgress />
  </div>
)

export default PrivacyPage
