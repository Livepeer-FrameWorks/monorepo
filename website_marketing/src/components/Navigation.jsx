import { Link, useLocation } from 'react-router-dom'
import { useEffect, useState } from 'react'
import config from '../config'
import { ArrowTopRightOnSquareIcon } from '@heroicons/react/24/outline'
import BetaBadge from './shared/BetaBadge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { Sheet, SheetTrigger, SheetContent, SheetClose, SheetTitle } from '@/components/ui/sheet'

const Navigation = () => {
  const location = useLocation()
  const [isMenuOpen, setIsMenuOpen] = useState(false)

  const linkClasses = (path, variant = 'desktop') => {
    const isActive = Boolean(path && location.pathname === path)

    return cn(
      'text-sm font-medium transition-colors duration-200',
      variant === 'desktop' ? 'nav-link inline-flex items-center' : 'nav-mobile-link',
      isActive
        ? variant === 'desktop'
          ? 'nav-link-active text-accent'
          : 'nav-mobile-link-active'
        : 'text-muted-foreground hover:text-accent'
    )
  }

  useEffect(() => {
    setIsMenuOpen(false)
  }, [location.pathname])

  return (
    <nav className="nav-surface fixed top-0 left-0 right-0 z-50">
      <div className="max-w-7xl mx-auto px-4 sm:px-6 lg:px-8">
        <div className="flex justify-between items-center h-16">
          {/* Logo */}
          <div className="flex items-center gap-3">
            <a href="/" className="flex items-center">
              <img src="/frameworks-dark-horizontal-lockup-transparent.svg" alt={config.companyName} className="h-10" />
            </a>
            <BetaBadge />
          </div>

          {/* Desktop Navigation */}
          <div className="hidden lg:flex items-center space-x-8">
            <Link to="/" className={linkClasses('/')}>
              Home
            </Link>
            <Link to="/about" className={linkClasses('/about')}>
              About
            </Link>
            <Link to="/pricing" className={linkClasses('/pricing')}>
              Pricing
            </Link>
            <Link to="/docs" className={linkClasses('/docs')}>
              Docs
            </Link>
            <Link to="/contact" className={linkClasses('/contact')}>
              Contact
            </Link>
            <a
              href={config.githubUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="group nav-link inline-flex items-center gap-1 text-sm font-medium text-muted-foreground transition-colors duration-200 hover:text-accent"
            >
              Code
              <ArrowTopRightOnSquareIcon className="w-3 h-3 ml-1 shrink-0 transition-transform duration-200 group-hover:translate-x-1 group-hover:-translate-y-1 group-focus-visible:translate-x-1 group-focus-visible:-translate-y-1" />
            </a>
            <Button asChild className="cta-button">
              <a href={config.appUrl} className="flex items-center gap-2">
                Try Now
                <ArrowTopRightOnSquareIcon className="w-4 h-4 cta-button__icon" />
              </a>
            </Button>
          </div>

          <Sheet open={isMenuOpen} onOpenChange={setIsMenuOpen}>
            <SheetTrigger asChild>
              <button
                className="nav-trigger p-2 lg:hidden"
                aria-label={isMenuOpen ? 'Close navigation' : 'Open navigation'}
                aria-expanded={isMenuOpen}
                aria-controls="mobile-nav-panel"
              >
                <svg className="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                  {isMenuOpen ? (
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
                  ) : (
                    <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 6h16M4 12h16M4 18h16" />
                  )}
                </svg>
              </button>
            </SheetTrigger>
            <SheetContent
              side="top"
              id="mobile-nav-panel"
              className="nav-mobile-surface px-4 pb-8 pt-6 shadow-lg lg:hidden"
            >
              <SheetTitle className="sr-only">Primary navigation</SheetTitle>
              <div className="flex flex-col gap-3 text-left">
                <SheetClose asChild>
                  <Link to="/" className={linkClasses('/', 'mobile')}>Home</Link>
                </SheetClose>
                <SheetClose asChild>
                  <Link to="/about" className={linkClasses('/about', 'mobile')}>About</Link>
                </SheetClose>
                <SheetClose asChild>
                  <Link to="/pricing" className={linkClasses('/pricing', 'mobile')}>Pricing</Link>
                </SheetClose>
                <SheetClose asChild>
                  <Link to="/docs" className={linkClasses('/docs', 'mobile')}>Documentation</Link>
                </SheetClose>
                <SheetClose asChild>
                  <Link to="/contact" className={linkClasses('/contact', 'mobile')}>Contact</Link>
                </SheetClose>

                <div className="nav-divider mt-3 pt-3 border-t">
                  <p className="nav-group-label mb-2">Resources</p>
                  <div className="flex flex-col gap-3">
                    <SheetClose asChild>
                      <Link to="/status" className={linkClasses('/status', 'mobile')}>Status</Link>
                    </SheetClose>
                    <SheetClose asChild>
                      <Link to="/roadmap" className={linkClasses('/roadmap', 'mobile')}>Roadmap</Link>
                    </SheetClose>
                  </div>
                </div>

                <SheetClose asChild>
                  <a
                    href={config.githubUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className={cn(
                      'group w-full gap-2',
                      linkClasses(null, 'mobile')
                    )}
                  >
                    GitHub
                    <ArrowTopRightOnSquareIcon className="w-3 h-3 shrink-0 transition-transform duration-200 group-hover:translate-x-1 group-hover:-translate-y-1 group-focus-visible:translate-x-1 group-focus-visible:-translate-y-1" />
                  </a>
                </SheetClose>

                <SheetClose asChild>
                  <Button asChild className="cta-button w-full justify-center mt-2">
                    <a href={config.appUrl} className="flex items-center gap-2">
                      Try Now
                      <ArrowTopRightOnSquareIcon className="w-4 h-4 cta-button__icon" />
                    </a>
                  </Button>
                </SheetClose>
              </div>
            </SheetContent>
          </Sheet>
        </div>
      </div>
    </nav>
  )
}

export default Navigation 
