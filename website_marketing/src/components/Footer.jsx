import { Link } from 'react-router-dom'

const Footer = () => {
  return (
    <footer className="mt-0 border-t border-border/60 bg-background/80">
      <div className="max-w-7xl mx-auto px-4 py-8 sm:px-6 lg:px-8">
        <div className="mb-8 grid grid-cols-1 gap-8 md:grid-cols-4">
          {/* Company */}
          <div>
            <h3 className="mb-3 text-sm font-semibold text-foreground">Company</h3>
            <div className="flex flex-col gap-2">
              <Link to="/about" className="text-sm text-muted-foreground transition-colors hover:text-foreground">About</Link>
              <Link to="/contact" className="text-sm text-muted-foreground transition-colors hover:text-foreground">Contact</Link>
            </div>
          </div>
          
          {/* Resources */}
          <div>
            <h3 className="mb-3 text-sm font-semibold text-foreground">Resources</h3>
            <div className="flex flex-col gap-2">
              <Link to="/docs" className="text-sm text-muted-foreground transition-colors hover:text-foreground">Documentation</Link>
              <Link to="/status" className="text-sm text-muted-foreground transition-colors hover:text-foreground">Status</Link>
              <Link to="/roadmap" className="text-sm text-muted-foreground transition-colors hover:text-foreground">Roadmap</Link>
              {/* <Link to="/changelog" className="text-sm text-muted-foreground transition-colors hover:text-foreground">Changelog</Link> */}
            </div>
          </div>
          
          {/* Product */}
          <div>
            <h3 className="mb-3 text-sm font-semibold text-foreground">Product</h3>
            <div className="flex flex-col gap-2">
              <Link to="/pricing" className="text-sm text-muted-foreground transition-colors hover:text-foreground">Pricing</Link>
              <a href="https://github.com/livepeer-frameworks/monorepo" target="_blank" rel="noopener noreferrer" className="text-sm text-muted-foreground transition-colors hover:text-foreground">GitHub</a>
            </div>
          </div>
          
          {/* Legal */}
          <div>
            <h3 className="mb-3 text-sm font-semibold text-foreground">Legal</h3>
            <div className="flex flex-col gap-2">
              <Link to="/privacy" className="text-sm text-muted-foreground transition-colors hover:text-foreground">Privacy</Link>
              <Link to="/terms" className="text-sm text-muted-foreground transition-colors hover:text-foreground">Terms</Link>
              <Link to="/aup" className="text-sm text-muted-foreground transition-colors hover:text-foreground">AUP</Link>
            </div>
          </div>
        </div>
        
        <div className="flex flex-col items-center justify-between gap-4 border-t border-border/60 pt-8 md:flex-row">
          <div className="text-sm text-muted-foreground">© {new Date().getFullYear()} FrameWorks</div>
        </div>
      </div>
    </footer>
  )
}

export default Footer
