import { BrowserRouter as Router, Routes, Route } from 'react-router-dom'
import Navigation from './components/Navigation'
import LandingPage from './components/LandingPage'
import Documentation from './components/Documentation'
import Pricing from './components/Pricing'
import About from './components/About'
import Contact from './components/Contact'
import ScrollToTop from './components/ScrollToTop'
import Footer from './components/Footer'
import StatusPage from './components/StatusPage'
import RoadmapPage from './components/RoadmapPage'
import ChangelogPage from './components/ChangelogPage'
import PrivacyPage from './components/PrivacyPage'
import TermsPage from './components/TermsPage'
import AupPage from './components/AupPage'

function App() {
  return (
    <Router>
      <ScrollToTop />
      <div className="App">
        <Navigation />
        <Routes>
          <Route path="/" element={<LandingPage />} />
          <Route path="/docs" element={<Documentation />} />
          <Route path="/pricing" element={<Pricing />} />
          <Route path="/about" element={<About />} />
          <Route path="/contact" element={<Contact />} />
          <Route path="/status" element={<StatusPage />} />
          <Route path="/roadmap" element={<RoadmapPage />} />
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
