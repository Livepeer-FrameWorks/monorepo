import React from 'react'
import { Link } from 'react-router-dom'

const Footer = () => {
  return (
    <footer className="mt-16 border-t border-tokyo-night-fg-gutter bg-tokyo-night-bg/70">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8 py-8">
        <div className="grid grid-cols-1 md:grid-cols-4 gap-8 mb-8">
          {/* Company */}
          <div>
            <h3 className="text-sm font-semibold text-tokyo-night-fg mb-3">Company</h3>
            <div className="flex flex-col gap-2">
              <Link to="/about" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">About</Link>
              <Link to="/contact" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">Contact</Link>
            </div>
          </div>
          
          {/* Resources */}
          <div>
            <h3 className="text-sm font-semibold text-tokyo-night-fg mb-3">Resources</h3>
            <div className="flex flex-col gap-2">
              <Link to="/docs" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">Documentation</Link>
              <Link to="/status" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">Status</Link>
              <Link to="/roadmap" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">Roadmap</Link>
              {/* <Link to="/changelog" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">Changelog</Link> */}
            </div>
          </div>
          
          {/* Product */}
          <div>
            <h3 className="text-sm font-semibold text-tokyo-night-fg mb-3">Product</h3>
            <div className="flex flex-col gap-2">
              <Link to="/pricing" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">Pricing</Link>
              <a href="https://github.com/livepeer-frameworks/monorepo" target="_blank" rel="noopener noreferrer" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">GitHub</a>
            </div>
          </div>
          
          {/* Legal */}
          <div>
            <h3 className="text-sm font-semibold text-tokyo-night-fg mb-3">Legal</h3>
            <div className="flex flex-col gap-2">
              <Link to="/privacy" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">Privacy</Link>
              <Link to="/terms" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">Terms</Link>
              <Link to="/aup" className="text-sm text-tokyo-night-fg-dark hover:text-tokyo-night-fg">AUP</Link>
            </div>
          </div>
        </div>
        
        <div className="pt-8 border-t border-tokyo-night-fg-gutter flex flex-col md:flex-row items-center justify-between gap-4">
          <div className="text-sm text-tokyo-night-comment">© {new Date().getFullYear()} FrameWorks</div>
        </div>
      </div>
    </footer>
  )
}

export default Footer
