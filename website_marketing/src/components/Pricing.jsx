import { motion } from 'framer-motion'
import { Link } from 'react-router-dom'
import config from '../config'
import ExternalLinkIcon from './ExternalLinkIcon'

const Pricing = () => {
  // Free tier - displayed prominently at top
  const freeTier = {
    name: "Free Tier",
    price: "Free",
    period: "",
    description: "Self-hosted features with shared pool access",
    features: [
      "All self-hosted features",
      "Shared bandwidth pool",
      "Transcoded streams via Livepeer network",
      "Community support",
      "Stream dashboard",
      "Basic analytics"
    ],
    limitations: [
      "No subdomain",
      "No hosted load balancer",
      "No AI processing or multi-stream compositing",
      "FrameWorks watermarking in player"
    ],
    cta: "Start Free",
    ctaLink: config.appUrl,
    popular: true,
    badge: "Most Popular"
  }

  // Main paid tiers - displayed in a row
  const paidTiers = [
    {
      name: "Supporter",
      price: "€50",
      period: "/month",
      dailyCost: "~€1.66 per day",
      description: "Enhanced features and processing access",
      features: [
        "Everything in Free tier",
        "~100-250 Mbps sustained bandwidth",
        "Custom subdomain (yourname.frameworks.network)",
        "Hosted load balancer",
        "Calendar integration",
        "Stream scheduling & automation",
        "Telemetry & monitoring of self-hosted instances",
        "90-day analytics retention",
        "Remove watermarking",
        "Transparent usage reporting and limits"
      ],
      limitations: [
        "Suitable for ~100-300 concurrent viewers",
        "No service level agreement"
      ],
      cta: "Get Started",
      ctaLink: config.appUrl,
      popular: false
    },
    {
      name: "Developer",
      price: "€250",
      period: "/month",
      dailyCost: "~€8.33 per day",
      description: "Enhanced capacity for development teams",
      features: [
        "Everything in Supporter tier",
        "~500 Mbps - 1 Gbps sustained bandwidth",
        "GPU allocation for AI processing and multi-stream compositing",
        "Team collaboration features",
        "Priority support",
        "Advanced analytics with materialized views",
        "180-day analytics retention",
        "Your own watermarking"
      ],
      limitations: [
        "Suitable for ~500-1,000 concurrent viewers",
        "Standard SLA"
      ],
      cta: "Get Started",
      ctaLink: config.appUrl,
      popular: false
    },
    {
      name: "Production Ready",
      price: "€1,000",
      period: "/month",
      dailyCost: "~€33.33 per day",
      description: "Reliable enterprise infrastructure with redundancy",
      features: [
        "Everything in Developer tier",
        "~2-5 Gbps sustained bandwidth",
        "Dedicated processing allocation",
        "Enterprise SLA & service contract",
        "24/7 priority support",
        "Advanced analytics with live dashboard"
      ],
      limitations: [
        "Suitable for ~2,000-5,000 concurrent viewers",
        "For consistently higher usage (>5 Gbps sustained), we'll discuss custom deployment"
      ],
      cta: "Get Started",
      ctaLink: config.appUrl,
      popular: false
    }
  ]

  // Enterprise tier - displayed separately at bottom
  const enterpriseTier = {
    name: "Enterprise",
    price: "Custom",
    period: "pricing",
    description: "Ready to be the next Netflix, Twitch, or YouTube: when you're building at massive scale",
    features: [
      "Custom feature development",
      "White-label solutions",
      "Private deployments",
      "Or: we help you run it!",
      "Unlimited bandwidth",
      "Dedicated GPU infrastructure",
      "Custom SLA",
      "Training & consulting",
      "Custom billing arrangements"
    ],
    limitations: [],
    cta: "Schedule Call",
    ctaLink: "/contact",
    popular: false
  }

  const gpuFeatures = [
    {
      icon: "🎬",
      title: "Transcoding",
      description: "Real-time video transcoding to multiple formats and bitrates",
      freeAccess: "Powered by Livepeer network",
      supporterAccess: "Powered by Livepeer network",
      developerAccess: "Powered by Livepeer network",
      productionAccess: "Dedicated processing allocation"
    },
    {
      icon: "🤖",
      title: "AI Processing",
      description: "Advanced video processing and analysis capabilities",
      freeAccess: "Not available",
      supporterAccess: "Not available",
      developerAccess: "Rate-limited access",
      productionAccess: "Dedicated allocation"
    },
    {
      icon: "🎥",
      title: "Multi-stream Compositing",
      description: "Combine multiple streams with advanced mixing and effects",
      freeAccess: "Not available",
      supporterAccess: "Not available",
      developerAccess: "Rate-limited access",
      productionAccess: "Dedicated allocation"
    }
  ]

  const faqs = [
    {
      question: "How does the shared GPU pool work?",
      answer: "The free tier gets transcoding via Livepeer network only - no AI or compositing. The Developer tier (€250+) provides rate-limited access to our shared GPU pool for AI and compositing. Production Ready gets dedicated GPU allocation. This ensures fair access while providing upgrade paths for higher performance needs."
    },
    {
      question: "What happens if I exceed my bandwidth limits?",
      answer: "We don't hard-cap your usage - instead, we'll reach out to discuss upgrading to a plan that better fits your needs. Our limits are guidelines for sustained usage, not burst traffic. A Saturday night spike won't trigger any issues, but consistently streaming to 1,000+ concurrent viewers daily means you're ready for the Developer tier."
    },
    {
      question: "Can I self-host everything and still use GPU features?",
      answer: "Yes! All tiers include full self-hosting capabilities, including the ability to run your own AI processing and compositing. Free tier includes transcoding via Livepeer network. You can run your core infrastructure anywhere while leveraging our processing power."
    },
    {
      question: "What's included in the custom subdomain?",
      answer: "Supporter tier and above get a subdomain like yourname.frameport.dev with SSL certificates and CDN. You can use this for streaming, embedding, and as your branded streaming endpoint."
    },
    {
      question: "How does the hosted load balancer work?",
      answer: "Starting with Supporter tier, we run and manage a Foghorn load balancer instance for you. This handles intelligent routing, failover, and scaling across your streaming infrastructure without you needing to manage it."
    },
    {
      question: "When should I upgrade to Production Ready?",
      answer: "Production Ready is designed for active products with steady viewers. If you're consistently streaming to 1,000+ concurrent viewers, need dedicated GPU processing, or require enterprise SLA guarantees, it's time to upgrade. This tier supports 2,000-5,000 concurrent viewers with dedicated resource allocation."
    },
    {
      question: "How does Enterprise work for massive scale?",
      answer: "Enterprise is about partnership, not limits. We've scaled customers to 100+ Gbps sustained bandwidth. The question isn't 'can we handle it?' but 'do you want us to run it for you, or do you prefer a service contract to run it yourself'. We'll work with you either way to build whatever infrastructure your platform needs."
    }
  ]

  return (
    <div className="pt-16">
      {/* Header */}
      <section className="section-padding bg-gradient-to-br from-tokyo-night-bg via-tokyo-night-bg-light to-tokyo-night-bg">
        <div className="max-w-7xl mx-auto text-center">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.6 }}
          >
            <div className="mb-6">
              <span className="inline-block px-4 py-2 bg-tokyo-night-blue/20 border border-tokyo-night-blue rounded-full text-tokyo-night-blue text-sm font-medium mb-4">
                Self-hosted + FrameWorks Hosted
              </span>
            </div>
            <h1 className="text-4xl md:text-6xl font-bold gradient-text mb-6" style={{display: 'inline-block'}}>
              <span className="transparent-word">Transparent</span> Pricing
            </h1>
            <p className="text-xl text-tokyo-night-fg-dark max-w-3xl mx-auto mb-8">
              Start free with full self-hosting capabilities. Upgrade as you grow for hosted services,
              dedicated bandwidth, and GPU processing through our infrastructure and the Livepeer network.
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <a href={config.appUrl} className="btn-primary flex items-center justify-center whitespace-nowrap">
                Start Free - No Credit Card
                <ExternalLinkIcon className="w-4 h-4 ml-2 flex-shrink-0" />
              </a>
              <Link to="/contact" className="btn-secondary">
                Contact Us
              </Link>
            </div>
          </motion.div>
        </div>
      </section>

      {/* Pricing Cards */}
      <section className="section-padding">
        <div className="max-w-7xl mx-auto">

          {/* Free Tier - Prominent Display */}
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
            className="mb-16"
          >
            <div className="text-center mb-8">
              <h2 className="text-2xl md:text-3xl font-bold gradient-text mb-2">
                Start Free Today
              </h2>
              <p className="text-tokyo-night-fg-dark">
                Full self-hosting capabilities with shared processing access
              </p>
            </div>

            <div className="max-w-2xl mx-auto">
              <div className="glow-card p-8 relative border-2 border-tokyo-night-blue">
                <div className="absolute -top-3 left-1/2 transform -translate-x-1/2">
                  <span className="bg-tokyo-night-blue text-tokyo-night-bg px-4 py-2 rounded-full text-sm font-medium">
                    {freeTier.badge}
                  </span>
                </div>

                <div className="grid md:grid-cols-2 gap-8">
                  <div>
                    <h3 className="text-2xl font-bold text-tokyo-night-fg mb-2">{freeTier.name}</h3>
                    <div className="mb-4">
                      <span className="text-4xl font-bold gradient-text">{freeTier.price}</span>
                      <span className="text-tokyo-night-comment ml-2">{freeTier.period}</span>
                    </div>
                    <p className="text-tokyo-night-fg-dark mb-6">{freeTier.description}</p>

                    <div className="flex">
                      <a href={freeTier.ctaLink} className="btn-primary flex items-center justify-center whitespace-nowrap">
                        {freeTier.cta}
                        <ExternalLinkIcon className="w-4 h-4 ml-2 flex-shrink-0" />
                      </a>
                    </div>
                  </div>

                  <div>
                    <ul className="space-y-2">
                      {freeTier.features.map((feature, featureIndex) => (
                        <li key={featureIndex} className="flex items-start gap-2">
                          <div className="w-1.5 h-1.5 bg-tokyo-night-green rounded-full mt-2 flex-shrink-0"></div>
                          <span className="text-tokyo-night-fg-dark text-sm">{feature}</span>
                        </li>
                      ))}
                    </ul>

                    {freeTier.limitations.length > 0 && (
                      <div className="mt-6">
                        <h4 className="text-sm font-semibold text-tokyo-night-comment mb-2">Limitations:</h4>
                        <ul className="space-y-1">
                          {freeTier.limitations.map((limitation, limitIndex) => (
                            <li key={limitIndex} className="flex items-start gap-2">
                              <div className="w-1.5 h-1.5 bg-tokyo-night-comment rounded-full mt-2 flex-shrink-0"></div>
                              <span className="text-tokyo-night-comment text-xs">{limitation}</span>
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                  </div>
                </div>
              </div>
            </div>
          </motion.div>

          {/* Paid Tiers */}
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6, delay: 0.2 }}
            className="mb-16"
          >
            <div className="text-center mb-8">
              <h2 className="text-2xl md:text-3xl font-bold gradient-text mb-2">
                Upgrade for More
              </h2>
              <p className="text-tokyo-night-fg-dark">
                Enhanced features, priority processing, and service level agreements
              </p>
            </div>

            <div className="grid md:grid-cols-3 gap-6">
              {paidTiers.map((plan, index) => (
                <motion.div
                  key={plan.name}
                  initial={{ opacity: 0, y: 30 }}
                  whileInView={{ opacity: 1, y: 0 }}
                  viewport={{ once: true }}
                  transition={{ duration: 0.6, delay: index * 0.1 }}
                  className="glow-card p-6 relative flex flex-col h-full"
                >
                  <div className="text-center mb-6">
                    <h3 className="text-xl font-bold text-tokyo-night-fg mb-2">{plan.name}</h3>
                    <div className="mb-3">
                      <span className="text-3xl font-bold gradient-text">{plan.price}</span>
                      <span className="text-tokyo-night-comment ml-1 text-sm">{plan.period}</span>
                      {plan.dailyCost && (
                        <div className="text-tokyo-night-comment text-xs mt-1">{plan.dailyCost}</div>
                      )}
                    </div>
                    <p className="text-tokyo-night-fg-dark text-sm">{plan.description}</p>
                  </div>

                  <div className="flex-grow">
                    <ul className="space-y-2 mb-6">
                      {plan.features.map((feature, featureIndex) => (
                        <li key={featureIndex} className="flex items-start gap-2">
                          <div className="w-1.5 h-1.5 bg-tokyo-night-green rounded-full mt-2 flex-shrink-0"></div>
                          <span className="text-tokyo-night-fg-dark text-sm">{feature}</span>
                        </li>
                      ))}
                    </ul>

                    {plan.limitations.length > 0 && (
                      <div className="mb-6">
                        <h4 className="text-sm font-semibold text-tokyo-night-comment mb-2">Limitations:</h4>
                        <ul className="space-y-1">
                          {plan.limitations.map((limitation, limitIndex) => (
                            <li key={limitIndex} className="flex items-start gap-2">
                              <div className="w-1.5 h-1.5 bg-tokyo-night-comment rounded-full mt-2 flex-shrink-0"></div>
                              <span className="text-tokyo-night-comment text-xs">{limitation}</span>
                            </li>
                          ))}
                        </ul>
                      </div>
                    )}
                  </div>

                  <div className="text-center mt-auto">
                    <a
                      href={plan.ctaLink}
                      className="btn-secondary w-full flex items-center justify-center text-sm whitespace-nowrap"
                    >
                      {plan.cta}
                      <ExternalLinkIcon className="w-4 h-4 ml-2 flex-shrink-0" />
                    </a>
                  </div>
                </motion.div>
              ))}
            </div>
          </motion.div>

          {/* Enterprise Tier - Special Section */}
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6, delay: 0.4 }}
          >
            <div className="text-center mb-8">
              <h2 className="text-2xl md:text-3xl font-bold gradient-text mb-2">
                Need More? Go Enterprise
              </h2>
              <p className="text-tokyo-night-fg-dark">
                For high-bandwidth projects and fully custom deployments
              </p>
            </div>

            <div className="max-w-2xl mx-auto">
              <div className="glow-card p-8 bg-gradient-to-br from-tokyo-night-bg-light to-tokyo-night-bg-dark border border-tokyo-night-yellow/30">
                <div className="grid md:grid-cols-2 gap-8">
                  <div>
                    <h3 className="text-2xl font-bold text-tokyo-night-fg mb-4">{enterpriseTier.name}</h3>
                    <div className="mb-4">
                      <span className="text-3xl font-bold gradient-text">{enterpriseTier.price}</span>
                      <span className="text-tokyo-night-comment ml-2">{enterpriseTier.period}</span>
                    </div>
                    <p className="text-tokyo-night-fg-dark mb-6">{enterpriseTier.description}</p>

                    <div className="flex">
                      <Link
                        to={enterpriseTier.ctaLink}
                        className="btn-primary"
                      >
                        {enterpriseTier.cta}
                      </Link>
                    </div>
                  </div>

                  <div>
                    <ul className="space-y-2">
                      {enterpriseTier.features.map((feature, featureIndex) => (
                        <li key={featureIndex} className="flex items-start gap-2">
                          <div className="w-1.5 h-1.5 bg-tokyo-night-yellow rounded-full mt-2 flex-shrink-0"></div>
                          <span className="text-tokyo-night-fg-dark text-sm">{feature}</span>
                        </li>
                      ))}
                    </ul>
                  </div>
                </div>
              </div>
            </div>
          </motion.div>
        </div>
      </section>

      {/* GPU Features */}
      <section className="section-padding bg-tokyo-night-bg-light/30">
        <div className="max-w-7xl mx-auto">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
            className="text-center mb-12"
          >
            <h2 className="text-3xl md:text-4xl font-bold gradient-text mb-4">
              GPU-Powered Features
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark">
              Advanced processing features through FrameWorks infrastructure and Livepeer network
            </p>
          </motion.div>

          <div className="grid md:grid-cols-3 gap-8">
            {gpuFeatures.map((feature, index) => (
              <motion.div
                key={feature.title}
                initial={{ opacity: 0, y: 30 }}
                whileInView={{ opacity: 1, y: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.6, delay: index * 0.1 }}
                className="glow-card p-6"
              >
                <div className="text-4xl mb-4 text-center">{feature.icon}</div>
                <h3 className="text-xl font-bold text-tokyo-night-fg mb-3 text-center">{feature.title}</h3>
                <p className="text-tokyo-night-fg-dark text-sm mb-6 text-center">{feature.description}</p>

                <div className="space-y-3">
                  <div className="flex items-center gap-3 p-3 bg-tokyo-night-bg-dark rounded-lg">
                    <div className="w-2 h-2 bg-tokyo-night-blue rounded-full"></div>
                    <div>
                      <div className="text-sm font-medium text-tokyo-night-fg">Free</div>
                      <div className="text-xs text-tokyo-night-comment">{feature.freeAccess}</div>
                    </div>
                  </div>

                  <div className="flex items-center gap-3 p-3 bg-tokyo-night-bg-dark rounded-lg">
                    <div className="w-2 h-2 bg-tokyo-night-green rounded-full"></div>
                    <div>
                      <div className="text-sm font-medium text-tokyo-night-fg">Supporter (€50)</div>
                      <div className="text-xs text-tokyo-night-comment">{feature.supporterAccess}</div>
                    </div>
                  </div>

                  <div className="flex items-center gap-3 p-3 bg-tokyo-night-bg-dark rounded-lg">
                    <div className="w-2 h-2 bg-tokyo-night-cyan rounded-full"></div>
                    <div>
                      <div className="text-sm font-medium text-tokyo-night-fg">Developer (€250)</div>
                      <div className="text-xs text-tokyo-night-comment">{feature.developerAccess}</div>
                    </div>
                  </div>

                  <div className="flex items-center gap-3 p-3 bg-tokyo-night-bg-dark rounded-lg">
                    <div className="w-2 h-2 bg-tokyo-night-yellow rounded-full"></div>
                    <div>
                      <div className="text-sm font-medium text-tokyo-night-fg">Production (€1000)</div>
                      <div className="text-xs text-tokyo-night-comment">{feature.productionAccess}</div>
                    </div>
                  </div>
                </div>
              </motion.div>
            ))}
          </div>
        </div>
      </section>

      {/* Hybrid Model */}
      <section className="section-padding">
        <div className="max-w-7xl mx-auto">
          <div className="grid md:grid-cols-2 gap-12 items-center">
            <motion.div
              initial={{ opacity: 0, x: -30 }}
              whileInView={{ opacity: 1, x: 0 }}
              transition={{ duration: 0.6 }}
            >
              <h2 className="text-3xl md:text-4xl font-bold gradient-text mb-6">
                Build Your Own Network, Or Use Ours
              </h2>
              <p className="text-lg text-tokyo-night-fg-dark mb-6">
                FrameWorks gives you complete architectural flexibility. Run your own infrastructure
                everywhere, or let us handle the heavy lifting while you focus on your product.
              </p>
              <div className="space-y-4">
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-blue rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">Your Own Edge Network</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">Deploy Edge nodes anywhere - your data centers, AWS, bare metal. You control the infrastructure.</p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-green rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">Complete FrameWorks Pipeline</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">Or use our hosted infrastructure - ingest, processing, delivery, analytics. Just send us streams.</p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-yellow rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">Hybrid Approach</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">Mix and match - your edge for delivery, our cloud for AI processing. Build exactly what you need.</p>
                  </div>
                </div>
              </div>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, x: 30 }}
              whileInView={{ opacity: 1, x: 0 }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-8"
            >
              <h3 className="text-2xl font-bold text-tokyo-night-fg mb-6">Architecture Options</h3>
              <div className="space-y-4">
                <div className="bg-tokyo-night-bg-dark p-4 rounded-lg">
                  <h4 className="font-semibold text-tokyo-night-blue mb-2">🏠 Self-Hosted Everything</h4>
                  <div className="text-tokyo-night-fg-dark text-sm space-y-1">
                    <div>• Your servers, your control</div>
                    <div>• Docker deployment anywhere</div>
                  </div>
                </div>
                <div className="bg-tokyo-night-bg-dark p-4 rounded-lg">
                  <h4 className="font-semibold text-tokyo-night-green mb-2">🌐 Your Network + Our Network</h4>
                  <div className="text-tokyo-night-fg-dark text-sm space-y-1">
                    <div>• Your network nodes for delivery</div>
                    <div>• Our network for AI, transcoding, compositing</div>
                  </div>
                </div>
                <div className="bg-tokyo-night-bg-dark p-4 rounded-lg">
                  <h4 className="font-semibold text-tokyo-night-cyan mb-2">☁️ Full FrameWorks Pipeline</h4>
                  <div className="text-tokyo-night-fg-dark text-sm space-y-1">
                    <div>• We handle ingest, processing, delivery</div>
                    <div>• You focus on your product</div>
                  </div>
                </div>
                <div className="bg-tokyo-night-bg-dark p-4 rounded-lg">
                  <h4 className="font-semibold text-tokyo-night-yellow mb-2">🏢 Enterprise Custom</h4>
                  <div className="text-tokyo-night-fg-dark text-sm space-y-1">
                    <div>• Private deployments in your VPC</div>
                    <div>• White-label everything</div>
                    <div>• We run it or train your team</div>
                  </div>
                </div>
              </div>
            </motion.div>
          </div>
        </div>
      </section>

      {/* FAQ */}
      <section className="section-padding bg-tokyo-night-bg-light/30">
        <div className="max-w-4xl mx-auto">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
            className="text-center mb-12"
          >
            <h2 className="text-3xl md:text-4xl font-bold gradient-text mb-4">
              Frequently Asked Questions
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark">
              Everything you need to know about FrameWorks pricing
            </p>
          </motion.div>

          <div className="space-y-6">
            {faqs.map((faq, index) => (
              <motion.div
                key={index}
                initial={{ opacity: 0, y: 30 }}
                whileInView={{ opacity: 1, y: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.6, delay: index * 0.1 }}
                className="glow-card p-6"
              >
                <h3 className="text-lg font-semibold text-tokyo-night-fg mb-3">{faq.question}</h3>
                <p className="text-tokyo-night-fg-dark">{faq.answer}</p>
              </motion.div>
            ))}
          </div>
        </div>
      </section>

      {/* CTA */}
      <section className="section-padding bg-gradient-to-br from-tokyo-night-bg-dark to-tokyo-night-bg">
        <div className="max-w-7xl mx-auto text-center">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <h2 className="text-3xl md:text-4xl font-bold gradient-text mb-6">
              Ready to Start Building?
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark max-w-2xl mx-auto mb-8">
              Start with full self-hosting capabilities, upgrade for GPU features and hosted services
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <a
                href={config.appUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="btn-primary flex items-center justify-center"
              >
                Start Free Today
                <ExternalLinkIcon className="w-4 h-4 ml-2" />
              </a>
              <Link to="/contact" className="btn-secondary">
                Schedule Demo
              </Link>
            </div>
            <p className="text-tokyo-night-comment text-sm mt-6">
              No credit card required • Full self-hosting • Shared bandwidth pool access
            </p>
          </motion.div>
        </div>
      </section>
    </div>
  )
}

export default Pricing 