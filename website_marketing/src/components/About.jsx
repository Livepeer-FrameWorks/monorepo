import { motion } from 'framer-motion'
import { Link } from 'react-router-dom'
import config from '../config'
import BetaBadge from './BetaBadge'
import InfoTooltip from './InfoTooltip'
import { ChartBarIcon, ArrowTopRightOnSquareIcon } from '@heroicons/react/24/outline'

const About = () => {
  const team = [
    {
      name: "MistServer Team",
      role: "Video Infrastructure Pioneers",
      description: "The team behind MistServer - the battle-tested media server powering streaming infrastructure worldwide. Decades of experience building rock-solid video technology at scale",
      avatar: "/mist.svg",
      isLogo: true
    },
    {
      name: "Livepeer Network",
      role: "Decentralized Video Infrastructure",
      description: "The world's largest decentralized video network, processing millions of minutes of video daily. Their treasury backing enables FrameWorks to offer a free tier and feature-rich supporter tier",
      avatar: "/livepeer-light.svg",
      isLogo: true
    }
  ]

  const timeline = [
    {
      year: "Now",
      title: "Public Beta",
      description: "Core platform available. Auto‑discovery, compositing, and AI flagged as Beta with limited capacity."
    },
    {
      year: "Sep 2025",
      title: "IBC Demo Milestone",
      description: "First user onboarding at IBC Amsterdam (Sep 12–15)."
    },
    {
      year: "2026+",
      title: "Scale & Expand",
      description: "Team expansion, strategic partnerships, mobile apps, and global infrastructure deployment."
    },
    {
      year: "Future",
      title: "Federalized CDN Network",
      description: "Independent edge clusters with resource exchange — a cost‑effective, federated CDN."
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
              About FrameWorks
            </h1>
            <p className="text-xl text-tokyo-night-fg-dark max-w-3xl mx-auto">
              The only streaming platform that combines full self-hosting capabilities
              with hosted processing, backed by unique features you won't find anywhere else.
            </p>
          </motion.div>
        </div>
      </section>

      {/* Mission */}
      <section className="section-padding">
        <div className="max-w-7xl mx-auto">
          <div className="grid md:grid-cols-2 gap-12 items-center">
            <motion.div
              initial={{ opacity: 0, x: -30 }}
              whileInView={{ opacity: 1, x: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6 }}
            >
              <h2 className="text-3xl md:text-4xl font-bold gradient-text mb-6">
                Our Mission
              </h2>
              <p className="text-lg text-tokyo-night-fg-dark mb-6">
                We're building the streaming infrastructure that doesn't lock you in.
                Need custom features? Build them yourself or let us help.
                Switch providers? Your infrastructure comes with you.
                Cloud bills spiraling? Run it yourself with our open source stack.
              </p>
              <p className="text-lg text-tokyo-night-fg-dark mb-6">
                Built by the MistServer team and subsidized by the Livepeer treasury,
                we're on a mission to democratize video infrastructure by leveraging decentralized protocols
                and open source technology.
              </p>
              <p className="text-lg text-tokyo-night-fg-dark mb-6">
                Run it yourself, use our hosted services, or mix and match.
                Uncloud your infrastructure.
              </p>
              <div className="flex flex-col sm:flex-row gap-4">
                <a href={config.appUrl} className="btn-primary flex items-center justify-center whitespace-nowrap">
                  Start Free
                  <ArrowTopRightOnSquareIcon className="w-4 h-4 ml-2 flex-shrink-0" />
                </a>
                <Link to="/contact" className="btn-secondary">
                  Talk to Sales
                </Link>
              </div>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, x: 30 }}
              whileInView={{ opacity: 1, x: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-8"
            >
              <h3 className="text-2xl font-bold text-tokyo-night-fg mb-6">Why FrameWorks?</h3>
              <div className="space-y-4">
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-blue rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">Free Self-Hosting</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">Complete open source self-hosting with optional hosted processing</p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-green rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <div className="flex items-center gap-2">
                      <h4 className="font-semibold text-tokyo-night-fg">Unique Features</h4>
                      <BetaBadge label="Beta" />
                    </div>
                    <p className="text-tokyo-night-fg-dark text-sm">Auto‑discovery, multi‑stream compositing, AI processing <span className="inline-flex items-center gap-1"><InfoTooltip>Experimental during beta; performance and hardware support vary.</InfoTooltip></span></p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-yellow rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">Architectural Flexibility</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">Build your own edge network or use our complete pipeline - your choice</p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-orange rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">VOD</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">DVR and Clips available today; uploaded VOD library management is planned.</p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-cyan rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">B2B Focus</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">Custom features, integrations, and enterprise support</p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-purple rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">Public Domain Licensed</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">No attribution required, no copyleft restrictions - you truly own what you deploy, including for commercial use</p>
                  </div>
                </div>
              </div>
            </motion.div>
          </div>
        </div>
      </section>

      {/* Differentiators */}
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
              What Makes Us Different?
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark">
              Features and capabilities you won't find anywhere else
            </p>
          </motion.div>

          <div className="grid md:grid-cols-2 gap-8">
            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.1 }}
              className="glow-card p-6 relative"
            >
              <div className="absolute top-4 right-4">
                <span className="bg-tokyo-night-blue/20 text-tokyo-night-blue px-2 py-1 rounded text-xs font-medium">
                  Treasury Backed
                </span>
              </div>
              <div className="text-4xl mb-4">💎</div>
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-3">Livepeer Network Integration</h3>
              <p className="text-tokyo-night-fg-dark">Subsidized by the Livepeer treasury and powered by the world's largest decentralized video network for transcoding and AI processing.</p>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-6 relative"
            >
              <div className="absolute top-4 right-4">
                <span className="bg-tokyo-night-blue/20 text-tokyo-night-blue px-2 py-1 rounded text-xs font-medium">
                  Industry First
                </span>
              </div>
              <div className="text-4xl mb-4">📹</div>
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-3">Auto-Discovery App</h3>
              <p className="text-tokyo-night-fg-dark">The only platform with a drop-in app that automatically discovers and connects ONVIF cameras, VISCA PTZ controls, NDI sources, USB webcams, and HDMI inputs. Zero configuration required - it just works.</p>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.3 }}
              className="glow-card p-6 relative"
            >
              <div className="absolute top-4 right-4">
                <span className="bg-tokyo-night-blue/20 text-tokyo-night-blue px-2 py-1 rounded text-xs font-medium">
                  Advanced Feature
                </span>
              </div>
              <div className="text-4xl mb-4">🎬</div>
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-3">Multi-stream Compositing</h3>
              <p className="text-tokyo-night-fg-dark">Combine multiple input streams into one composite output with picture-in-picture, overlays, and OBS-style mixing capabilities. Most platforms don't offer this at all.</p>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.4 }}
              className="glow-card p-6 relative"
            >
              <div className="absolute top-4 right-4">
                <span className="bg-tokyo-night-blue/20 text-tokyo-night-blue px-2 py-1 rounded text-xs font-medium">
                  Unique Model
                </span>
              </div>
              <div className="text-4xl mb-4">🌐</div>
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-3">Architectural Freedom</h3>
              <p className="text-tokyo-night-fg-dark">Deploy your own edge nodes for maximum performance and control or use our complete pipeline. Scale however works best for your use case.</p>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.5 }}
              className="glow-card p-6 relative"
            >
              <div className="absolute top-4 right-4">
                <span className="bg-tokyo-night-blue/20 text-tokyo-night-blue px-2 py-1 rounded text-xs font-medium">
                  Open Source
                </span>
              </div>
              <div className="text-4xl mb-4">🔓</div>
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-3">Public Domain Licensed</h3>
              <p className="text-tokyo-night-fg-dark">Unlike typical "open source" with restrictive licenses, our entire stack is public domain. No attribution required, no copyleft restrictions - you truly own what you deploy, including for commercial use.</p>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.6 }}
              className="glow-card p-6 relative"
            >
              <div className="absolute top-4 right-4">
                <span className="bg-tokyo-night-blue/20 text-tokyo-night-blue px-2 py-1 rounded text-xs font-medium">
                  Live Updates
                </span>
              </div>
              <div className="mb-4">
                <ChartBarIcon className="w-12 h-12 text-tokyo-night-cyan" />
              </div>
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-3">Real-time Analytics & Monitoring</h3>
              <p className="text-tokyo-night-fg-dark">Live viewer counts, bandwidth monitoring, instant alerts, and performance metrics. See what's happening across your entire streaming network in real-time with WebSocket-powered dashboards.</p>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.7 }}
              className="glow-card p-6 relative"
            >
              <div className="absolute top-4 right-4">
                <span className="bg-tokyo-night-blue/20 text-tokyo-night-blue px-2 py-1 rounded text-xs font-medium">
                  AI-powered
                </span>
              </div>
              <div className="text-4xl mb-4">🤖</div>
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-3">Live AI Processing</h3>
              <p className="text-tokyo-night-fg-dark">Real-time speech-to-text, object detection, content classification, and automated clipping with AI segmentation. Process video intelligence at the edge while streaming live.</p>
            </motion.div>
          </div>
        </div>
      </section>

      {/* Technology Stack */}
      <section className="section-padding">
        <div className="max-w-7xl mx-auto">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
            className="text-center mb-12"
          >
            <h2 className="text-3xl md:text-4xl font-bold gradient-text mb-4">
              Built on Proven Technology
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark">
              Production-ready infrastructure with modern, scalable components
            </p>
          </motion.div>

          <div className="grid md:grid-cols-3 gap-8">
            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.1 }}
              className="glow-card p-6"
            >
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-4">Broad Support</h3>
              <div className="space-y-3">
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/mist.svg" alt="MistServer" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">MistServer - Battle-tested media server</span>
                </div>
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/livepeer-light.svg" alt="Livepeer" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">Livepeer Network - Decentralized transcoding & AI</span>
                </div>
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/webrtc.svg" alt="WebRTC" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">WebRTC, RTMP, SRT, HLS & DASH - All the streaming protocols you need</span>
                </div>
              </div>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-6"
            >
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-4">Backend Infrastructure</h3>
              <div className="space-y-3">
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/go-lightblue.svg" alt="Go" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">Go - High-performance, custom microservices</span>
                </div>
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/kafka.svg" alt="Kafka" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">Apache Kafka - Event streaming</span>
                </div>
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/postgres.svg" alt="PostgreSQL" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">YugabyteDB - State & configuration</span>
                </div>
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/clickhouse.svg" alt="ClickHouse" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">ClickHouse - Time-series analytics</span>
                </div>
              </div>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.3 }}
              className="glow-card p-6"
            >
              <h3 className="text-xl font-bold text-tokyo-night-fg mb-4">Deployment & Operations</h3>
              <div className="space-y-3">
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/docker-mark-blue.svg" alt="Docker" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">Docker - Containerized deployment</span>
                </div>
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/websocket.svg" alt="WebSockets" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">WebSockets - Real-time updates</span>
                </div>
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/nginx.svg" alt="Nginx" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">Nginx - Load balancing & SSL</span>
                </div>
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/prometheus.svg" alt="Prometheus" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">Prometheus - Monitoring & metrics</span>
                </div>
                <div className="flex items-center gap-3">
                  <div className="w-8 h-8 bg-white/10 rounded-full flex items-center justify-center">
                    <img src="/svelte.svg" alt="Svelte" className="w-5 h-5" />
                  </div>
                  <span className="text-tokyo-night-fg-dark">SvelteKit - Modern web interface</span>
                </div>
              </div>
            </motion.div>
          </div>
        </div>
      </section>

      {/* Timeline */}
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
              Our Journey
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark">
              From concept to industry-leading streaming platform
            </p>
          </motion.div>

          <div className="space-y-8">
            {timeline.map((item, index) => (
              <motion.div
                key={index}
                initial={{ opacity: 0, x: index % 2 === 0 ? -30 : 30 }}
                whileInView={{ opacity: 1, x: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.6, delay: index * 0.1 }}
                className="flex items-center gap-6"
              >
                <div className="w-20 text-center">
                  <div className="text-2xl font-bold gradient-text">{item.year}</div>
                </div>
                <div className="flex-1 glow-card p-6">
                  <h3 className="text-xl font-bold text-tokyo-night-fg mb-2">{item.title}</h3>
                  <p className="text-tokyo-night-fg-dark">{item.description}</p>
                </div>
              </motion.div>
            ))}
          </div>
        </div>
      </section>

      {/* Team */}
      <section className="section-padding">
        <div className="max-w-7xl mx-auto">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
            className="text-center mb-12"
          >
            <h2 className="text-3xl md:text-4xl font-bold gradient-text mb-4">
              Powered by MistServer & Livepeer
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark">
              Industry-leading video infrastructure expertise backed by the Livepeer treasury
            </p>
          </motion.div>

          <div className="grid md:grid-cols-2 gap-8 max-w-5xl mx-auto">
            {team.map((member, index) => (
              <motion.div
                key={index}
                initial={{ opacity: 0, y: 30 }}
                whileInView={{ opacity: 1, y: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.6, delay: index * 0.1 }}
                className="glow-card p-8 text-center"
              >
                {member.isLogo ? (
                  <div className="mb-4 flex justify-center">
                    <img src={member.avatar} alt={member.name} className="w-20 h-20" />
                  </div>
                ) : (
                  <div className="text-6xl mb-4">{member.avatar}</div>
                )}
                <h3 className="text-2xl font-bold text-tokyo-night-fg mb-2">{member.name}</h3>
                <p className="text-tokyo-night-blue mb-4">{member.role}</p>
                <p className="text-tokyo-night-fg-dark">{member.description}</p>
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
              Ready to Transform Your Streaming?
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark max-w-2xl mx-auto mb-8">
              Join the growing community of developers and businesses using FrameWorks
              for their streaming infrastructure. Run it yourself, or let us help - it's your choice.
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <a
                href={config.appUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="btn-primary flex items-center justify-center"
              >
                Start Free Today
                <ArrowTopRightOnSquareIcon className="w-4 h-4 ml-2" />
              </a>
              <Link to="/contact" className="btn-secondary">
                Enterprise Sales
              </Link>
              <a
                href={config.githubUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="btn-secondary flex items-center justify-center"
              >
                View Open Source
                <ArrowTopRightOnSquareIcon className="w-4 h-4 ml-2" />
              </a>
            </div>
            <p className="text-tokyo-night-comment text-sm mt-6">
              No credit card required • Open source • Run it anywhere
            </p>
          </motion.div>
        </div>
      </section>
    </div>
  )
}

export default About 
