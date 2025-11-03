# FrameWorks Marketing Website

The marketing website for FrameWorks - a React-based site showcasing the streaming platform's features, pricing, and documentation.

## Quick Start

### Prerequisites

- Node.js 18+ 
- npm

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
   Edit `.env` as needed. See `env.example` comments for details. Do not commit secrets.

4. **Start the development server:**
   ```bash
   npm run dev
   ```

5. **Open your browser:**
   Navigate to `http://localhost:9004`

## Development

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
- **Anti-spam Contact Form**: Behavioral checks, Cloudflare Turnstile, and validation
- **Responsive Design**: Mobile-first with Tailwind CSS
- **Tokyo Night Theme**: Custom dark theme with gradient accents
- **Framer Motion**: Smooth animations and transitions
- **SEO Optimized**: Meta tags and semantic HTML

## Styling

The site uses a custom "Tokyo Night" theme built with Tailwind CSS:

- **Colors**: Dark background with blue, green, yellow, purple accents
- **Typography**: Gradient text effects for headings
- **Components**: Glass cards with glow effects
- **Animations**: Smooth transitions and hover effects

## Brand System

Our visual language is anchored in the Tokyo Night palette, with a focus on “solid as a rock” slabs accented by subtle beams and seams.

- **Core palette**: `--background` (`#1a1b26`), `--accent` (`#7aa2f7`), secondary highlights (`#9ece6a`, `#e0af68`, `#bb9af7`).
- **Surfaces**: `MarketingBand` exposes surface presets—
  - `slate`: soft gradient panels for general content.
  - `midnight`: deeper contrast for immersive sections.
  - `mesh`: grid texture for technical callouts.
  - `beam`: diagonal beams evoking metal braces and infrastructure strength.
- **Outline treatments**: `MarketingOutline` restores the legacy “No Surprise Bills” badge aesthetic without nested cards.
- **Seams**: seam utilities rely on the shared `--accent-rgb` token, keeping dividers consistent across grids and stacks.
- **Motion**: use the optional `.cta-motion` utility on buttons to add a shared hover glow without bespoke animations.

## Component Library

All primitives live in `src/components/MarketingElements.jsx` with matching styles in `src/index.css`.

### Layout Wrappers

| Component | Purpose | Notes |
|-----------|---------|-------|
| `MarketingBand` | Neutral background section with opt-in surfaces & padding presets (`none`, `compact`, `balanced`, `spacious`). | `surface="panel"` recreates the heavy slab; `flush` removes radius/gaps. |
| `MarketingOutline` | Outline-only slab with corner or centered labels. | Combine with `MarketingOutlineCluster` for multi-card outlines. |
| `MarketingGridSeam` | Seam-driven grid for equal columns with token-based dividers. | `columns`, `stackAt`, and `gap` control responsiveness. |
| `MarketingGridSplit` | Two-column layout with optional reversal/stack breakpoints. | Ideal for hero+media or copy/visual splits. |
| `MarketingOutlineCluster` | Grid of outlined cards with optional header via `HeadlineStack`. | Supports `items` arrays or custom children. |
| `MarketingStackedSeam` | Vertical list with horizontal seams (roadmaps, FAQs). | Works with raw children or `items` arrays. |
| `MarketingHero` | Hero container with optional media slot (`media`, `mediaPosition`, `layout`). | `surface` toggles preset backgrounds (`none`, `gradient`, `beams`, `spotlight`); `accents` places per-page beams/spots via percentage coords; `support`/`footnote` inject copy above/below CTAs. `mediaSurface="none"` keeps embeds flush. |
| `MarketingFeatureWall` | Flush feature wall with brand seams and icon badges. | Defaults to the legacy wall styling; use `variant="grid"` or `flush` for equal-width grids. |
| `MarketingPartnerSurface` | Partner/endorsement shelf with outline-ready cards. | `variant="flush"` renders legacy seam grid. |
| `TimelineBand` | Neutral timeline wrapper that hosts `MarketingTimeline` or custom children. | `variant="plain"` drops the band background. |
| `SectionDivider` | Reusable section divider with angled “shelf” option. | Use between bands when gradients don’t change. |

### Content Modules

| Component | Usage |
|-----------|-------|
| `HeadlineStack` | Eyebrow/title/subtitle stack with optional underline + action slot. | `actionsPlacement="inline"` keeps CTAs on the baseline. |
| `CTACluster` & `CTAFootnote` | Primary/secondary button groups and legal footers. |
| `MetricStack` & `StatBadge` | Key metrics/credibility stats with tone-aware badges. | `variant="plain"` removes the card treatment. |
| `IconList` | Feature bullets with shared gradient icon plate. | `variant="plain"` matches the mission pillar list. |
| `TestimonialTile` | Quote card with avatar, author meta, and optional extras. |
| `ComparisonTable` | Token-driven comparison table for feature matrices (sets `--comparison-cols`). |
| `MarketingFeatureCard` | Icon-forward feature tile used inside `MarketingFeatureWall` or standalone. | Supports `flush`, `stripe`, `hover="subtle"`, `metaAlign`. |
| `MarketingTimeline` | Accordion timeline tuned for roadmap milestones. |

### Utility Patterns

- **CTA motion**: add `.cta-motion` to any button to enable the shared glow hover.
- **Seam stacks**: combine `MarketingStackedSeam` with `MarketingGridSeam` for hybrid layouts (e.g., deployment options, FAQs).
- **Surface tokens**: all gradients reference `--accent-rgb` or brand tokens so palette adjustments propagate automatically.

Refer to `WEBSITE_MARKETING_LAYOUT_PLAN.md` for the evolving playbook and migration checklist.

## Configuration

All configuration is handled through environment variables defined in `config.js`:

```javascript
const config = {
  appUrl: import.meta.env.VITE_APP_URL || 'http://localhost:9090',
  apiUrl: import.meta.env.VITE_API_URL || 'http://localhost:9000',
  contactApiUrl: import.meta.env.VITE_CONTACT_API_URL || 'http://localhost:18032',
  // ... other config options
}
```

Key environment variables:

- `VITE_TURNSTILE_FORMS_SITE_KEY` – Cloudflare Turnstile site key for contact forms. Use the [Cloudflare test key](https://developers.cloudflare.com/turnstile/troubleshooting/testing/) (`1x0000000000000000000000000000000AA`) for local development.
- `VITE_CONTACT_API_URL` – Points to the contact API service (defaults to `http://localhost:18032`).
- `VITE_GATEWAY_URL` – GraphQL endpoint used for the status page and live player.

## Pages

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

## Deployment

### Docker Deployment

The marketing website includes a multi-stage Dockerfile:

```bash
# Build the Docker image
docker build -t frameworks-marketing .

# Run the container
docker run -p 80:80 frameworks-marketing
```

Configuration for production is also managed via `.env`. Use `env.example` as the source of truth for available variables and descriptions.

Note
- The Contact API (`VITE_CONTACT_API_URL`, default `http://localhost:18032`) is not part of the dev docker-compose stack. To use the contact form locally, run the service separately: `cd monorepo/api_forms && npm install && npm run dev`.
