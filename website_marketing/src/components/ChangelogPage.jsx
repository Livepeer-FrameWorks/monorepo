import React, { useState, useEffect } from 'react'
import { motion } from 'framer-motion'
import MarkdownView from './MarkdownView'

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
      <section className="section-padding bg-gradient-to-br from-tokyo-night-bg via-tokyo-night-bg-light to-tokyo-night-bg">
        <div className="max-w-7xl mx-auto text-center">
          <motion.div initial={{ opacity: 0, y: 30 }} animate={{ opacity: 1, y: 0 }} transition={{ duration: 0.6 }}>
            <h1 className="text-4xl md:text-6xl font-bold gradient-text mb-6">Changelog</h1>
            <p className="text-xl text-tokyo-night-fg-dark max-w-3xl mx-auto">Recent updates and releases</p>
          </motion.div>
        </div>
      </section>
      <section className="section-padding">
        <div className="max-w-4xl mx-auto">
          <div className="glow-card p-6">
            {loading ? (
              <div className="text-center py-8">
                <div className="animate-pulse text-tokyo-night-comment">Loading changelog...</div>
              </div>
            ) : (
              <MarkdownView markdown={changelogMd} />
            )}
          </div>
        </div>
      </section>
    </div>
  )
}

export default ChangelogPage

