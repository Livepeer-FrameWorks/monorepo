# FrameWorks Marketing Website

The marketing website for FrameWorks - a React-based site showcasing the streaming platform's features, pricing, and documentation.

## 🚀 Quick Start

### Prerequisites

- Node.js 18+ 
- npm or yarn

### Setup

1. **Clone and navigate to the marketing website directory:**
   ```bash
   cd monorepo/website_marketing
   ```

2. **Install dependencies:**
   ```bash
   npm install
   ```

3. **Configure environment variables:**
   ```bash
   cp env.example .env
   ```
   
   Edit `.env` with your specific configuration:
   - `VITE_APP_URL`: URL of the main FrameWorks application
   - `VITE_CONTACT_API_URL`: URL of the contact forms API
   - `VITE_DEMO_STREAM_NAME`: Stream name for the live demo player

4. **Start the development server:**
   ```bash
   npm run dev
   ```

5. **Open your browser:**
   Navigate to `http://localhost:9004`

## 🛠️ Development

### Available Scripts

- `npm run dev` - Start development server (port 9004)
- `npm run build` - Build for production
- `npm run preview` - Preview production build
- `npm run lint` - Run ESLint

### Project Structure

```
src/
├── components/           # React components
│   ├── About.jsx        # About page
│   ├── Contact.jsx      # Contact form with anti-spam
│   ├── Documentation.jsx # API documentation
│   ├── LandingPage.jsx  # Home page with live demo
│   ├── Navigation.jsx   # Header navigation
│   └── Pricing.jsx      # Pricing tiers
├── config.js            # Environment configuration
├── App.jsx              # Main app component
└── main.jsx             # Entry point
```

### Key Features

- **Live Demo Player**: Integrated MistPlayer showing real streaming
- **Anti-Spam Contact Form**: Level 3™️ bot protection with behavioral tracking
- **Responsive Design**: Mobile-first with Tailwind CSS
- **Tokyo Night Theme**: Custom dark theme with gradient accents
- **Framer Motion**: Smooth animations and transitions
- **SEO Optimized**: Meta tags and semantic HTML

## 🎨 Styling

The site uses a custom "Tokyo Night" theme built with Tailwind CSS:

- **Colors**: Dark background with blue, green, yellow, purple accents
- **Typography**: Gradient text effects for headings
- **Components**: Glass cards with glow effects
- **Animations**: Smooth transitions and hover effects

## 🔧 Configuration

All configuration is handled through environment variables defined in `config.js`:

```javascript
const config = {
  appUrl: import.meta.env.VITE_APP_URL || 'http://localhost:9090',
  apiUrl: import.meta.env.VITE_API_URL || 'http://localhost:9000',
  contactApiUrl: import.meta.env.VITE_CONTACT_API_URL || 'http://localhost:18032',
  // ... other config options
}
```

## 📱 Pages

### Landing Page (`/`)
- Hero section with live streaming demo
- Feature highlights and unique selling points
- Pricing preview with free tier emphasis
- Hybrid deployment explanation

### About (`/about`)
- Mission and team information
- Technology stack and differentiators
- Development timeline and roadmap
- MistServer and Livepeer partnership details

### Pricing (`/pricing`)
- Detailed tier breakdown (Free, Supporter, Developer, Production, Enterprise)
- GPU feature comparison
- Self-hosted vs hosted deployment options
- FAQ section

### Contact (`/contact`)
- Multi-channel contact options (Email, Discord, Forum, GitHub)
- Custom contact form with anti-spam protection
- Common questions and answers

### Documentation (`/docs`)
- Getting started guide
- API reference
- Architecture overview
- Code examples and deployment instructions

## 🚢 Deployment

### Docker Deployment

The marketing website includes a multi-stage Dockerfile:

```bash
# Build the Docker image
docker build -t frameworks-marketing .

# Run the container
docker run -p 80:80 frameworks-marketing
```

### Environment Variables for Production

Set these environment variables for production deployment:

```bash
VITE_APP_URL=https://app.frameworks.network
VITE_CONTACT_API_URL=https://contact.frameworks.network
VITE_CONTACT_EMAIL=info@frameworks.network
VITE_DOMAIN=frameworks.network
```
