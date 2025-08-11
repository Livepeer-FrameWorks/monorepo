import { Link, useLocation } from 'react-router-dom'
import { useState } from 'react'
import config from '../config'
import ExternalLinkIcon from './ExternalLinkIcon'

const Navigation = () => {
  const location = useLocation()
  const [isMenuOpen, setIsMenuOpen] = useState(false)

  const isActive = (path) => location.pathname === path

  return (
    <nav className="fixed top-0 left-0 right-0 z-50 bg-tokyo-night-bg/80 backdrop-blur-md border-b border-tokyo-night-fg-gutter">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div className="flex justify-between items-center h-16">
          {/* Logo */}
          <div className="flex items-center space-x-2">
            <a href="/" className="flex items-center space-x-3">
              <img src="/icon.png" alt={config.companyName} className="h-12 w-12 rounded-lg logo-gradient" />
              <span className="text-xl font-bold gradient-text">{config.companyName}</span>
            </a>
          </div>

          {/* Desktop Navigation */}
          <div className="hidden md:flex items-center space-x-8">
            <Link
              to="/"
              className={`text-sm font-medium transition-colors duration-200 ${isActive('/') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                }`}
            >
              Home
            </Link>
            <Link
              to="/about"
              className={`text-sm font-medium transition-colors duration-200 ${isActive('/about') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                }`}
            >
              About
            </Link>
            <Link
              to="/pricing"
              className={`text-sm font-medium transition-colors duration-200 ${isActive('/pricing') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                }`}
            >
              Pricing
            </Link>
            <Link
              to="/docs"
              className={`text-sm font-medium transition-colors duration-200 ${isActive('/docs') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                }`}
            >
              Docs
            </Link>
            <Link
              to="/contact"
              className={`text-sm font-medium transition-colors duration-200 ${isActive('/contact') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                }`}
            >
              Contact
            </Link>
            <a
              href={config.githubUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="text-sm font-medium text-tokyo-night-fg-dark hover:text-tokyo-night-fg transition-colors duration-200 flex items-center"
            >
              Code
              <ExternalLinkIcon className="w-3 h-3 ml-1" />
            </a>
            <a
              href={config.appUrl}
              className="btn-primary flex items-center"
            >
              Try Now
              <ExternalLinkIcon className="w-4 h-4 ml-2" />
            </a>
          </div>

          {/* Mobile menu button */}
          <button
            className="md:hidden p-2 rounded-lg hover:bg-tokyo-night-bg-light transition-colors duration-200"
            onClick={() => setIsMenuOpen(!isMenuOpen)}
          >
            <svg className="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              {isMenuOpen ? (
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
              ) : (
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 6h16M4 12h16M4 18h16" />
              )}
            </svg>
          </button>
        </div>

        {/* Mobile Navigation */}
        {isMenuOpen && (
          <div className="md:hidden py-4 border-t border-tokyo-night-fg-gutter">
            <div className="flex flex-col space-y-4">
              <Link
                to="/"
                className={`text-sm font-medium transition-colors duration-200 ${isActive('/') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                  }`}
                onClick={() => setIsMenuOpen(false)}
              >
                Home
              </Link>
              <Link
                to="/docs"
                className={`text-sm font-medium transition-colors duration-200 ${isActive('/docs') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                  }`}
                onClick={() => setIsMenuOpen(false)}
              >
                Documentation
              </Link>
              <Link
                to="/pricing"
                className={`text-sm font-medium transition-colors duration-200 ${isActive('/pricing') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                  }`}
                onClick={() => setIsMenuOpen(false)}
              >
                Pricing
              </Link>
              <Link
                to="/about"
                className={`text-sm font-medium transition-colors duration-200 ${isActive('/about') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                  }`}
                onClick={() => setIsMenuOpen(false)}
              >
                About
              </Link>
              <Link
                to="/contact"
                className={`text-sm font-medium transition-colors duration-200 ${isActive('/contact') ? 'text-tokyo-night-blue' : 'text-tokyo-night-fg-dark hover:text-tokyo-night-fg'
                  }`}
                onClick={() => setIsMenuOpen(false)}
              >
                Contact
              </Link>
              <a
                href={config.githubUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="text-sm font-medium text-tokyo-night-fg-dark hover:text-tokyo-night-fg transition-colors duration-200 flex items-center"
              >
                GitHub
                <ExternalLinkIcon className="w-3 h-3 ml-1" />
              </a>
              <a
                href={config.appUrl}
                className="btn-primary inline-flex items-center justify-center"
              >
                Try Now
                <ExternalLinkIcon className="w-4 h-4 ml-2" />
              </a>
            </div>
          </div>
        )}
      </div>
    </nav>
  )
}

export default Navigation 