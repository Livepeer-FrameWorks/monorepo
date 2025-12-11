import { BrowserRouter as Router, Routes, Route } from 'react-router-dom'
import Navigation from './components/Navigation'
import LandingPage from './components/pages/LandingPage'
import Pricing from './components/pages/Pricing'
import About from './components/pages/About'
import Contact from './components/pages/Contact'
import ScrollToTop from './components/shared/ScrollToTop'
import Footer from './components/Footer'
import StatusPage from './components/pages/StatusPage'
import ChangelogPage from './components/pages/ChangelogPage'
import PrivacyPage from './components/pages/PrivacyPage'
import TermsPage from './components/pages/TermsPage'
import AupPage from './components/pages/AupPage'

function App() {
  return (
    <Router>
      <ScrollToTop />
      <div className="App">
        <Navigation />
        <Routes>
          <Route path="/" element={<LandingPage />} />
          <Route path="/pricing" element={<Pricing />} />
          <Route path="/about" element={<About />} />
          <Route path="/contact" element={<Contact />} />
          <Route path="/status" element={<StatusPage />} />
          <Route path="/changelog" element={<ChangelogPage />} />
          <Route path="/privacy" element={<PrivacyPage />} />
          <Route path="/terms" element={<TermsPage />} />
          <Route path="/aup" element={<AupPage />} />
        </Routes>
      </div>
      <Footer />
    </Router>
  )
}

export default App
