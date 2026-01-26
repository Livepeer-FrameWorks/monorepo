import { motion } from 'framer-motion'
import { useState, useEffect, useRef } from 'react'
import { Turnstile } from '@marsidev/react-turnstile'
import config from '../../config'
import {
  EnvelopeIcon,
  ChatBubbleLeftRightIcon,
  ChatBubbleOvalLeftEllipsisIcon,
  BugAntIcon,
  CpuChipIcon,
  UserIcon,
  CheckCircleIcon,
  ExclamationCircleIcon,
  ArrowTopRightOnSquareIcon,
} from '@heroicons/react/24/outline'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { Alert, AlertTitle, AlertDescription } from '@/components/ui/alert'
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from '@/components/ui/accordion'
import { Section, SectionContainer } from '@/components/ui/section'
import { cn } from '@/lib/utils'
import {
  MarketingHero,
  MarketingBand,
  MarketingFeatureWall,
  MarketingSlab,
  MarketingSlabHeader,
  HeadlineStack,
  MarketingCTAButton,
  MarketingFeatureCard,
  MarketingFinalCTA,
  MarketingScrollProgress,
  SectionDivider,
} from '@/components/marketing'

const Contact = () => {
  const isTurnstileEnabled = Boolean(config.turnstileSiteKey)
  const defaultHumanCheck = isTurnstileEnabled ? 'human' : 'robot'

  const [formData, setFormData] = useState({
    name: '',
    email: '',
    company: '',
    message: '',
    phone_number: '',
    human_check: defaultHumanCheck,
  })

  const [loading, setLoading] = useState(false)
  const [success, setSuccess] = useState(false)
  const [error, setError] = useState('')
  const [turnstileToken, setTurnstileToken] = useState('')

  const [behavior, setBehavior] = useState({
    mouse: false,
    typed: false,
    formShownAt: Date.now(),
    submittedAt: null,
  })

  const formRef = useRef(null)

  useEffect(() => {
    const handleMouseMove = () => {
      setBehavior((prev) => ({ ...prev, mouse: true }))
    }

    const handleKeyDown = () => {
      setBehavior((prev) => ({ ...prev, typed: true }))
    }

    document.addEventListener('mousemove', handleMouseMove, { once: true })
    document.addEventListener('keydown', handleKeyDown, { once: true })

    return () => {
      document.removeEventListener('mousemove', handleMouseMove)
      document.removeEventListener('keydown', handleKeyDown)
    }
  }, [])

  const handleChange = (e) => {
    setFormData({
      ...formData,
      [e.target.name]: e.target.value,
    })
  }

  const contactMethods = [
    {
      icon: EnvelopeIcon,
      tone: 'accent',
      title: 'Email',
      subtitle: config.contactEmail,
      description: 'Get in touch via email.',
      link: { label: config.contactEmail, href: `mailto:${config.contactEmail}`, external: true },
    },
    {
      icon: ChatBubbleLeftRightIcon,
      tone: 'purple',
      title: 'Discord Community',
      subtitle: 'discord.gg/9J6haUjdAq',
      description: 'Join our Discord for ultra low latency chat.',
      link: { label: 'discord.gg/9J6haUjdAq', href: config.discordUrl, external: true },
    },
    {
      icon: ChatBubbleOvalLeftEllipsisIcon,
      tone: 'green',
      title: 'Forum',
      subtitle: 'forum.frameworks.network',
      description: 'Structured discussions with the FrameWorks team.',
      link: { label: 'forum.frameworks.network', href: config.forumUrl, external: true },
    },
    {
      icon: BugAntIcon,
      tone: 'yellow',
      title: 'GitHub Issues',
      subtitle: 'Open an issue',
      description: 'Report bugs or request new features.',
      link: { label: 'Open an issue', href: `${config.githubUrl}/issues`, external: true },
    },
  ]

  const faqs = [
    {
      question: 'What is "Sovereign SaaS"?',
      answer:
        'Sovereign SaaS means you can run FrameWorks on your own infrastructure, our infrastructure, or both—without vendor lock-in. Unlike cloud-only platforms or self-hosted-only products, FrameWorks gives you deployment flexibility with native multi-tenancy.',
    },
    {
      question: 'How do I get started with FrameWorks?',
      answer:
        'Deploy the full stack yourself using the Docker Compose setup, or use our hosted service and add your own edge nodes for a hybrid approach. Both paths get you streaming in minutes.',
    },
    {
      question: 'Do you offer commercial support?',
      answer:
        'Yes. We provide enterprise support with SLAs, custom development, managed hosting, and priority GPU access. Reach out to discuss enterprise pricing and requirements.',
    },
    {
      question: 'Can I run FrameWorks entirely on my own infrastructure?',
      answer:
        'Yes. Every component—control plane, analytics (ClickHouse), event streaming (Kafka), edge nodes (MistServer), and mesh networking (WireGuard)—can run on your servers. No external cloud dependencies required.',
    },
    {
      question: 'Can I contribute to FrameWorks?',
      answer:
        'Absolutely. FrameWorks is public domain - use, modify, or distribute it without restrictions. Contributions are welcome via GitHub, Discord, or the forum.',
    },
    {
      question: 'How do AI agents access FrameWorks?',
      answer:
        'Agents authenticate via wallet signature or API token, then use the MCP server or GraphQL API. Usage is charged to your account balance automatically.',
    },
    {
      question: 'What is pay-as-you-go billing?',
      answer:
        'Add funds to your account via card or crypto. Usage (storage, transcoding, delivered minutes) is deducted automatically — no invoices, no monthly commitment. Top up again when your balance runs low.',
    },
    {
      question: 'Can I use FrameWorks without an email account?',
      answer:
        'Yes. Connect an Ethereum wallet to authenticate — your wallet address is your identity. You can optionally add an email later for notifications.',
    },
  ]

  const handleSubmit = async (e) => {
    e.preventDefault()
    setLoading(true)
    setError('')

    try {
      if (isTurnstileEnabled && !turnstileToken) {
        setError('Please complete the verification challenge before submitting.')
        setLoading(false)
        return
      }

      const submissionData = {
        ...formData,
        behavior: {
          ...behavior,
          submittedAt: Date.now(),
        },
        turnstileToken: isTurnstileEnabled ? turnstileToken : undefined,
      }

      const apiUrl = `${config.contactApiUrl}/api/contact`

      const response = await fetch(apiUrl, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify(submissionData),
      })

      const result = await response.json()

      if (result.success) {
        setSuccess(true)
        setFormData({
          name: '',
          email: '',
          company: '',
          message: '',
          phone_number: '',
          human_check: defaultHumanCheck,
        })
        setBehavior({
          mouse: false,
          typed: false,
          formShownAt: Date.now(),
          submittedAt: null,
        })
        if (isTurnstileEnabled) {
          setTurnstileToken('')
        }
      } else {
        setError(result.error || 'Failed to send message.')
        if (result.details && process.env.NODE_ENV === 'development') {
          console.log('Validation errors:', result.details)
        }
        if (isTurnstileEnabled) {
          setTurnstileToken('')
        }
      }
    } catch (err) {
      setError('Network error. Please try again.')
      console.error('Contact form error:', err)
      if (isTurnstileEnabled) {
        setTurnstileToken('')
      }
    } finally {
      setLoading(false)
    }
  }

  const contactHeroAccents = [
    {
      kind: 'beam',
      x: 14,
      y: 28,
      width: 'clamp(28rem, 42vw, 36rem)',
      height: 'clamp(18rem, 30vw, 26rem)',
      rotate: -18,
      fill: 'linear-gradient(140deg, rgba(125, 207, 255, 0.28), rgba(16, 22, 38, 0.2))',
      opacity: 0.52,
      radius: '48px',
    },
    {
      kind: 'spot',
      x: 66,
      y: 18,
      width: 'clamp(24rem, 36vw, 30rem)',
      height: 'clamp(22rem, 38vw, 30rem)',
      fill: 'radial-gradient(circle, rgba(88, 150, 255, 0.24) 0%, transparent 68%)',
      opacity: 0.4,
      blur: '110px',
    },
    {
      kind: 'beam',
      x: 78,
      y: 74,
      width: 'clamp(20rem, 34vw, 28rem)',
      height: 'clamp(18rem, 30vw, 24rem)',
      rotate: 18,
      fill: 'linear-gradient(150deg, rgba(132, 196, 255, 0.22), rgba(18, 24, 42, 0.22))',
      opacity: 0.36,
      radius: '44px',
    },
  ]

  return (
    <div className="pt-16">
      <MarketingHero
        seed="/contact"
        title="Contact us"
        description="Have questions about FrameWorks? We would love to hear from you."
        align="left"
        surface="gradient"
        support="Email • Discord • Forum • Enterprise requests"
        accents={contactHeroAccents}
        primaryAction={{
          label: `Email`,
          href: `mailto:${config.contactEmail}`,
          external: true,
          icon: ArrowTopRightOnSquareIcon,
        }}
        secondaryAction={{
          label: 'Join the Discord',
          href: config.discordUrl,
          external: true,
          icon: ArrowTopRightOnSquareIcon,
          variant: 'secondary',
        }}
      />

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <MarketingBand surface="none">
            <HeadlineStack
              eyebrow="Get in touch"
              title="Choose the channel that fits you"
              subtitle="Email for longer threads, Discord for real-time chat, or the forum for structured conversations with the team."
              align="left"
            />
            <MarketingFeatureWall
              items={contactMethods}
              columns={4}
              stackAt="md"
              flush
              renderItem={(method, index) => {
                const Icon = method.icon
                const content = (
                  <MarketingFeatureCard
                    tone={method.tone}
                    icon={Icon}
                    iconTone={method.tone}
                    title={method.title}
                    hover="lift"
                    flush
                    metaAlign="end"
                    className="contact-method-card"
                    meta={<ArrowTopRightOnSquareIcon className="contact-method-chevron" aria-hidden="true" />}
                  >
                    <div className="contact-method-subtitle">
                      {method.subtitle ?? method.link?.label}
                    </div>
                    <p className="marketing-feature-card__description">{method.description}</p>
                  </MarketingFeatureCard>
                )

                if (method.link?.href) {
                  return (
                    <a
                      key={method.title ?? index}
                      href={method.link.href}
                      target={method.link.external ? '_blank' : undefined}
                      rel={method.link.external ? 'noreferrer noopener' : undefined}
                      className="contact-method-link"
                    >
                      {content}
                    </a>
                  )
                }

                return content
              }}
            />
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <MarketingSlab className="contact-form-slab">
            <MarketingSlabHeader
              eyebrow="Contact"
              title="Send us a message"
              subtitle="We usually reply within one business day. Let us know how we can help."
            />
            <form ref={formRef} onSubmit={handleSubmit} className="contact-form space-y-6">
                {!isTurnstileEnabled && (
                  <input
                    type="text"
                    name="phone_number"
                    value={formData.phone_number}
                    onChange={handleChange}
                    style={{ display: 'none' }}
                    tabIndex="-1"
                    autoComplete="off"
                  />
                )}

                <div className="grid gap-6 md:grid-cols-2">
                  <div className="space-y-2">
                    <Label htmlFor="name">Name *</Label>
                    <Input
                      type="text"
                      id="name"
                      name="name"
                      value={formData.name}
                      onChange={handleChange}
                      required
                      disabled={success}
                      placeholder="Your name"
                    />
                  </div>
                  <div className="space-y-2">
                    <Label htmlFor="email">Email *</Label>
                    <Input
                      type="email"
                      id="email"
                      name="email"
                      value={formData.email}
                      onChange={handleChange}
                      required
                      disabled={success}
                      placeholder="you@example.com"
                    />
                  </div>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="company">Company</Label>
                  <Input
                    type="text"
                    id="company"
                    name="company"
                    value={formData.company}
                    onChange={handleChange}
                    disabled={success}
                    placeholder="Your company"
                  />
                </div>

                <div className="space-y-2">
                  <Label htmlFor="message">Message *</Label>
                  <Textarea
                    id="message"
                    name="message"
                    value={formData.message}
                    onChange={handleChange}
                    required
                    disabled={success}
                    placeholder="Tell us about your project or questions..."
                    rows={6}
                  />
                </div>

                {isTurnstileEnabled && !success ? (
                  <div className="contact-turnstile">
                    <span className="contact-turnstile__label">Security Check *</span>
                    <div className="contact-turnstile__widget">
                      <Turnstile
                        siteKey={config.turnstileSiteKey}
                        onSuccess={(token) => {
                          setTurnstileToken(token)
                          setFormData((prev) => ({ ...prev, human_check: 'human' }))
                          setError('')
                        }}
                        onExpire={() => {
                          setTurnstileToken('')
                          setFormData((prev) => ({ ...prev, human_check: defaultHumanCheck }))
                        }}
                        onError={(err) => {
                          console.error('Turnstile error:', err)
                          setTurnstileToken('')
                          setFormData((prev) => ({ ...prev, human_check: defaultHumanCheck }))
                          setError('There was a problem with the verification challenge. Please try again.')
                        }}
                        options={{ action: 'contact_form', theme: 'dark' }}
                      />
                    </div>
                  </div>
                ) : null}

                {!isTurnstileEnabled && (
                  <div className="contact-verification">
                    <span className="contact-turnstile__label">Security Check *</span>
                    <div className="contact-verification__options">
                      <label
                        className={cn(
                          'contact-verification__option contact-verification__option--robot',
                          success && 'is-disabled'
                        )}
                      >
                        <input
                          type="radio"
                          name="human_check"
                          value="robot"
                          checked={formData.human_check === 'robot'}
                          onChange={handleChange}
                          disabled={success}
                          className="contact-verification__radio"
                        />
                        <CpuChipIcon className="contact-verification__icon" />
                        <span className="contact-verification__copy">I am a robot – discard this message.</span>
                      </label>
                      <label
                        className={cn(
                          'contact-verification__option contact-verification__option--human',
                          success && 'is-disabled'
                        )}
                      >
                        <input
                          type="radio"
                          name="human_check"
                          value="human"
                          checked={formData.human_check === 'human'}
                          onChange={handleChange}
                          disabled={success}
                          className="contact-verification__radio"
                        />
                        <UserIcon className="contact-verification__icon" />
                        <span className="contact-verification__copy">I am human – please send this.</span>
                      </label>
                    </div>
                  </div>
                )}

                {success ? (
                  <motion.div
                    initial={{ opacity: 0, y: 20 }}
                    animate={{ opacity: 1, y: 0 }}
                  >
                    <Alert className="contact-alert contact-alert--success">
                      <CheckCircleIcon className="h-5 w-5" />
                      <AlertTitle>Message sent successfully</AlertTitle>
                      <AlertDescription>
                        Thank you for reaching out. We will respond shortly.
                      </AlertDescription>
                    </Alert>
                  </motion.div>
                ) : (
                  <MarketingCTAButton
                    intent="primary"
                    type="submit"
                    disabled={loading || (isTurnstileEnabled && !turnstileToken)}
                    className="w-full justify-center"
                  >
                    {loading ? (
                      <span className="flex items-center justify-center gap-2">
                        <span className="h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent" />
                        Sending...
                      </span>
                    ) : (
                      'Send Message'
                    )}
                  </MarketingCTAButton>
                )}

                {error && (
                  <motion.div
                    initial={{ opacity: 0, y: 20 }}
                    animate={{ opacity: 1, y: 0 }}
                  >
                    <Alert className="contact-alert contact-alert--error">
                      <ExclamationCircleIcon className="h-5 w-5" />
                      <AlertTitle>Error</AlertTitle>
                      <AlertDescription>{error}</AlertDescription>
                    </Alert>
                  </motion.div>
                )}
              </form>
          </MarketingSlab>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <MarketingSlab variant="feature-panel">
            <MarketingSlabHeader
              eyebrow="FAQ"
              title="Common questions"
              subtitle="Quick answers to the questions we hear most often."
            />
            <Accordion type="single" collapsible className="marketing-accordion">
              {faqs.map((faq, index) => (
                <AccordionItem key={faq.question} value={`faq-${index}`}>
                  <AccordionTrigger>
                    {faq.question}
                  </AccordionTrigger>
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

      <Section className="px-0">
        <MarketingFinalCTA
          variant="band"
          eyebrow="Next steps"
          title="Partner with FrameWorks"
          description="Tell us what you are building and we will map the next steps together."
          primaryAction={{
            label: 'Start Free',
            href: config.appUrl,
            external: true,
          }}
          secondaryAction={[
            {
              label: 'Join the Discord',
              href: config.discordUrl,
              icon: 'auto',
              external: true,
            },
            {
              label: 'Browse Docs',
              href: `${config.appUrl.replace(/\/+$/, '')}/developer/api`,
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

export default Contact
