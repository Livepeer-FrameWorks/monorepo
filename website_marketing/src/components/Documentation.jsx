import { motion } from 'framer-motion'
import { Link } from 'react-router-dom'
import config from '../config'
import BetaBadge from './BetaBadge'
import { RocketLaunchIcon, SparklesIcon, ArrowTopRightOnSquareIcon } from '@heroicons/react/24/outline'

const Documentation = () => {
  const sections = [
    {
      title: "Getting Started",
      description: "Quick setup and deployment guide",
      items: [
        { title: "Installation", description: "Deploy with Docker Compose" },
        { title: "Configuration", description: "Environment variables and settings" },
        { title: "First Stream", description: "Create your first live stream" }
      ]
    },
    {
      title: "API Reference",
      description: "Complete API documentation",
      items: [
        { title: "Authentication", description: "JWT tokens and API keys" },
        { title: "Streams", description: "Create and manage live streams" },
        { title: "Analytics", description: "Stream metrics and analytics" }
      ]
    },
    {
      title: "Architecture",
      description: "System components and deployment",
      items: [
        { title: "Components", description: "APIs, MistServer, Livepeer, YugabyteDB, ClickHouse" },
        { title: "Deployment", description: "Central, Regional, and Edge tiers" },
        { title: "Scaling", description: "Multi-region deployment patterns" }
      ]
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
              Documentation
            </h1>
            <p className="text-xl text-tokyo-night-fg-dark max-w-3xl mx-auto mb-8">
              Browse repo docs and open the in‑app API Explorer. Quick CLI start below.
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <a href={`${config.githubUrl}/tree/master/docs`} target="_blank" rel="noopener noreferrer" className="btn-primary flex items-center justify-center whitespace-nowrap">
                Browse Repo Docs
                <ArrowTopRightOnSquareIcon className="w-4 h-4 ml-2 flex-shrink-0" />
              </a>
              <a
                href={`${config.appUrl.replace(/\/+$/, '')}/developer/api`}
                target="_blank"
                rel="noopener noreferrer"
                className="btn-secondary flex items-center justify-center"
              >
                Open API Explorer
                <ArrowTopRightOnSquareIcon className="w-4 h-4 ml-2" />
              </a>
            </div>
          </motion.div>
        </div>
      </section>

      {/* Highlights */}
      <section className="section-padding">
        <div className="max-w-7xl mx-auto">
          <div className="grid md:grid-cols-2 gap-8">
            <motion.a
              href={`${config.githubUrl}/blob/master/docs/TLDR.md`}
              target="_blank"
              rel="noopener noreferrer"
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.1 }}
              className="glow-card p-6 block hover:scale-[1.01] transition"
            >
              <h2 className="text-2xl font-bold text-tokyo-night-fg mb-2">Architecture TL;DR</h2>
              <p className="text-tokyo-night-fg-dark">High‑level overview of FrameWorks architecture and components</p>
            </motion.a>
            <motion.a
              href={`${config.githubUrl}/blob/master/docs/ROADMAP.md`}
              target="_blank"
              rel="noopener noreferrer"
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-6 block hover:scale-[1.01] transition"
            >
              <h2 className="text-2xl font-bold text-tokyo-night-fg mb-2">Roadmap</h2>
              <p className="text-tokyo-night-fg-dark">Upcoming features and current status by area</p>
            </motion.a>
          </div>
        </div>
      </section>

      {/* Quick Start */}
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
              Quick Start
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark">
              Get FrameWorks running in under 5 minutes
            </p>
          </motion.div>

          <div className="max-w-4xl mx-auto">
            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-8 bg-gradient-to-br from-tokyo-night-bg-light to-tokyo-night-bg-dark"
            >
              <div className="flex items-center gap-3 mb-6">
                <div className="w-3 h-3 bg-tokyo-night-red rounded-full"></div>
                <div className="w-3 h-3 bg-tokyo-night-yellow rounded-full"></div>
                <div className="w-3 h-3 bg-tokyo-night-green rounded-full"></div>
                <span className="text-tokyo-night-comment text-sm ml-2">Terminal</span>
              </div>
              <div className="text-left font-mono">
                <div className="text-tokyo-night-green mb-2">$ git clone https://github.com/livepeer-frameworks/monorepo.git</div>
                <div className="text-tokyo-night-comment mb-4">
                  Cloning into 'monorepo'...<br />
                  ✓ Receiving objects: 100% (1247/1247), done.<br />
                  ✓ Resolving deltas: 100% (892/892), done.
                </div>
                <div className="text-tokyo-night-yellow mb-2">$ docker-compose up -d</div>
                <div className="text-tokyo-night-comment mb-4">
                  ✓ Creating network "frameworks_default"<br />
                  ✓ Creating yugabytedb... done<br />
                  ✓ Creating clickhouse... done<br />
                  ✓ Creating mistserver... done<br />
                  ✓ Creating backend... done<br />
                  ✓ Creating frontend... done<br />
                  ✓ Creating nginx... done
                </div>
                <div className="text-tokyo-night-cyan mb-2 flex items-center gap-2">
                  <RocketLaunchIcon className="w-5 h-5" />
                  FrameWorks is running at http://localhost:18090
                </div>
                <div className="text-tokyo-night-green flex items-center gap-2">
                  <SparklesIcon className="w-5 h-5" />
                  Ready to stream! Check the dashboard for your stream key
                </div>
              </div>
            </motion.div>
          </div>
        </div>
      </section>

      {/* API Example */}
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
              API Example
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark">
              Create a stream and start broadcasting
            </p>
          </motion.div>

          <div className="max-w-4xl mx-auto">
            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-8 bg-gradient-to-br from-tokyo-night-bg-light to-tokyo-night-bg-dark"
            >
              <div className="flex items-center gap-3 mb-6">
                <div className="w-3 h-3 bg-tokyo-night-red rounded-full"></div>
                <div className="w-3 h-3 bg-tokyo-night-yellow rounded-full"></div>
                <div className="w-3 h-3 bg-tokyo-night-green rounded-full"></div>
                <span className="text-tokyo-night-comment text-sm ml-2">API Example</span>
              </div>
              <div className="text-left">
                <div className="text-tokyo-night-comment mb-2">// Create a new stream</div>
                <div className="text-tokyo-night-blue mb-0">const response = await fetch('<span className="text-tokyo-night-green">http://localhost:18090/api/streams</span>', {'{'}</div>
                <div className="text-tokyo-night-fg ml-4 mb-0">
                  method: '<span className="text-tokyo-night-yellow">POST</span>',<br />
                  headers: {'{'}<br />
                  <span className="ml-4">'<span className="text-tokyo-night-cyan">Content-Type</span>': '<span className="text-tokyo-night-green">application/json</span>',</span><br />
                  <span className="ml-4">'<span className="text-tokyo-night-cyan">Authorization</span>': '<span className="text-tokyo-night-green">Bearer YOUR_TOKEN</span>'</span><br />
                  {'}'}, <br />
                  body: JSON.stringify({'{'}<br />
                  <span className="ml-4">title: '<span className="text-tokyo-night-green">My Live Stream</span>',</span><br />
                  <span className="ml-4">description: '<span className="text-tokyo-night-green">Live streaming with FrameWorks</span>'</span><br />
                  {'})'}
                </div>
                <div className="text-tokyo-night-blue mb-4">{'});'}</div>

                <div className="text-tokyo-night-comment mb-4">
                  ✓ Stream created successfully<br />
                  ✓ Stream key: live_abc123def456<br />
                  ✓ URLs generated and ready
                </div>

                <div className="text-tokyo-night-blue mb-2">const stream = await response.json();</div>
                <div className="text-tokyo-night-yellow mb-2">console.log('<span className="text-tokyo-night-green">Ingest URL:</span>', stream.ingest_url);</div>
                <div className="text-tokyo-night-comment mb-2">→ rtmp://localhost:1935/live/live_abc123def456</div>
                <div className="text-tokyo-night-yellow mb-2">console.log('<span className="text-tokyo-night-green">Playback URL:</span>', stream.playback_url);</div>
                <div className="text-tokyo-night-comment mb-4">→ http://localhost:8080/hls/live_abc123def456.m3u8</div>

                <div className="text-tokyo-night-cyan">🎥 Ready to stream! Configure OBS with the ingest URL above</div>
              </div>
            </motion.div>
          </div>
        </div>
      </section>

      {/* CLI Quick Start */}
      <section className="section-padding bg-tokyo-night-bg-light/30">
        <div className="max-w-7xl mx-auto">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
            className="text-center mb-12"
          >
            <h2 className="text-3xl md:text-4xl font-bold gradient-text mb-4">CLI Quick Start</h2>
            <p className="text-xl text-tokyo-night-fg-dark">Install, init context, and bring up an edge</p>
          </motion.div>
          <div className="max-w-4xl mx-auto">
            <motion.div
              initial={{ opacity: 0, y: 30 }}
              whileInView={{ opacity: 1, y: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-8 bg-gradient-to-br from-tokyo-night-bg-light to-tokyo-night-bg-dark"
            >
              <div className="flex items-center gap-3 mb-6">
                <div className="w-3 h-3 bg-tokyo-night-red rounded-full"></div>
                <div className="w-3 h-3 bg-tokyo-night-yellow rounded-full"></div>
                <div className="w-3 h-3 bg-tokyo-night-green rounded-full"></div>
                <span className="text-tokyo-night-comment text-sm ml-2">FrameWorks CLI</span>
              </div>
              <div className="text-left">
                <div className="text-tokyo-night-comment mb-2"># 1. Get your enrollment token from the dashboard</div>
                <div className="text-tokyo-night-green mb-4">Login to your dashboard and create a bootstrap token</div>
                
                <div className="text-tokyo-night-comment mb-2"># 2. Install the CLI</div>
                <div className="text-tokyo-night-green mb-4">$ curl -L https://github.com/frameworks/cli/releases/latest/download/frameworks -o frameworks<br/>$ chmod +x frameworks && sudo mv frameworks /usr/local/bin/</div>
                
                <div className="text-tokyo-night-comment mb-2"># 3. Deploy your edge node</div>
                <div className="text-tokyo-night-green mb-2">$ frameworks edge init --domain stream.example.com --email ops@example.com</div>
                <div className="text-tokyo-night-comment mb-2"># Add your token to .edge.env</div>
                <div className="text-tokyo-night-green mb-2">$ frameworks edge preflight --domain stream.example.com</div>
                <div className="text-tokyo-night-green">$ frameworks edge enroll</div>
              </div>
            </motion.div>
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
              Need Help?
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark max-w-2xl mx-auto mb-8">
              Join our community or get in touch for support
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <Link to="/contact" className="btn-primary">
                Get Support
              </Link>
            </div>
          </motion.div>
        </div>
      </section>
    </div>
  )
}

export default Documentation 
