import { motion } from "framer-motion";
import config from "../../config";
import { GlobeAltIcon, SignalIcon, LockClosedIcon } from "@heroicons/react/24/outline";
import { Section, SectionContainer } from "@/components/ui/section";
import SovereigntyNote from "../shared/SovereigntyNote";
import {
  MarketingHero,
  MarketingBand,
  MarketingGridSplit,
  HeadlineStack,
  CTACluster,
  MarketingCTAButton,
  MarketingFeatureWall,
  MarketingFinalCTA,
  MarketingScrollProgress,
  SectionDivider,
  IconList,
  DashboardFrame,
  StatRow,
  RetentionCurve,
  TrendChart,
  BootWaterfall,
  BarBreakdown,
  GeoPanel,
  analyticsFixtures as fx,
} from "@/components/marketing";

const heroAccents = [
  {
    kind: "beam",
    x: 16,
    y: 30,
    width: "clamp(24rem, 46vw, 40rem)",
    height: "clamp(18rem, 34vw, 28rem)",
    rotate: -20,
    fill: "linear-gradient(150deg, rgba(53, 186, 255, 0.32), rgba(20, 26, 44, 0.22))",
    opacity: 0.55,
    radius: "50px",
  },
  {
    kind: "spot",
    x: 78,
    y: 26,
    width: "clamp(20rem, 42vw, 32rem)",
    height: "clamp(20rem, 42vw, 32rem)",
    fill: "radial-gradient(circle, rgba(125, 207, 255, 0.22) 0%, transparent 70%)",
    opacity: 0.34,
    blur: "90px",
  },
  {
    kind: "beam",
    x: 70,
    y: 80,
    width: "clamp(18rem, 32vw, 26rem)",
    height: "clamp(16rem, 28vw, 24rem)",
    rotate: 14,
    fill: "linear-gradient(140deg, rgba(147, 197, 114, 0.22), rgba(18, 24, 40, 0.18))",
    opacity: 0.32,
    radius: "42px",
  },
];

// Sentence bullets reuse the shared IconList dot variant (no bespoke list CSS).
const bullets = (items) => items.map((title) => ({ title }));

const infraCards = [
  {
    icon: GlobeAltIcon,
    tone: "cyan",
    iconTone: "cyan",
    title: "Global demand heatmap",
    description:
      "Where viewers actually are, bucketed into H3 cells for regional insight, without tracking individuals.",
  },
  {
    icon: SignalIcon,
    tone: "green",
    iconTone: "green",
    title: "Routing decisions, visualized",
    description:
      "Every client-to-edge flow, flagged for success and long-haul, so you can see why a viewer landed where they did.",
  },
  {
    icon: LockClosedIcon,
    tone: "yellow",
    iconTone: "yellow",
    title: "Hosted or self-hosted",
    description:
      "Run it hosted by us, or self-host the whole stack for full sovereignty. Federation shares the same view across hybrid edges.",
    meta: <SovereigntyNote />,
  },
];

const Analytics = () => {
  return (
    <div className="pt-16">
      <MarketingHero
        seed="/analytics"
        className="analytics-hero"
        title="Analytics that show the whole picture"
        description="From the first byte to the last frame: real-time viewer geography, routing decisions, player quality, and transparent usage, so you can see why viewer X connected to edge Y. Run it hosted, hybrid, or self-hosted."
        align="center"
        surface="gradient"
        surfaceTone="cyan"
        surfaceIntensity="raised"
        support="Real-time • Multi-tenant • Hosted or self-hosted"
        accents={heroAccents}
      />

      <SectionDivider />

      {/* Section 1: Live + VOD (viz right) */}
      <Section className="bg-brand-surface">
        <SectionContainer>
          <MarketingBand surface="none">
            <MarketingGridSplit align="start" stackAt="lg" gap="lg">
              <motion.div
                initial={{ opacity: 0, x: -26 }}
                whileInView={{ opacity: 1, x: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.55 }}
              >
                <HeadlineStack
                  eyebrow="Live + on-demand"
                  title="Know your audience as it happens"
                  align="left"
                  underlineAlign="start"
                >
                  <p className="analytics-intro">
                    Concurrent viewers, peak, views and watch hours update in real time over a
                    durable event backbone. VOD retention then shows exactly where attention holds
                    and where it drops.
                  </p>
                </HeadlineStack>
                <IconList
                  variant="plain"
                  indicator="dot"
                  tone="accent"
                  className="analytics-list"
                  items={bullets([
                    "Real-time concurrent and peak viewers, with quality-tier breakdowns per stream.",
                    "VOD audience retention with a most-replayed density strip that surfaces the moments that matter.",
                    "Every number scoped to your tenant and queryable over GraphQL.",
                  ])}
                />
              </motion.div>

              <motion.div
                className="w-full"
                initial={{ opacity: 0, x: 26 }}
                whileInView={{ opacity: 1, x: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.55, delay: 0.1 }}
              >
                <DashboardFrame title="Audience" badge="Live + VOD" tone="cyan">
                  <StatRow stats={fx.liveVodStats} />
                  <RetentionCurve {...fx.retention} />
                </DashboardFrame>
              </motion.div>
            </MarketingGridSplit>
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      {/* Section 2: Player experience (viz left) */}
      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <MarketingBand surface="none">
            <MarketingGridSplit align="start" stackAt="lg" gap="lg">
              <motion.div
                className="w-full"
                initial={{ opacity: 0, x: -26 }}
                whileInView={{ opacity: 1, x: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.55 }}
              >
                <DashboardFrame title="Player experience" badge="QoE + boot" tone="accent">
                  <TrendChart
                    data={fx.qoeTrend}
                    series={fx.qoeSeries}
                    height={250}
                    leftTitle="Rebuffer / frame-drop %"
                    rightTitle="Bitrate (Mbps)"
                  />
                  <BootWaterfall
                    stages={fx.bootWaterfall.stages}
                    cacheHitRatio={fx.bootWaterfall.cacheHitRatio}
                  />
                </DashboardFrame>
              </motion.div>

              <motion.div
                initial={{ opacity: 0, x: 26 }}
                whileInView={{ opacity: 1, x: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.55, delay: 0.1 }}
              >
                <HeadlineStack
                  eyebrow="Player experience"
                  title="Quality measured where it's felt, in the player"
                  align="left"
                  underlineAlign="start"
                >
                  <p className="analytics-intro">
                    QoE comes from the viewer's browser, not just the origin buffer: rebuffering,
                    frame drops and bitrate over time, plus a time-to-first-frame waterfall that
                    splits gateway resolve, Mist hydrate and prebuffer.
                  </p>
                </HeadlineStack>
                <IconList
                  variant="plain"
                  indicator="dot"
                  tone="accent"
                  className="analytics-list"
                  items={bullets([
                    "Viewer-measured rebuffering ratio, frame drops and bitrate trends.",
                    "Boot waterfall pinpoints startup latency from resolve to first frame.",
                    "Per-cluster and per-node breakdowns for operators.",
                  ])}
                />
              </motion.div>
            </MarketingGridSplit>
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      {/* Section 3: Infrastructure (full-width geo) */}
      <Section className="bg-brand-surface">
        <SectionContainer>
          <motion.div
            initial={{ opacity: 0, y: 30 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true }}
            transition={{ duration: 0.6 }}
          >
            <MarketingBand preset="foundation">
              <HeadlineStack
                eyebrow="Infrastructure"
                title="See which edge served every viewer, and why"
                align="left"
                underlineAlign="start"
              >
                <p className="analytics-intro">
                  Viewer demand, edge clusters and the routing decisions between them, on one live
                  map. Hold the modifier key to zoom; toggle routing to trace client-to-edge flows.
                </p>
              </HeadlineStack>
              <DashboardFrame
                title="Network"
                badge="Geo + routing"
                tone="accent"
                bodyClassName="dashboard-frame__body--flush"
              >
                <GeoPanel height={460} />
              </DashboardFrame>
              <MarketingFeatureWall items={infraCards} columns={3} stackAt="md" />
            </MarketingBand>
          </motion.div>
        </SectionContainer>
      </Section>

      <SectionDivider />

      {/* Section 4: Usage & cost (viz right) */}
      <Section className="bg-brand-surface-muted">
        <SectionContainer>
          <MarketingBand surface="none">
            <MarketingGridSplit align="start" stackAt="lg" gap="lg">
              <motion.div
                initial={{ opacity: 0, x: -26 }}
                whileInView={{ opacity: 1, x: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.55 }}
              >
                <HeadlineStack
                  eyebrow="Usage & cost"
                  title="Transparent usage, no data lock-in"
                  align="left"
                  underlineAlign="start"
                >
                  <p className="analytics-intro">
                    Egress, stream hours, storage and codec mix settle in near real time, about five
                    minutes, not end of month. Export it over the API, or run the whole stack
                    yourself.
                  </p>
                </HeadlineStack>
                <IconList
                  variant="plain"
                  indicator="dot"
                  tone="green"
                  className="analytics-list"
                  items={bullets([
                    "Bandwidth, storage and codec breakdowns with ~5-minute settlement.",
                    "Per-stream and per-tenant cost visibility.",
                    "Export over the API, or self-host the full stack. No proprietary lock-in.",
                  ])}
                />
                <CTACluster align="start" wrap className="analytics-cta">
                  <MarketingCTAButton
                    intent="primary"
                    label="Start Free"
                    href={config.appUrl}
                    external
                  />
                  <MarketingCTAButton
                    intent="secondary"
                    label="Read the docs"
                    href={config.docsUrl}
                    external
                  />
                </CTACluster>
              </motion.div>

              <motion.div
                className="w-full"
                initial={{ opacity: 0, x: 26 }}
                whileInView={{ opacity: 1, x: 0 }}
                viewport={{ once: true }}
                transition={{ duration: 0.55, delay: 0.1 }}
              >
                <DashboardFrame title="Usage" badge="Bandwidth + codecs" tone="green">
                  <StatRow stats={fx.usageStats} />
                  <BarBreakdown items={fx.codecMix} unit=" GB" height={210} />
                </DashboardFrame>
              </motion.div>
            </MarketingGridSplit>
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section className="px-0">
        <motion.div
          initial={{ opacity: 0, y: 32 }}
          whileInView={{ opacity: 1, y: 0 }}
          viewport={{ once: true }}
          transition={{ duration: 0.6 }}
        >
          <MarketingFinalCTA
            eyebrow="See it on your streams"
            title="Start measuring in minutes"
            description="Every plan includes the full analytics suite. Point a stream at FrameWorks and watch the data arrive."
            variant="band"
            primaryAction={{ label: "Start Free", href: config.appUrl, external: true }}
            secondaryAction={[
              { label: "Browse Docs", href: config.docsUrl, external: true },
              { label: "Talk to our team", to: "/contact" },
            ]}
          />
        </motion.div>
      </Section>

      <MarketingScrollProgress />
    </div>
  );
};

export default Analytics;
