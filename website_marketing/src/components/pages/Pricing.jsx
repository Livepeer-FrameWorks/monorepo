import { motion } from "framer-motion";
import { Link } from "react-router";
import config from "../../config";
import status from "@/data/status.json";
import { featureOf, showcaseHref } from "@/lib/featureStatus";
import { signupUrl } from "@/lib/siteLinks";
import { contactPath } from "@/lib/contactTopics";
import InfoTooltip from "../shared/InfoTooltip";
import StatusTag from "../shared/StatusTag";
import SovereigntyNote from "../shared/SovereigntyNote";
import SavingsCalculator from "../shared/SavingsCalculator";
import { Section, SectionContainer } from "@/components/ui/section";
import {
  MarketingHero,
  MarketingSlab,
  MarketingSlabHeader,
  IconList,
  MarketingFinalCTA,
  MarketingScrollProgress,
  MarketingBand,
  MarketingComparisonGrid,
  MarketingComparisonCard,
  MarketingCTAButton,
  HeadlineStack,
  CTACluster,
  SectionDivider,
  ComparisonTable,
  PricingTierOutline,
  MarketingGridSplit,
  DeploymentModes,
} from "@/components/marketing";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { HomeIcon, BanknotesIcon, CpuChipIcon } from "@heroicons/react/24/outline";
import pricingCatalog from "@/data/pricing-catalog.json";

const catalogTier = Object.fromEntries(pricingCatalog.tiers.map((t) => [t.id, t]));
const formatEuro = (n, digits = 2) =>
  `€${n.toLocaleString("en-US", { minimumFractionDigits: digits, maximumFractionDigits: digits })}`;
const minutePrice = (tier) => formatEuro(tier.deliveredMinutes.unitPrice, 5);
const storagePrice = (tier) => `${formatEuro(tier.storage.unitPrice, 3)}/GB-month`;
const includedMinutes = (tier) => tier.deliveredMinutes.included.toLocaleString("en-US");
const basePrice = (tier) => `€${tier.basePrice.toLocaleString("en-US")}`;
const dailyCost = (tier) => `~${formatEuro(tier.basePrice / 30)}/day`;
const compactMinutes = (tier) =>
  new Intl.NumberFormat("en-US", { notation: "compact" }).format(tier.deliveredMinutes.included);

const paidTierPricing = (tier) => ({
  price: basePrice(tier),
  dailyCost: dailyCost(tier),
  delivery: (
    <span key="delivery">
      {includedMinutes(tier)} delivered minutes included{" "}
      <InfoTooltip>Overage {minutePrice(tier)}/min</InfoTooltip>
    </span>
  ),
  storage: `Recordings and uploads stored at ${storagePrice(tier)}`,
});

const supporterPricing = paidTierPricing(catalogTier.supporter);
const developerPricing = paidTierPricing(catalogTier.developer);
const productionPricing = paidTierPricing(catalogTier.production);
const freeCatalog = catalogTier.free;

const streamingFeatures = [
  "Adaptive bitrate included",
  "Multistreaming with no destination cap: each enabled destination uses one viewer-capacity slot and counts toward delivered minutes",
];

const freeTier = {
  id: "free",
  name: "Free Tier",
  price: "Free",
  period: "",
  description:
    "Stream on FrameWorks infrastructure, with free transcoding through the Livepeer network and free delivery from the shared bandwidth pool.",
  features: [
    `${compactMinutes(freeCatalog)} delivered min/mo, ${freeCatalog.limits.storageGiB} GB storage, ${freeCatalog.limits.maxConcurrentStreams} streams / ${freeCatalog.limits.maxConcurrentViewers} viewers, ${freeCatalog.limits.retentionDays}-day retention`,
    ...streamingFeatures,
    "The same dashboard, APIs, and analytics as paid plans",
  ],
  limitations: [
    `${freeCatalog.limits.maxConcurrentStreams} streams, ${freeCatalog.limits.maxConcurrentViewers} viewers, and ${freeCatalog.limits.storageGiB} GB storage are hard limits at any load`,
    "Delivered minutes above the allowance are served only while cluster capacity allows, and at peak load paying tenants take priority",
  ],
  badge: "No Surprise Bills",
  ctaLabel: "Start Free",
  ctaLink: signupUrl(config.appUrl, "pricing-free"),
};

const supportLine = "Business-hours support from our video engineers";
const teamRbac = featureOf(status, "team-rbac");

const paidTiers = [
  {
    id: "supporter",
    tone: "accent",
    name: "Supporter",
    price: supporterPricing.price,
    period: "/month",
    dailyCost: supporterPricing.dailyCost,
    description: `${compactMinutes(catalogTier.supporter)} delivered minutes, hosted load balancer, and a custom subdomain.`,
    features: [
      supporterPricing.delivery,
      supporterPricing.storage,
      ...streamingFeatures,
      "Hosted load balancer",
      "Custom subdomain (*.frameworks.network)",
      "Transparent usage reporting",
      supportLine,
    ],
    limitations: ["Suitable for ~100–300 concurrent viewers (adaptive)"],
    note: "Includes hosted LB + subdomain",
    ctaLabel: "Get Started",
    ctaLink: signupUrl(config.appUrl, "pricing-supporter", { plan: "supporter" }),
  },
  {
    id: "developer",
    tone: "cyan",
    name: "Developer",
    price: developerPricing.price,
    period: "/month",
    dailyCost: developerPricing.dailyCost,
    description: `${compactMinutes(catalogTier.developer)} delivered minutes, lower overage rates, and team features as they ship.`,
    features: [
      developerPricing.delivery,
      developerPricing.storage,
      ...streamingFeatures,
      `Team collaboration features (${teamRbac.tag.toLowerCase()})`,
      supportLine,
    ],
    limitations: ["Suitable for ~500–1,000 concurrent viewers (adaptive)"],
    note: "Lower overage rates than Supporter",
    ctaLabel: "Get Started",
    ctaLink: signupUrl(config.appUrl, "pricing-developer", { plan: "developer" }),
  },
  {
    id: "production",
    tone: "yellow",
    name: "Production",
    price: productionPricing.price,
    period: "/month",
    dailyCost: productionPricing.dailyCost,
    description: `${compactMinutes(catalogTier.production)} delivered minutes, the lowest overage rates, and a dedicated processing allocation.`,
    features: [
      productionPricing.delivery,
      productionPricing.storage,
      ...streamingFeatures,
      "Dedicated processing allocation",
      `${supportLine}, on-call by Enterprise agreement`,
    ],
    limitations: ["Suitable for ~2,000–5,000 concurrent viewers (adaptive)"],
    note: "Reserved capacity through an Enterprise arrangement",
    ctaLabel: "Talk to the engineers",
    ctaLink: contactPath("pricing"),
  },
];

const enterpriseTier = {
  name: "Enterprise",
  price: "Custom pricing",
  headline: "For teams scaling past Production limits or needing custom compliance.",
  description: "",
  bullets: [
    "Custom feature development and white-label solutions",
    "Private deployments or co-managed operations with our engineers",
    "Committed bandwidth and processing capacity with reserved capacity",
    "Custom SLAs with training, consulting, and on-call support",
    "Invoice, procurement, or prepaid billing terms",
  ],
};

const payAsYouGo = {
  name: "Account Balance",
  price: minutePrice(catalogTier.payg),
  period: "/delivered min",
  badge: "Agent-Ready",
  description: "Pay only when you use it. Wallet-friendly, with account controls for operators.",
  features: [
    "No subscription, no commitment",
    "Wallet auth with no email or signup form",
    "Top up via card, crypto, or gasless USDC",
    `Storage at ${storagePrice(catalogTier.payg)}, full API access`,
  ],
};

const everyTier = (value) => ({
  free: value,
  supporter: value,
  developer: value,
  production: value,
});

// Rows for registry features take their title, summary and status from
// status.json; planned rows link to their showcase entry.
const statusRow = (id, cell, link) => {
  const entry = featureOf(status, id);
  return {
    feature: entry.showcase?.title ?? entry.title,
    description: entry.showcase?.summary ?? entry.note,
    statusId: id,
    link: link ?? { label: "On the showcase", to: showcaseHref(id) },
    tiers: everyTier(cell),
  };
};

const processingFeatureMatrix = [
  {
    feature: "Transcoding",
    description:
      "Real-time transcoding on FrameWorks edges for dedicated capacity, on Livepeer for low-cost capacity that is quick to scale, or on your own edge with no FrameWorks processing charge.",
    tiers: {
      free: "Powered by Livepeer network",
      supporter: "Powered by Livepeer network",
      developer: "Powered by Livepeer network",
      production: "Dedicated processing allocation",
    },
  },
  statusRow("streamcrafter", "Included", { label: "Try it in your browser", to: "/stream-test" }),
  statusRow("live-ai", "Not yet available"),
  statusRow("composition", "Not yet available"),
];

const tierColumns = [
  { key: "free", label: "Free" },
  { key: "supporter", label: "Supporter" },
  { key: "developer", label: "Developer" },
  { key: "production", label: "Production" },
];

const deploymentModes = [
  {
    id: "hosted",
    tone: "accent",
    title: "Fully hosted",
    description:
      "FrameWorks runs ingest, delivery, processing, and operations. Your team keeps the dashboards, logs, and runbooks, with managed load balancers and per-line-item usage.",
  },
  {
    id: "hybrid",
    tone: "green",
    title: "Hybrid, self-hosted edge",
    description:
      "You run edge nodes. FrameWorks runs the control plane and hosted burst capacity, with one set of dashboards, failover, and traffic steering across your sites and ours.",
  },
  {
    id: "self-hosted",
    tone: "green",
    title: "Fully self-hosted",
    description:
      "You run the control plane, databases, and edge on bare metal, VMs, or Kubernetes. S3-compatible storage and DNS stay external integrations today. Burst into hosted capacity when you choose.",
  },
  {
    id: "enterprise",
    tone: "yellow",
    title: "Enterprise and regulated",
    description:
      "Reserved clusters, custom SLAs, security and compliance reviews, and co-managed operations with our engineers for regulated environments.",
  },
];

export const PRICING_FAQS = [
  {
    question: "Why can FrameWorks price video differently from hyperscaler-based platforms?",
    answer:
      "Our team develops the core MistServer media engine and can operate the live path directly on infrastructure including bare metal. For processing, use FrameWorks edges when you need dedicated capacity, Livepeer when you want low-cost capacity that is quick to scale, or your own edge with no FrameWorks processing charge.",
  },
  {
    question: "What does beta pricing include?",
    answer:
      "Generous allowances, transparent overages, and shared processing capacity. As we scale, pricing may adjust, but you keep the allowances you signed up for throughout beta.",
  },
  {
    question: "Can I mix self-hosted and hosted workloads?",
    answer:
      "Yes. Every tier includes self-hosting. You can process on your own edge with no FrameWorks processing charge, use Livepeer for low-cost capacity that is quick to scale, or move workloads that need dedicated capacity onto FrameWorks edges.",
  },
  {
    question: "When should I upgrade to Production?",
    answer:
      "Upgrade once you have steady audiences or need dedicated processing capacity. Production is tuned for 2,000–5,000 concurrent viewers with reserved capacity.",
  },
  {
    question: "How does the hosted load balancer work?",
    answer:
      "From Supporter tier onward, we run Foghorn load balancers for you. They handle routing, failover, certificates, and scaling so your ingest infrastructure stays resilient without extra ops.",
  },
  {
    question: "Who answers when we need support?",
    answer:
      "Video engineers. You work directly with the people who have built MistServer for more than 15 years. That experience covers your whole pipeline, not just our part of it: encoders and contribution before FrameWorks, players and delivery after it.",
  },
  {
    question: "What if we outgrow the published tiers?",
    answer:
      "Enterprise engagements add private deployments, custom SLAs, security reviews, and co-managed operations. We scope the stack with you so you retain control while leaning on our crews.",
  },
  {
    question: "What happens when a prepaid balance runs out?",
    answer:
      "Running streams and their viewers keep going. At €0 new streams and new viewers are blocked, so a viewer or encoder that disconnects cannot reconnect until you top up. Below -€10 the account is suspended and streams stop.",
  },
  {
    question: "What happens if a card payment fails?",
    answer:
      "Your plan and streams keep running for 14 days while you set up payment again. After that your account moves to the Free plan, with a final invoice for your usage up to the move.",
  },
  {
    question: "What is pay-as-you-go billing?",
    answer:
      "Add funds to your account via card or crypto. Delivered minutes and storage are deducted automatically; ingest, transcoding, and bandwidth are included. No invoices or monthly commitment. Top up again when your balance runs low.",
  },
  {
    question: "Can I use FrameWorks without an email account?",
    answer:
      "Yes. Connect an Ethereum wallet to authenticate. Your wallet address is your identity. You can optionally add an email later for notifications.",
  },
  {
    question: "How do AI agents access FrameWorks?",
    answer:
      "Agents discover the platform via skill.json (compatible with OpenClaw, Claude Code, Cursor, Gemini CLI), llms.txt, or .well-known/mcp.json. They authenticate with an EVM wallet signature that auto-provisions a prepaid tenant without email or registration. They fund their balance via x402 gasless USDC on Base or Arbitrum, then use MCP tools or GraphQL to manage streams, recordings, analytics, and QoE diagnostics. Three operations work without any auth: payment options, payment submission, and playback resolution.",
  },
  {
    question: "What is x402 and how does it work?",
    answer:
      "x402 is an open protocol for inline HTTP payments using USDC. When an agent hits a balance wall, the server returns HTTP 402 with payment options. The agent signs an EIP-3009 transfer authorization off-chain, retries with an X-PAYMENT header, and FrameWorks submits the transaction on-chain (paying the gas). Balance credits instantly. Supported on Base and Arbitrum L2s.",
  },
  {
    question: "Which agent frameworks are compatible?",
    answer:
      "Any framework that reads skill.json or llms.txt can discover FrameWorks, including OpenClaw, Claude Code, Claude Desktop, Cursor, Gemini CLI, and others. For full operational access, connect via the MCP endpoint or use the GraphQL API directly. The platform also publishes a W3C DID document and OAuth protected resource metadata for standards-based integration.",
  },
];

const pricingHeroAccents = [
  {
    kind: "beam",
    x: 14,
    y: 32,
    width: "clamp(28rem, 46vw, 36rem)",
    height: "clamp(18rem, 32vw, 26rem)",
    rotate: -16,
    fill: "linear-gradient(150deg, rgba(122, 162, 247, 0.32), rgba(18, 22, 38, 0.18))",
    opacity: 0.5,
    radius: "44px",
  },
  {
    kind: "beam",
    x: 82,
    y: 24,
    width: "clamp(18rem, 34vw, 28rem)",
    height: "clamp(16rem, 28vw, 22rem)",
    rotate: 24,
    fill: "linear-gradient(155deg, rgba(125, 207, 255, 0.26), rgba(24, 30, 48, 0.16))",
    opacity: 0.46,
    radius: "36px",
  },
  {
    kind: "spot",
    x: 58,
    y: 78,
    width: "clamp(26rem, 52vw, 40rem)",
    height: "clamp(26rem, 52vw, 40rem)",
    fill: "radial-gradient(circle, rgba(63, 78, 150, 0.26) 0%, transparent 68%)",
    opacity: 0.32,
    blur: "110px",
  },
  {
    kind: "beam",
    x: 8,
    y: 80,
    width: "clamp(18rem, 32vw, 26rem)",
    height: "clamp(16rem, 30vw, 24rem)",
    rotate: -8,
    fill: "linear-gradient(165deg, rgba(59, 117, 214, 0.22), rgba(16, 20, 32, 0.12))",
    opacity: 0.36,
    radius: "38px",
  },
];

const pricingHeroHighlights = [
  {
    title: "Zero licensing fees",
    description:
      "Self-host the full stack with no per-seat, per-core, or per-stream licensing. Public domain code.",
    tone: "yellow",
    icon: HomeIcon,
  },
  {
    title: "No rebuild between models",
    description:
      "Move from hosted to hybrid or self-hosted without changing the application or APIs built on top.",
    tone: "accent",
    icon: CpuChipIcon,
  },
  {
    title: "Deep analytics included",
    description:
      "See why viewer X connected to edge Y with QoE metrics and routing decisions in every plan.",
    tone: "green",
    icon: BanknotesIcon,
  },
];

const Pricing = () => {
  const tierCards = paidTiers.map((plan, index) => {
    const isInternalLink = plan.ctaLink.startsWith("/");
    const actionProps = isInternalLink
      ? { to: plan.ctaLink }
      : { href: plan.ctaLink, external: true };

    return {
      id: plan.id,
      tone: plan.tone ?? "accent",
      title: plan.name,
      badge: plan.id === "developer" ? "Best Value" : undefined,
      price: plan.price,
      period: plan.period,
      meta: plan.dailyCost,
      description: plan.description,
      features: plan.features,
      limitations: plan.limitations,
      footnote: plan.note,
      featured: plan.id === "developer",
      action: (
        <MarketingCTAButton
          intent={plan.id === "production" ? "secondary" : "primary"}
          label={plan.ctaLabel}
          className="w-full justify-center"
          {...actionProps}
        />
      ),
      motionDelay: index * 0.08,
    };
  });

  const paidCards = tierCards;

  const processingColumns = tierColumns.map((tier) => ({
    key: tier.key,
    label: tier.label,
  }));

  const processingRows = processingFeatureMatrix.map((entry) => {
    const tierCells = tierColumns.reduce(
      (acc, tier) => ({
        ...acc,
        [tier.key]: entry.tiers[tier.key],
      }),
      {}
    );

    return {
      key: entry.feature,
      label: (
        <div className="pricing-processing-feature">
          <span className="pricing-processing-feature__title">{entry.feature}</span>
          <p className="pricing-processing-feature__copy">{entry.description}</p>
          {entry.statusId ? <StatusTag feature={entry.statusId} className="mt-1" /> : null}
          {entry.link ? (
            <Link to={entry.link.to} className="pricing-processing-feature__link">
              {entry.link.label} <span aria-hidden="true">→</span>
            </Link>
          ) : null}
        </div>
      ),
      ...tierCells,
    };
  });

  return (
    <div className="pt-16">
      <MarketingHero
        seed="/pricing"
        align="left"
        layout="split"
        mediaPosition="right"
        surface="gradient"
        surfaceTone="accent"
        surfaceIntensity="raised"
        accents={pricingHeroAccents}
        title={
          <>
            <span className="transparent-word" data-text="Transparent">
              Transparent
            </span>{" "}
            pricing
          </>
        }
        description="Start on FrameWorks infrastructure, connect your own edges, or deploy the full stack yourself. The media engine, APIs, and operational model stay consistent, and the costs stay visible."
        support={
          <IconList
            items={pricingHeroHighlights.map((highlight) => {
              const Icon = highlight.icon;
              return {
                title: highlight.title,
                description: highlight.description,
                icon: <Icon className="h-5 w-5 text-foreground/90" />,
                tone: highlight.tone,
              };
            })}
            columns={3}
            stackAt="md"
            headingLevel="h2"
          />
        }
      />

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <HeadlineStack
              eyebrow="Free tier"
              title="Start free on the hosted platform"
              subtitle="Stream on our infrastructure at no cost. Self-hosting the whole platform, control plane included, is free as well."
              align="left"
              underlineAlign="start"
            />
            <PricingTierOutline
              tone="accent"
              badge={freeTier.badge}
              name={freeTier.name}
              price={freeTier.price}
              period={freeTier.period}
              description={freeTier.description}
              actions={
                <CTACluster align="start">
                  <MarketingCTAButton
                    intent="primary"
                    label={freeTier.ctaLabel}
                    href={freeTier.ctaLink}
                    external
                  />
                </CTACluster>
              }
              sections={[
                {
                  title: "What you get",
                  items: freeTier.features,
                },
                {
                  title: "Limits",
                  items: freeTier.limitations,
                  bullet: "dash",
                },
              ]}
              className="mt-8"
            />
          </motion.div>
        </SectionContainer>

        <SectionContainer className="mt-6">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand
              preset="foundation"
              tone="cool"
              texturePattern="seams"
              textureNoise="film"
              textureBeam="soft"
              textureMotion="drift"
              textureStrength="soft"
            >
              <HeadlineStack
                eyebrow="Paid tiers"
                title="Add managed infrastructure without changing stacks"
                subtitle="Move workloads onto FrameWorks infrastructure, reserve capacity, and add operational support while keeping the same media engine and APIs. All hosted plans run on the same redundant infrastructure, and running streams continue through a control-plane outage."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <CTACluster align="end" wrap>
                    <MarketingCTAButton
                      intent="secondary"
                      href={config.docsUrl}
                      label="Read the docs"
                    />
                    <MarketingCTAButton
                      intent="secondary"
                      to={contactPath("pricing")}
                      label="Talk to the engineers"
                    />
                  </CTACluster>
                }
              />
              <MarketingComparisonGrid
                columns={3}
                stackAt="md"
                gap="tight"
                items={paidCards}
                renderCard={(item, index) => (
                  <motion.div
                    key={item.id ?? index}
                    initial={{ opacity: 0, y: 24 }}
                    whileInView={{ opacity: 1, y: 0 }}
                    viewport={{ once: true }}
                    transition={{ duration: 0.55, delay: item.motionDelay ?? index * 0.08 }}
                  >
                    <MarketingComparisonCard {...item} />
                  </motion.div>
                )}
              />
            </MarketingBand>
          </motion.div>
        </SectionContainer>

        <SectionContainer id="pay-as-you-go" className="mt-6 scroll-mt-24">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand surface="none">
              <HeadlineStack
                eyebrow="No subscription required"
                title="Pay As You Go"
                subtitle="Top up your balance and pay for what you use. AI agents can discover, authenticate, fund, and operate the platform through MCP and GraphQL, using the same account balance as operators."
                align="left"
                underlineAlign="start"
              />
              <PricingTierOutline
                tone="cyan"
                badge={payAsYouGo.badge}
                name={payAsYouGo.name}
                price={payAsYouGo.price}
                period={payAsYouGo.period}
                description={payAsYouGo.description}
                actions={
                  <CTACluster align="start">
                    <MarketingCTAButton
                      intent="primary"
                      href={config.appUrl}
                      label="Connect Wallet"
                      external
                    />
                    <MarketingCTAButton
                      intent="secondary"
                      href={config.appUrl}
                      label="Add Funds"
                      external
                    />
                  </CTACluster>
                }
                className="mt-8"
                sections={[
                  {
                    title: "What's included",
                    items: payAsYouGo.features,
                  },
                ]}
              />
            </MarketingBand>
          </motion.div>
        </SectionContainer>

        <SectionContainer className="mt-6">
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand surface="none">
              <HeadlineStack
                eyebrow="Need more?"
                title="Go Enterprise"
                subtitle={enterpriseTier.headline}
                align="left"
                underlineAlign="start"
              />
              <PricingTierOutline
                tone="amber"
                name={enterpriseTier.name}
                price={enterpriseTier.price}
                description={enterpriseTier.description}
                actions={
                  <CTACluster align="start">
                    <MarketingCTAButton
                      intent="primary"
                      to={contactPath("pricing")}
                      label="Talk to the engineers"
                    />
                  </CTACluster>
                }
                className="mt-8"
                sections={[
                  {
                    title: "What's included",
                    items: enterpriseTier.bullets,
                  },
                ]}
              />
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface" id="calculator">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand surface="none">
              <HeadlineStack
                eyebrow="Cost calculator"
                title="What will it cost?"
                subtitle="Pick a workload close to yours and adjust it. Every plan is priced side by side, including storage for recordings and uploads."
                align="left"
                underlineAlign="start"
              />
              <SavingsCalculator className="mt-8" />
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface-strong">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand
              preset="foundation"
              tone="steel"
              texturePattern="pinlines"
              textureNoise="grain"
              textureBeam="soft"
              textureMotion="drift"
              textureStrength="soft"
            >
              <HeadlineStack
                eyebrow="Advanced processing"
                title="Advanced processing capabilities"
                subtitle="Run processing on FrameWorks edges for dedicated capacity, use Livepeer for low-cost capacity that is quick to scale, or process on your own edge with no FrameWorks processing charge. The StreamCrafter browser studio works on every plan today; live AI and StreamCrafter cloud production are next on the roadmap."
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
              />
              <ComparisonTable columns={processingColumns} rows={processingRows} tone="accent" />
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand
              preset="foundation"
              texturePattern="seams"
              textureNoise="film"
              textureBeam="soft"
              textureStrength="soft"
            >
              <HeadlineStack
                eyebrow="Operating models"
                title="Hosted, hybrid, or fully self-hosted"
                subtitle={
                  <>
                    Choose where each part of the video path runs. FrameWorks can operate the
                    complete service, combine our control plane with your edges, or run entirely in
                    your footprint. <SovereigntyNote />
                  </>
                }
                align="left"
                underlineAlign="start"
                actionsPlacement="inline"
                actions={
                  <MarketingCTAButton
                    intent="primary"
                    to={contactPath("engineering")}
                    label="Talk to the engineers"
                  />
                }
              />
              <MarketingGridSplit align="stretch" stackAt="lg" seam ratio="wide-narrow">
                <div className="marketing-figure-cell">
                  <DeploymentModes />
                </div>
                <div className="slab-zone">
                  <div className="slab-zone__body">
                    <IconList
                      items={deploymentModes}
                      variant="list"
                      indicator="dot"
                      headingLevel="h3"
                    />
                  </div>
                </div>
              </MarketingGridSplit>
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section>
        <SectionContainer>
          <MarketingBand preset="quiet">
            <HeadlineStack
              eyebrow="Talk to the engineers"
              title="Trials, custom pricing, and a shared channel"
              subtitle="Reach out for trial usage, custom pricing, or a shared Slack or WhatsApp channel. You talk directly with the video engineers who build the platform."
              align="left"
              underlineAlign="start"
              actionsPlacement="inline"
              actions={
                <CTACluster align="end" wrap>
                  <MarketingCTAButton
                    intent="primary"
                    to={contactPath("trial")}
                    label="Talk to the engineers"
                  />
                  <MarketingCTAButton intent="secondary" to="/showcase" label="See the showcase" />
                </CTACluster>
              }
            />
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="bg-brand-surface">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingSlab variant="feature-panel">
              <MarketingSlabHeader
                as="h2"
                eyebrow="FAQ"
                title="Frequently asked questions"
                subtitle="Everything you need to know about FrameWorks pricing before you launch."
              />
              <Accordion type="single" collapsible>
                {PRICING_FAQS.map((faq, index) => (
                  <AccordionItem key={faq.question} value={`faq-${index}`}>
                    <AccordionTrigger>{faq.question}</AccordionTrigger>
                    <AccordionContent>
                      <div className="marketing-accordion__answer">
                        <p>{faq.answer}</p>
                      </div>
                    </AccordionContent>
                  </AccordionItem>
                ))}
              </Accordion>
            </MarketingSlab>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="px-0 bg-brand-surface-strong">
        <motion.div
          initial={{ opacity: 0, y: 32 }}
          whileInView={{ opacity: 1, y: 0 }}
          viewport={{ once: true }}
          transition={{ duration: 0.6 }}
        >
          <MarketingFinalCTA
            eyebrow="Next steps"
            title="Two ways to start"
            description="Start free on our hosted platform, or run the whole stack on your own hardware. The media engine and APIs are the same in both."
            variant="band"
            primaryAction={{
              label: "Start Free",
              href: signupUrl(config.appUrl, "pricing-final"),
              external: true,
            }}
            secondaryAction={[
              {
                label: "Browse Docs",
                href: config.docsUrl,
                external: true,
              },
              {
                label: "Talk to the engineers",
                to: contactPath("engineering"),
              },
            ]}
          />
        </motion.div>
      </Section>

      <MarketingScrollProgress />
    </div>
  );
};

export default Pricing;
