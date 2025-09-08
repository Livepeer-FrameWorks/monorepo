import { motion } from 'framer-motion'
import { Link } from 'react-router-dom'
// Demo player wrapper with status/health integration
import { Player as FrameworksPlayer } from '@livepeer-frameworks/player'
import BetaBadge from './BetaBadge'
import InfoTooltip from './InfoTooltip'
import { useState, useEffect } from 'react'
import config from '../config'
import { ArrowTopRightOnSquareIcon } from '@heroicons/react/24/outline'
import { 
  VideoCameraIcon,
  FilmIcon,
  GlobeAltIcon,
  LockOpenIcon
} from '@heroicons/react/24/outline'

const LandingPage = () => {
  const [showDemo, setShowDemo] = useState(false)
  const [showPlayer, setShowPlayer] = useState(false)
  const [logoAnimationComplete, setLogoAnimationComplete] = useState(false)
  const [demoState, setDemoState] = useState('booting')

  useEffect(() => {
    // Preload logo image so glitch strips can start immediately
    const img = new Image()
    img.src = '/frameworks-dark-vertical-lockup.svg'

    // Logo enters with glitch, then reveals player
    const playerTimer = setTimeout(() => {
      console.log('revealing player')
      setShowPlayer(true)
    }, 2000)

    // Remove logo element after fade animation completes
    const cleanupTimer = setTimeout(() => {
      setLogoAnimationComplete(true)
    }, 4200) // 2000ms delay + 2200ms animation duration

    const demoTimer = setTimeout(() => {
      setShowDemo(true)
    }, 1000)

    return () => {
      clearTimeout(playerTimer)
      clearTimeout(cleanupTimer)
      clearTimeout(demoTimer)
    }
  }, [])

  const uniqueFeatures = [
    {
      title: "Drop-in AV Device Discovery",
      description: "Our binary automatically discovers and connects IP cameras, USB webcams, HDMI inputs, and other AV devices. Zero configuration required.",
      icon: VideoCameraIcon,
      color: "text-tokyo-night-blue",
      badge: "Industry First"
    },
    {
      title: "Multi-stream Compositing",
      description: "Combine multiple input streams into one composite output with picture-in-picture, overlays, and OBS-style mixing capabilities.",
      icon: FilmIcon,
      color: "text-tokyo-night-green",
      badge: "Advanced Feature"
    },
    {
      title: "Hybrid Cloud + Self-hosted",
      description: "Combine our hosted service with your own nodes. One console to manage all your edge nodes worldwide.",
      icon: GlobeAltIcon,
      color: "text-tokyo-night-yellow",
      badge: "Unique Model"
    },
    {
      title: "Public Domain Licensed",
      description: "No attribution required, no copyleft restrictions. Unlike typical 'open source' licenses, you truly own what you deploy.",
      icon: LockOpenIcon,
      color: "text-tokyo-night-purple",
      badge: "Open Source"
    }
  ]

  const freeTierFeatures = [
    "Complete streaming stack",
    "Unlimited streams",
    "Shared bandwidth pool",
    "Open source & permissive licenses",
    "No cloud dependencies - runs anywhere",
    "Web dashboard included"
  ]

  return (
    <div className="pt-16">
      {/* Hero Section with Live Player */}
      <section className="section-padding bg-gradient-to-br from-tokyo-night-bg via-tokyo-night-bg-light to-tokyo-night-bg">
        <div className="max-w-7xl mx-auto text-center">
          <motion.div
            initial={{ opacity: 0, y: 50 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.8 }}
            className="mb-6"
          >
            <h1 className="text-5xl md:text-7xl font-bold gradient-text mb-8">
              Full-Stack Video
              <br />
              Infrastructure
            </h1>
            <p className="text-xl md:text-2xl text-tokyo-night-fg-dark max-w-3xl mx-auto mb-8">
              Most streaming platforms lock you into their ecosystem. We give you the keys.
            </p>
            <p className="text-lg text-tokyo-night-comment mb-8 max-w-3xl mx-auto">
              Open source stack • Permissive licenses • No cloud dependencies • Scales globally
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <a href={config.appUrl} className="btn-primary flex items-center justify-center whitespace-nowrap">
                Start Free
                <ArrowTopRightOnSquareIcon className="w-4 h-4 ml-2 flex-shrink-0" />
              </a>
              <Link to="/pricing" className="btn-secondary">
                View Pricing
              </Link>
            </div>
            <p className="text-tokyo-night-comment text-sm mt-4">
              Free tier includes self-hosting + access to shared bandwidth pool
            </p>
          </motion.div>

          {/* Live Player with Logo Glitch Animation */}
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.8, delay: 0.2 }}
            className="max-w-2xl mx-auto"
          >
            <div className="glass-card p-6 border-2 border-transparent hover:border-tokyo-night-blue/30 transition-colors duration-300 ease-out relative overflow-hidden w-full max-w-md sm:max-w-lg mx-auto">
              {/* Background gradient accent */}
              <div className="absolute inset-0 bg-gradient-to-br from-tokyo-night-blue/5 via-transparent to-tokyo-night-purple/5 pointer-events-none"></div>

              <div className="relative z-10 flex flex-col min-h-[400px]">
                {/* Frame Top */}
                <div className="frame-top text-center mb-6">
                  <div className="inline-flex items-center gap-3 mb-3">
                    <div className="w-3 h-3 bg-tokyo-night-comment rounded-full"></div>
                    <h2 className="text-xl sm:text-2xl font-bold gradient-text">
                      FrameWorks Demo
                    </h2>
                    {/* Online/Offline pill */}
                    {(() => {
                      const s = demoState
                      const map = {
                        booting: { label: 'BOOTING', cls: 'bg-tokyo-night-comment/20 text-tokyo-night-comment border-tokyo-night-comment/40' },
                        gateway_loading: { label: 'RESOLVING', cls: 'bg-tokyo-night-comment/20 text-tokyo-night-comment border-tokyo-night-comment/40' },
                        gateway_ready: { label: 'ENDPOINT READY', cls: 'bg-tokyo-night-blue/20 text-tokyo-night-blue border-tokyo-night-blue/40' },
                        gateway_error: { label: 'GATEWAY ERROR', cls: 'bg-red-500/20 text-red-400 border-red-500/40' },
                        no_endpoint: { label: 'WAITING FOR ENDPOINT', cls: 'bg-tokyo-night-comment/20 text-tokyo-night-comment border-tokyo-night-comment/40' },
                        selecting_player: { label: 'SELECTING PLAYER', cls: 'bg-tokyo-night-blue/20 text-tokyo-night-blue border-tokyo-night-blue/40' },
                        connecting: { label: 'CONNECTING', cls: 'bg-tokyo-night-blue/20 text-tokyo-night-blue border-tokyo-night-blue/40' },
                        buffering: { label: 'BUFFERING', cls: 'bg-yellow-500/20 text-yellow-400 border-yellow-500/40' },
                        playing: { label: 'STREAMING', cls: 'bg-green-500/20 text-green-400 border-green-500/40' },
                        paused: { label: 'PAUSED', cls: 'bg-tokyo-night-comment/20 text-tokyo-night-comment border-tokyo-night-comment/40' },
                        ended: { label: 'ENDED', cls: 'bg-tokyo-night-comment/20 text-tokyo-night-comment border-tokyo-night-comment/40' },
                        error: { label: 'ERROR', cls: 'bg-red-500/20 text-red-400 border-red-500/40' },
                        destroyed: { label: 'STOPPED', cls: 'bg-tokyo-night-comment/20 text-tokyo-night-comment border-tokyo-night-comment/40' }
                      }
                      const m = map[s] || map.booting
                      return <span className={`px-2 py-1 rounded-full text-xs font-medium border ${m.cls}`}>{m.label}</span>
                    })()}
                  </div>
                  <p className="text-tokyo-night-fg-dark text-sm">
                    Watch our streaming infrastructure in action.
                  </p>
                </div>

                {/* Video Player - takes up most space */}
                <div className="relative flex-1 mb-6">
                  <div className="w-full h-full rounded-xl overflow-hidden bg-tokyo-night-bg-dark shadow-2xl border border-tokyo-night-bg">
                    <div className="relative w-full h-[320px] sm:h-[380px]">
                      <FrameworksPlayer
                        contentId={config.demoStreamName}
                        contentType="live"
                        options={{ autoplay: true, muted: true, controls: false, gatewayUrl: config.gatewayUrl || undefined }}
                        onStateChange={(st) => setDemoState(st)}
                      />
                    </div>
                  </div>
                </div>
              </div>

              {/* Logo Overlay - Covers entire glass-card and dissolves to reveal player */}
              {!logoAnimationComplete && (
                <motion.div
                  className="absolute inset-0 max-w-full max-h-full flex items-center justify-center bg-tokyo-night-bg rounded-xl z-50"
                  initial={{ opacity: 1 }}
                  animate={{
                    opacity: showPlayer ? 0 : 1,
                    scale: showPlayer ? 1.05 : 1
                  }}
                  transition={{
                    duration: 2,
                    ease: [0.25, 0.46, 0.45, 0.94],
                    opacity: { duration: 2 },
                    scale: { duration: 2.2 }
                  }}
                >
                  {/* Logo Entry Animation */}
                  <motion.div
                    className="relative w-full h-full"
                    initial={{ scale: 0.8, opacity: 0, y: 20 }}
                    animate={{
                      scale: 1,
                      opacity: 1,
                      y: 0
                    }}
                    transition={{
                      duration: 0.3,
                      ease: [0.25, 0.46, 0.45, 0.94]
                    }}
                  >
                    {/* Main logo - centered vertical lockup */}
                    <div className="absolute inset-0 w-full h-full rounded-xl shadow-2xl neon-glow flex items-center justify-center overflow-hidden bg-black">
                      <img 
                        src="/frameworks-dark-vertical-lockup.svg" 
                        alt="FrameWorks" 
                        className="w-2/3 max-w-[300px] h-auto"
                      />
                    </div>
                  </motion.div>

                  {/* Glitch Effect */}
                  <div
                    className="absolute inset-0 w-full h-full rounded-xl"
                    style={{
                      overflow: 'visible',
                      transform: 'translateZ(0)',
                      willChange: 'transform'
                    }}
                  >
                    {(() => {
                      const strips = [];
                      let currentPosition = 0;

                      const viewportWidth = typeof window !== 'undefined' ? window.innerWidth : 1024;
                      let maxSafeTranslation, stripExtension;

                      if (showPlayer) {
                        maxSafeTranslation = viewportWidth < 640 ? 2 : viewportWidth < 1024 ? 3 : 5;
                        stripExtension = 0;
                      } else {
                        if (viewportWidth < 640) {
                          maxSafeTranslation = 3;
                          stripExtension = 10;
                        } else if (viewportWidth < 1024) {
                          maxSafeTranslation = 6;
                          stripExtension = 15;
                        } else {
                          maxSafeTranslation = 10;
                          stripExtension = 20;
                        }
                      }

                      for (let i = 0; i < 15; i++) {
                        const stripHeight = 20 + Math.random() * 40;
                        const rawGlitchX1 = (Math.random() - 0.5) * 40;
                        const rawGlitchX2 = (Math.random() - 0.5) * 40;
                        const glitchX1 = Math.max(-maxSafeTranslation, Math.min(maxSafeTranslation, rawGlitchX1));
                        const glitchX2 = Math.max(-maxSafeTranslation, Math.min(maxSafeTranslation, rawGlitchX2));
                        const glitchHue1 = (Math.random() - 0.5) * 90;
                        const glitchHue2 = (Math.random() - 0.5) * 90;
                        const animationDelay = i < 3 ? 0 : i < 8 ? Math.random() * 0.5 : Math.random() * 1.5;
                        const animationDuration = 2000 + Math.random() * 3000;
                        const animationName = `glitch-${(i % 6) + 5}`;

                        strips.push(
                          <div
                            key={i}
                            className={`absolute${currentPosition === 0 ? ' rounded-t-xl' : i === 14 ? ' rounded-b-xl' : ''}`}
                            style={{
                              left: `-${stripExtension}px`,
                              right: `-${stripExtension}px`,
                              top: `${currentPosition}px`,
                              height: `${stripHeight}px`,
                              backgroundImage: 'url(/frameworks-dark-vertical-lockup.svg)',
                              backgroundSize: `calc(100% - ${stripExtension * 2}px) auto`,
                              backgroundPosition: `${stripExtension}px -${currentPosition}px`,
                              backgroundRepeat: 'no-repeat',
                              overflow: currentPosition === 0 || i === 14 ? 'hidden' : 'visible',
                              '--glitch-x-1': `${glitchX1}px`,
                              '--glitch-x-2': `${glitchX2}px`,
                              '--glitch-hue-1': `${glitchHue1}deg`,
                              '--glitch-hue-2': `${glitchHue2}deg`,
                              animationName: animationName,
                              animationDuration: `${animationDuration}ms`,
                              animationDelay: `${animationDelay}s`,
                              animationIterationCount: 'infinite',
                              animationDirection: 'alternate',
                              animationTimingFunction: 'linear',
                              imageRendering: 'pixelated'
                            }}
                          />
                        );

                        currentPosition += stripHeight;
                      }

                      return strips;
                    })()}
                  </div>
                </motion.div>
              )}
            </div>
          </motion.div>

          {/* Terminal Demo */}
          {/* <motion.div
            initial={{ opacity: 0, y: 30 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.8, delay: 0.4 }}
            className="max-w-4xl mx-auto"
          >
            <div className="glow-card p-8 bg-gradient-to-br from-tokyo-night-bg-light to-tokyo-night-bg-dark">
              <div className="flex items-center gap-3 mb-6">
                <div className="w-3 h-3 bg-tokyo-night-red rounded-full"></div>
                <div className="w-3 h-3 bg-tokyo-night-yellow rounded-full"></div>
                <div className="w-3 h-3 bg-tokyo-night-green rounded-full"></div>
                <span className="text-tokyo-night-comment text-sm ml-2">FrameWorks Console</span>
              </div>
              
              {showDemo ? (
                <div className="text-left">
                  <div className="text-tokyo-night-green mb-2">$ frameworks devices scan</div>
                  <div className="text-tokyo-night-comment mb-4">
                    ✓ Found IP camera at 192.168.1.100<br/>
                    ✓ Found USB webcam /dev/video0<br/>
                    ✓ Found HDMI capture device<br/>
                    ✓ Auto-configured optimal settings
                  </div>
                  <div className="text-tokyo-night-blue mb-2">$ frameworks stream create --composite</div>
                  <div className="text-tokyo-night-comment mb-4">
                    ✓ Stream created: live-stream-abc123<br/>
                    ✓ Compositing 3 input streams (PiP layout)<br/>
                    ✓ AI processing enabled (STT, object detection)<br/>
                    ✓ Adaptive bitrate configured
                  </div>
                  <div className="text-tokyo-night-yellow mb-2">🎥 Live: 1,247 viewers • 3 sources composited</div>
                  <div className="text-tokyo-night-cyan">🤖 AI: Detected 'person', 'microphone' | STT: "Welcome to my stream!"</div>
                </div>
              ) : (
                <div className="text-tokyo-night-comment">
                  <div className="animate-pulse">Scanning for devices...</div>
                </div>
              )}
            </div>
          </motion.div> */}
        </div>
      </section>

      {/* CSS for glitch animations */}
      <style>{`
        @keyframes glitch-5 {
          0.00%, 33.33%, 43.33%, 66.67%, 76.67%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          33.43%, 43.23% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff0040);
          }
          66.77%, 76.57% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-2px 0 0 #00ffff);
          }
        }
        
        @keyframes glitch-6 {
          0.00%, 25.00%, 35.00%, 50.00%, 60.00%, 75.00%, 85.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          25.10%, 34.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(1px 0 0 #ff0040);
          }
          50.10%, 59.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-1px 0 0 #00ffff);
          }
          75.10%, 84.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff4000);
          }
        }
        
        @keyframes glitch-7 {
          0.00%, 20.00%, 30.00%, 40.00%, 50.00%, 70.00%, 80.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          20.10%, 29.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(3px 0 0 #ff0040);
          }
          40.10%, 49.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-3px 0 0 #00ffff);
          }
          70.10%, 79.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(1px 0 0 #ff4000);
          }
        }
        
        @keyframes glitch-8 {
          0.00%, 15.00%, 25.00%, 45.00%, 55.00%, 65.00%, 75.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          15.10%, 24.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff0040);
          }
          45.10%, 54.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-2px 0 0 #00ffff);
          }
          65.10%, 74.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(3px 0 0 #ff4000);
          }
        }
        
        @keyframes glitch-9 {
          0.00%, 10.00%, 20.00%, 60.00%, 70.00%, 90.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          10.10%, 19.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(1px 0 0 #ff0040);
          }
          60.10%, 69.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-1px 0 0 #00ffff);
          }
          90.10%, 99.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff4000);
          }
        }
        
        @keyframes glitch-10 {
          0.00%, 5.00%, 15.00%, 35.00%, 45.00%, 55.00%, 65.00%, 85.00%, 95.00%, 100.00% {
            transform: none;
            filter: hue-rotate(0) drop-shadow(0 0 0 transparent);
          }
          5.10%, 14.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(4px 0 0 #ff0040);
          }
          35.10%, 44.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-4px 0 0 #00ffff);
          }
          55.10%, 64.90% {
            transform: translateX(var(--glitch-x-1));
            filter: hue-rotate(var(--glitch-hue-1)) drop-shadow(2px 0 0 #ff4000);
          }
          85.10%, 94.90% {
            transform: translateX(var(--glitch-x-2));
            filter: hue-rotate(var(--glitch-hue-2)) drop-shadow(-2px 0 0 #40ff00);
          }
        }
      `}</style>

      {/* Rest of the sections remain the same */}
      {/* Unique Features Section */}
      <section className="section-padding bg-tokyo-night-bg-light/30">
        <div className="max-w-7xl mx-auto">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
            className="text-center mb-16"
          >
            <h2 className="text-4xl md:text-5xl font-bold gradient-text mb-4">
              Key Platform Features
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark max-w-3xl mx-auto">
              Advanced streaming capabilities with hybrid deployment and self-hosting freedom
            </p>
          </motion.div>

          <div className="grid md:grid-cols-2 gap-8">
            {uniqueFeatures.map((feature, index) => (
              <motion.div
                key={feature.title}
                initial={{ opacity: 0, y: 30 }}
                whileInView={{ opacity: 1, y: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.6, delay: index * 0.1 }}
                className="glow-card p-6 relative"
              >
                <div className="absolute top-4 right-4 flex items-center gap-2">
                  {(feature.title.includes('Discovery') || feature.title.includes('Compositing')) ? (
                    <>
                      <BetaBadge label="Beta" />
                      <InfoTooltip>Feature available with limits during beta; see Roadmap for scope and timeline.</InfoTooltip>
                    </>
                  ) : (
                    <span className="bg-tokyo-night-blue/20 text-tokyo-night-blue px-2 py-1 rounded text-xs font-medium">
                      {feature.badge}
                    </span>
                  )}
                </div>
                <div className={`mb-4 ${feature.color}`}>
                  {(() => {
                    const Icon = feature.icon;
                    return <Icon className="w-10 h-10" />;
                  })()}
                </div>
                <h3 className="text-xl font-bold text-tokyo-night-fg mb-3">
                  {feature.title}
                </h3>
                <p className="text-tokyo-night-fg-dark">
                  {feature.description}
                </p>
              </motion.div>
            ))}
          </div>
        </div>
      </section>

      {/* Pricing Preview */}
      <section className="section-padding">
        <div className="max-w-7xl mx-auto">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
            className="text-center mb-16"
          >
            <h2 className="text-4xl md:text-5xl font-bold gradient-text mb-6">
              Transparent Pricing
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark max-w-3xl mx-auto">
              Start free with full self-hosting. Upgrade for GPU features, hosted services, and enterprise support.
            </p>
          </motion.div>

          <div className="max-w-4xl mx-auto">
            <div className="grid md:grid-cols-2 gap-8 items-stretch">
              {/* Free Tier */}
              <motion.div
                initial={{ opacity: 0, y: 30 }}
                whileInView={{ opacity: 1, y: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.6 }}
                className="glow-card p-8 text-center flex flex-col"
              >
                <h3 className="text-3xl font-bold text-tokyo-night-fg mb-2">Backed by Livepeer</h3>
                <div className="mb-4">
                  <span className="text-5xl font-bold gradient-text">Free</span>
                </div>
                <p className="text-tokyo-night-fg-dark mb-6">
                  Complete self-hosting stack with shared pool access. Open source with permissive licenses - you own it, run it cheap.
                </p>

                <ul className="space-y-3 mb-8 text-left flex-grow">
                  {freeTierFeatures.map((feature, featureIndex) => (
                    <li key={featureIndex} className="flex items-start gap-3">
                      <div className="w-2 h-2 bg-tokyo-night-green rounded-full mt-2 flex-shrink-0"></div>
                      <span className="text-tokyo-night-fg-dark">{feature}</span>
                    </li>
                  ))}
                </ul>

                <div className="mt-auto">
                  <a
                    href={config.appUrl}
                    className="btn-primary w-full flex items-center justify-center mb-4 whitespace-nowrap"
                  >
                    Start Free Today
                    <ArrowTopRightOnSquareIcon className="w-4 h-4 ml-2 flex-shrink-0" />
                  </a>
                  <p className="text-tokyo-night-comment text-sm">
                    No credit card required • Deploy in minutes
                  </p>
                </div>
              </motion.div>

              {/* Paid Plans */}
              <motion.div
                initial={{ opacity: 0, y: 30 }}
                whileInView={{ opacity: 1, y: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.6, delay: 0.2 }}
                className="glow-card p-8 text-center flex flex-col"
              >
                <h3 className="text-3xl font-bold text-tokyo-night-fg mb-2">Paid Plans</h3>
                <div className="mb-4">
                  <span className="text-5xl font-bold gradient-text">€50+</span>
                  <span className="text-tokyo-night-comment ml-2">/month</span>
                </div>
                <p className="text-tokyo-night-fg-dark mb-6">
                  GPU-intensive features like AI processing and multi-stream compositing, plus hosted services and enterprise support.
                </p>

                <ul className="space-y-3 mb-8 text-left flex-grow">
                  <li className="flex items-start gap-3">
                    <div className="w-2 h-2 bg-tokyo-night-blue rounded-full mt-2 flex-shrink-0"></div>
                    <span className="text-tokyo-night-fg-dark">Custom subdomains and hosted load balancers</span>
                  </li>
                  <li className="flex items-start gap-3">
                    <div className="w-2 h-2 bg-tokyo-night-blue rounded-full mt-2 flex-shrink-0"></div>
                    <span className="text-tokyo-night-fg-dark">GPU features and reserved bandwidth pool</span>
                  </li>
                  <li className="flex items-start gap-3">
                    <div className="w-2 h-2 bg-tokyo-night-blue rounded-full mt-2 flex-shrink-0"></div>
                    <span className="text-tokyo-night-fg-dark">Team collaboration and enterprise features</span>
                  </li>
                  <li className="flex items-start gap-3">
                    <div className="w-2 h-2 bg-tokyo-night-blue rounded-full mt-2 flex-shrink-0"></div>
                    <span className="text-tokyo-night-fg-dark">Email and 24/7 support options</span>
                  </li>
                </ul>

                <div className="mt-auto">
                  <Link
                    to="/pricing"
                    className="btn-secondary w-full block mb-4"
                  >
                    View All Plans
                  </Link>
                  <p className="text-tokyo-night-comment text-sm">
                    Supporter • Developer • Production • Enterprise
                  </p>
                </div>
              </motion.div>
            </div>
          </div>
        </div>
      </section>

      {/* Hybrid Deployment */}
      <section className="section-padding bg-tokyo-night-bg-light/30">
        <div className="max-w-7xl mx-auto">
          <div className="grid md:grid-cols-2 gap-12 items-center">
            <motion.div
              initial={{ opacity: 0, x: -30 }}
              whileInView={{ opacity: 1, x: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6 }}
            >
              <h2 className="text-3xl md:text-4xl font-bold gradient-text mb-6">
                Hybrid: Cloud + Self-Hosted
              </h2>
              <p className="text-lg text-tokyo-night-fg-dark mb-6">
                Why choose between cloud and self-hosted? Get the best of both worlds with our hybrid approach.
              </p>
              <div className="space-y-4">
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-blue rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">One Console for Everything</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">Manage your complete video infrastructure from a single dashboard - self-hosted nodes, hosted processing, and hybrid deployments all in one place.</p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-green rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">Seamless Failover</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">Automatic failover between your nodes and our hosted infrastructure for maximum reliability.</p>
                  </div>
                </div>
                <div className="flex items-start gap-3">
                  <div className="w-2 h-2 bg-tokyo-night-yellow rounded-full mt-3 flex-shrink-0"></div>
                  <div>
                    <h4 className="font-semibold text-tokyo-night-fg mb-1">Cost Optimization</h4>
                    <p className="text-tokyo-night-fg-dark text-sm">Use our free tier for overflow traffic and your own nodes for base load.</p>
                  </div>
                </div>
              </div>
            </motion.div>

            <motion.div
              initial={{ opacity: 0, x: 30 }}
              whileInView={{ opacity: 1, x: 0 }}
              viewport={{ once: true }}
              transition={{ duration: 0.6, delay: 0.2 }}
              className="glow-card p-8"
            >
              <h3 className="text-2xl font-bold text-tokyo-night-fg mb-6">Deployment Options</h3>
              <div className="space-y-6">
                <div className="border-l-4 border-tokyo-night-blue pl-4">
                  <h4 className="font-semibold text-tokyo-night-fg mb-2">🌐 Fully Hosted</h4>
                  <p className="text-tokyo-night-fg-dark text-sm">Use our global infrastructure with generous free tier</p>
                </div>
                <div className="border-l-4 border-tokyo-night-green pl-4">
                  <h4 className="font-semibold text-tokyo-night-fg mb-2">🏠 Self-Hosted</h4>
                  <p className="text-tokyo-night-fg-dark text-sm">Deploy on your own infrastructure and manage everything through one web console</p>
                </div>
                <div className="border-l-4 border-tokyo-night-yellow pl-4">
                  <h4 className="font-semibold text-tokyo-night-fg mb-2">🔄 Hybrid</h4>
                  <p className="text-tokyo-night-fg-dark text-sm">Combine both for optimal cost and performance</p>
                </div>
              </div>
            </motion.div>
          </div>
        </div>
      </section>

      {/* CTA Section */}
      <section className="section-padding bg-gradient-to-br from-tokyo-night-bg-dark to-tokyo-night-bg">
        <div className="max-w-7xl mx-auto text-center">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <h2 className="text-4xl md:text-5xl font-bold gradient-text mb-6">
              Ready to Transform Your Streaming?
            </h2>
            <p className="text-xl text-tokyo-night-fg-dark max-w-2xl mx-auto mb-8">
              Take complete control of your streaming infrastructure.
            </p>
            <div className="flex flex-col sm:flex-row gap-4 justify-center">
              <a href={config.appUrl} className="btn-primary flex items-center justify-center whitespace-nowrap">
                Start Free Today
                <ArrowTopRightOnSquareIcon className="w-4 h-4 ml-2 flex-shrink-0" />
              </a>
              <Link to="/contact" className="btn-secondary">
                Talk to Sales
              </Link>
              <a
                href={config.githubUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="btn-secondary flex items-center whitespace-nowrap"
              >
                View Open Source
                <ArrowTopRightOnSquareIcon className="w-4 h-4 ml-2 flex-shrink-0" />
              </a>
            </div>
            <p className="text-tokyo-night-comment text-sm mt-6">
              No credit card required • Full feature access • Deploy in minutes
            </p>
          </motion.div>
        </div>
      </section>
    </div>
  )
}

export default LandingPage 
