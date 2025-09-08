import React, { useState, useEffect } from 'react'
import { motion } from 'framer-motion'
import BetaBadge from './BetaBadge'
import MarkdownView from './MarkdownView'

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
      <section className="section-padding bg-gradient-to-br from-tokyo-night-bg via-tokyo-night-bg-light to-tokyo-night-bg">
        <div className="max-w-7xl mx-auto text-center">
          <motion.div initial={{ opacity: 0, y: 30 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.6 }}>
            <div className="mb-3 flex items-center justify-center gap-2">
              <BetaBadge />
              <span className="text-xs text-tokyo-night-comment">Public Beta — features marked accordingly</span>
            </div>
            <h1 className="text-4xl md:text-6xl font-bold gradient-text mb-6">Roadmap</h1>
            <p className="text-xl text-tokyo-night-fg-dark max-w-3xl mx-auto">High‑level roadmap from our repository docs</p>
          </motion.div>
        </div>
      </section>
      <section className="section-padding">
        <div className="max-w-4xl mx-auto">
          <div className="glow-card p-6">
            {loading ? (
              <div className="text-center py-8">
                <div className="animate-pulse text-tokyo-night-comment">Loading roadmap...</div>
              </div>
            ) : (
              <MarkdownView markdown={roadmapMd} />
            )}
          </div>
        </div>
      </section>
    </div>
  )
}

export default RoadmapPage

