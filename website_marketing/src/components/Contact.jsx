import { motion } from 'framer-motion'
import { useState, useEffect, useRef } from 'react'
import config from '../config'

const Contact = () => {
  const [formData, setFormData] = useState({
    name: '',
    email: '',
    company: '',
    message: '',
    phone_number: '', // Honeypot
    human_check: 'robot' // Default to robot
  })

  const [loading, setLoading] = useState(false)
  const [success, setSuccess] = useState(false)
  const [error, setError] = useState('')

  // Behavioral tracking
  const [behavior, setBehavior] = useState({
    mouse: false,
    typed: false,
    formShownAt: Date.now(),
    submittedAt: null
  })

  const formRef = useRef(null)

  // Track mouse movement
  useEffect(() => {
    const handleMouseMove = () => {
      setBehavior(prev => ({ ...prev, mouse: true }))
    }

    const handleKeyDown = () => {
      setBehavior(prev => ({ ...prev, typed: true }))
    }

    // Add event listeners
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
      [e.target.name]: e.target.value
    })
  }

  const handleSubmit = async (e) => {
    e.preventDefault()
    setLoading(true)
    setError('')

    try {
      const submissionData = {
        ...formData,
        behavior: {
          ...behavior,
          submittedAt: Date.now()
        }
      }

      const apiUrl = `${config.contactApiUrl}/api/contact`

      const response = await fetch(apiUrl, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify(submissionData)
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
          human_check: 'robot'
        })
        setBehavior({
          mouse: false,
          typed: false,
          formShownAt: Date.now(),
          submittedAt: null
        })
      } else {
        setError(result.error || 'Failed to send message')
        if (result.details && process.env.NODE_ENV === 'development') {
          console.log('Validation errors:', result.details)
        }
      }
    } catch (err) {
      setError('Network error. Please try again.')
      console.error('Contact form error:', err)
    } finally {
      setLoading(false)
    }
  }

  const contactMethods = [
    {
      icon: "📧",
      title: "Email",
      description: "Get in touch via email",
      contact: config.contactEmail,
      link: `mailto:${config.contactEmail}`,
      disabled: false
    },
    {
      icon: "💬",
      title: "Discord Community",
      description: "Join our Discord for ultra low latency chat",
      contact: "Invite link",
      link: config.discordUrl,
      disabled: false
    },
    {
      icon: "💭",
      title: "Forum",
      description: "For those who prefer a more structured discussion format",
      contact: "forum.frameworks.network",
      link: config.forumUrl,
      disabled: false
    },
    {
      icon: "🐛",
      title: "Issues",
      description: "Report bugs or request features",
      contact: "GitHub Issues",
      link: `${config.githubUrl}/issues`,
      disabled: false
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
            <h1 className="text-4xl md:text-6xl font-bold gradient-text mb-6">
              Contact Us
            </h1>
            <p className="text-xl text-tokyo-night-fg-dark max-w-3xl mx-auto">
              Have questions about FrameWorks? We'd love to hear from you.
            </p>
          </motion.div>
        </div>
      </section>

      {/* Contact Methods */}
      <section className="section-padding">
        <div className="max-w-7xl mx-auto">
          <div className="grid md:grid-cols-2 lg:grid-cols-4 gap-8 mb-12">
            {contactMethods.map((method, index) => (
              <motion.div
                key={method.title}
                initial={{ opacity: 0, y: 30 }}
                whileInView={{ opacity: 1, y: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.6, delay: index * 0.1 }}
                className={`glow-card p-6 text-center flex flex-col h-full ${method.disabled ? 'opacity-60' : ''}`}
              >
                <div className="text-4xl mb-4">{method.icon}</div>
                <h3 className="text-xl font-bold text-tokyo-night-fg mb-2">{method.title}</h3>
                <p className="text-tokyo-night-fg-dark mb-4 flex-grow">{method.description}</p>
                <div className="mt-auto">
                  {method.disabled ? (
                    <span className="text-tokyo-night-comment cursor-not-allowed">
                      {method.contact}
                    </span>
                  ) : (
                    <a
                      href={method.link}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="text-tokyo-night-blue hover:text-tokyo-night-blue/80 transition-colors duration-200"
                    >
                      {method.contact}
                    </a>
                  )}
                </div>
              </motion.div>
            ))}
          </div>

          {/* Contact Form */}
          <div className="max-w-2xl mx-auto">
            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6 }}
              className="glow-card p-8"
              ref={formRef}
            >
              <h2 className="text-2xl font-bold text-tokyo-night-fg mb-6 text-center">
                Send us a message
              </h2>

              <form onSubmit={handleSubmit} className="space-y-6">
                {/* Honeypot field - hidden from users */}
                <input
                  type="text"
                  name="phone_number"
                  value={formData.phone_number}
                  onChange={handleChange}
                  style={{ display: 'none' }}
                  tabIndex="-1"
                  autoComplete="off"
                />

                <div className="grid md:grid-cols-2 gap-6">
                  <div>
                    <label htmlFor="name" className="block text-tokyo-night-fg-dark mb-2">
                      Name *
                    </label>
                    <input
                      type="text"
                      id="name"
                      name="name"
                      value={formData.name}
                      onChange={handleChange}
                      required
                      disabled={success}
                      className={`w-full px-4 py-3 bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter rounded-lg focus:outline-none focus:border-tokyo-night-blue transition-colors duration-200 text-tokyo-night-fg ${success ? 'opacity-60 cursor-not-allowed' : ''}`}
                      placeholder="Your name"
                    />
                  </div>

                  <div>
                    <label htmlFor="email" className="block text-tokyo-night-fg-dark mb-2">
                      Email *
                    </label>
                    <input
                      type="email"
                      id="email"
                      name="email"
                      value={formData.email}
                      onChange={handleChange}
                      required
                      disabled={success}
                      className={`w-full px-4 py-3 bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter rounded-lg focus:outline-none focus:border-tokyo-night-blue transition-colors duration-200 text-tokyo-night-fg ${success ? 'opacity-60 cursor-not-allowed' : ''}`}
                      placeholder="your@email.com"
                    />
                  </div>
                </div>

                <div>
                  <label htmlFor="company" className="block text-tokyo-night-fg-dark mb-2">
                    Company
                  </label>
                  <input
                    type="text"
                    id="company"
                    name="company"
                    value={formData.company}
                    onChange={handleChange}
                    disabled={success}
                    className={`w-full px-4 py-3 bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter rounded-lg focus:outline-none focus:border-tokyo-night-blue transition-colors duration-200 text-tokyo-night-fg ${success ? 'opacity-60 cursor-not-allowed' : ''}`}
                    placeholder="Your company"
                  />
                </div>

                <div>
                  <label htmlFor="message" className="block text-tokyo-night-fg-dark mb-2">
                    Message *
                  </label>
                  <textarea
                    id="message"
                    name="message"
                    value={formData.message}
                    onChange={handleChange}
                    required
                    rows="6"
                    disabled={success}
                    className={`w-full px-4 py-3 bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter rounded-lg focus:outline-none focus:border-tokyo-night-blue transition-colors duration-200 text-tokyo-night-fg resize-none ${success ? 'opacity-60 cursor-not-allowed' : ''}`}
                    placeholder="Tell us about your project or questions..."
                  />
                </div>

                {/* Human verification toggle */}
                <div className="space-y-3">
                  <label className="block text-tokyo-night-fg-dark mb-2">
                    Verification *
                  </label>
                  <div className="flex gap-4 flex-wrap">
                    <label className={`flex items-center gap-3 p-3 rounded-lg border border-tokyo-night-red/30 bg-tokyo-night-red/10 cursor-pointer flex-1 ${success ? 'opacity-60 cursor-not-allowed' : ''}`}>
                      <input
                        type="radio"
                        name="human_check"
                        value="robot"
                        checked={formData.human_check === 'robot'}
                        onChange={handleChange}
                        disabled={success}
                        className="text-tokyo-night-red flex-shrink-0"
                      />
                      <span className="text-2xl">🤖</span>
                      <div className="text-tokyo-night-red text-sm">
                        <div>I'm a robot —</div>
                        <div>discard this message</div>
                      </div>
                    </label>
                    <label className={`flex items-center gap-3 p-3 rounded-lg border border-tokyo-night-green/30 bg-tokyo-night-green/10 cursor-pointer flex-1 ${success ? 'opacity-60 cursor-not-allowed' : ''}`}>
                      <input
                        type="radio"
                        name="human_check"
                        value="human"
                        checked={formData.human_check === 'human'}
                        onChange={handleChange}
                        disabled={success}
                        className="text-tokyo-night-green flex-shrink-0"
                      />
                      <span className="text-2xl">👋</span>
                      <div className="text-tokyo-night-green text-sm">
                        <div>I'm human —</div>
                        <div>please send this</div>
                      </div>
                    </label>
                  </div>
                </div>

                {success ? (
                  <motion.div
                    initial={{ opacity: 0, y: 30 }}
                    animate={{ opacity: 1, y: 0 }}
                    className="glass-card p-6 border-2 border-tokyo-night-green/30"
                  >
                    <div className="text-center">
                      <h3 className="text-xl font-semibold text-tokyo-night-green mb-2">
                        ✅ Message Sent Successfully!
                      </h3>
                      <p className="text-tokyo-night-fg-dark">
                        Thank you for your message! We'll get back to you soon.
                      </p>
                    </div>
                  </motion.div>
                ) : (
                  <button
                    type="submit"
                    disabled={loading}
                    className="btn-primary w-full"
                  >
                    {loading ? (
                      <span className="flex items-center justify-center gap-2">
                        <div className="loading-spinner"></div>
                        Sending...
                      </span>
                    ) : (
                      'Send Message'
                    )}
                  </button>
                )}

                {error && (
                  <motion.div
                    initial={{ opacity: 0, y: 30 }}
                    animate={{ opacity: 1, y: 0 }}
                    className="glass-card p-6 mb-8 border-2 border-tokyo-night-red/30"
                  >
                    <div className="text-center">
                      <h3 className="text-xl font-semibold text-tokyo-night-red mb-2">
                        ❌ Error
                      </h3>
                      <p className="text-tokyo-night-fg-dark">{error}</p>
                    </div>
                  </motion.div>
                )}
              </form>
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
              Common Questions
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark">
              Quick answers to frequently asked questions
            </p>
          </motion.div>

          <div className="space-y-6">
            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.1 }}
              className="glow-card p-6"
            >
              <h3 className="text-lg font-semibold text-tokyo-night-fg mb-3">
                How do I get started with FrameWorks?
              </h3>
              <p className="text-tokyo-night-fg-dark">
                You have two options: Deploy the full stack yourself using our Docker Compose setup, or use our hosted service and save costs by adding your own edge nodes for a hybrid approach. Both get you streaming in minutes.
              </p>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-6"
            >
              <h3 className="text-lg font-semibold text-tokyo-night-fg mb-3">
                Do you offer commercial support?
              </h3>
              <p className="text-tokyo-night-fg-dark">
                Yes! We offer enterprise support with SLAs, custom development, managed hosting, and priority GPU access. Contact us to discuss enterprise pricing and requirements for your organization.
              </p>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.3 }}
              className="glow-card p-6"
            >
              <h3 className="text-lg font-semibold text-tokyo-night-fg mb-3">
                Where can I get help and support?
              </h3>
              <p className="text-tokyo-night-fg-dark">
                For community support, join our Discord for quick questions or use our Discourse forum for detailed discussions. Both have active FrameWorks team participation. For enterprise support with SLAs, contact us directly.
              </p>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.4 }}
              className="glow-card p-6"
            >
              <h3 className="text-lg font-semibold text-tokyo-night-fg mb-3">
                Can I contribute to FrameWorks?
              </h3>
              <p className="text-tokyo-night-fg-dark">
                Absolutely! FrameWorks is public domain: our code is completely free for anyone to use, modify, distribute, or even sell without restrictions. Contributions are welcome via GitHub, or join our Discord and forum to discuss ideas and improvements.
              </p>
            </motion.div>
          </div>
        </div>
      </section>
    </div>
  )
}

export default Contact 