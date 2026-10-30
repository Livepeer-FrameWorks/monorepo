import { motion } from "framer-motion";
import config from "../../config";
import { Section, SectionContainer } from "@/components/ui/section";
import StatusTag from "../shared/StatusTag";
import {
  MarketingHero,
  MarketingBand,
  HeadlineStack,
  CTACluster,
  MarketingFeatureWall,
  MarketingFinalCTA,
  MarketingScrollProgress,
  MarketingCTAButton,
  SectionDivider,
} from "@/components/marketing";

const shippingToday = [
  {
    title: "Live video delivery",
    tone: "green",
    description:
      "Ingest, protocol conversion, low-latency delivery, recording, and playback, on our own media server.",
  },
  {
    title: "Multistreaming",
    tone: "blue",
    description: "Push a live stream to other platforms and destinations while it runs.",
  },
  {
    title: "StreamCrafter browser studio",
    tone: "purple",
    description:
      "Go live from a browser with multi-source mixing. The SDK will change a lot as cloud production arrives, so expect breaking changes.",
    status: "beta",
    statusNote: "Beta: usable today, and the SDK will evolve substantially.",
  },
  {
    title: "Players and embeds",
    tone: "cyan",
    description: "Player SDKs with protocol selection and playback access control.",
  },
  {
    title: "Recording, clips, and VOD",
    tone: "orange",
    description: "DVR recording, clipping, and on-demand assets with retention controls.",
  },
  {
    title: "Your own edges",
    tone: "yellow",
    description: "Connect your own edge clusters, or run the full stack yourself.",
  },
  {
    title: "Incidents and alerts",
    tone: "red",
    description: "Stream incidents and alerting, with outbound webhooks for your own tools.",
  },
  {
    title: "Agent access",
    tone: "violet",
    description: "Let AI agents operate streams through the MCP server.",
  },
];

const inThePipeline = [
  {
    title: "StreamCrafter cloud production",
    tone: "yellow",
    description:
      "Mix and switch multiple live sources on the server, with picture-in-picture, overlays, and scene changes.",
  },
  {
    title: "Teams and roles",
    tone: "blue",
    description: "Shared tenants with member roles, so producers and operators work together.",
  },
  {
    title: "SCTE-35 cue markers",
    tone: "green",
    description: "Cues in the media engine today, with platform controls still to come.",
  },
  {
    title: "Live AI processing",
    tone: "orange",
    description: "Transcribe, analyze, and transform streams in real time.",
  },
  {
    title: "DRM content protection",
    tone: "violet",
    description: "FairPlay, Widevine, and PlayReady, built natively into MistServer.",
  },
  {
    title: "Device discovery",
    tone: "cyan",
    description: "Find IP cameras, PTZ controls, NDI sources, webcams, and HDMI inputs.",
  },
];

const furtherOut = [
  {
    title: "Guests and remote contribution",
    tone: "purple",
    description: "Invite guests into a show, mixed in the browser or on the server.",
  },
  {
    title: "Rooms and chat",
    tone: "blue",
    description: "Rooms, presence, chat, and moderation built on the same stream.",
  },
  {
    title: "Server-side ad insertion",
    tone: "orange",
    description: "Insert ads on the server, building on cue markers.",
  },
  {
    title: "Live commerce",
    tone: "green",
    description: "Shopping and auctions synced to the live stream.",
  },
];

const toCards = (items, status, statusNote) =>
  items.map((item) => ({
    tone: item.tone,
    title: item.title,
    description: item.description,
    meta:
      item.status || status ? (
        <StatusTag status={item.status ?? status} note={item.statusNote ?? statusNote} />
      ) : undefined,
    hover: "subtle",
    stripe: true,
  }));

const Showcase = () => {
  const groups = [
    {
      eyebrow: "Shipping today",
      title: "Available now",
      cards: toCards(shippingToday),
    },
    {
      eyebrow: "In the pipeline",
      title: "In active development",
      cards: toCards(
        inThePipeline,
        "pipeline",
        "In the pipeline: in active development, not generally available yet."
      ),
    },
    {
      eyebrow: "Further out",
      title: "On the roadmap",
      cards: toCards(furtherOut, "pipeline", "On the roadmap: planned, with no date yet."),
    },
  ];

  return (
    <div className="pt-16">
      <MarketingHero
        seed="/showcase"
        eyebrow="Showcase"
        title="What FrameWorks does today, and what comes next"
        description="A plain view of what you can use now, what we are building, and what is further out."
        align="center"
        surface="gradient"
        surfaceTone="accent"
        surfaceIntensity="raised"
      />

      <SectionDivider />

      {groups.map((group) => (
        <div key={group.eyebrow}>
          <Section>
            <SectionContainer>
              <motion.div
                initial={{ opacity: 0, y: 24 }}
                whileInView={{ opacity: 1, y: 0 }}
                viewport={{ once: true, amount: 0.2 }}
                transition={{ duration: 0.6 }}
              >
                <MarketingBand preset="quiet">
                  <HeadlineStack
                    eyebrow={group.eyebrow}
                    title={group.title}
                    align="left"
                    underlineAlign="start"
                  />
                  <MarketingFeatureWall items={group.cards} columns={3} stackAt="md" />
                </MarketingBand>
              </motion.div>
            </SectionContainer>
          </Section>
          <SectionDivider />
        </div>
      ))}

      <Section>
        <SectionContainer>
          <MarketingBand preset="quiet">
            <HeadlineStack
              eyebrow="Talk to the engineers"
              title="Support from video engineers"
              subtitle="You work directly with the people who have built MistServer for more than 15 years. That experience covers your whole pipeline, not just our part of it: encoders and contribution before FrameWorks, players and delivery after it. Reach out for trial usage, custom pricing, or a shared channel."
              align="left"
              underlineAlign="start"
              actionsPlacement="inline"
              actions={
                <CTACluster align="end">
                  <MarketingCTAButton
                    intent="primary"
                    to="/contact"
                    label="Talk to the engineers"
                  />
                </CTACluster>
              }
            />
          </MarketingBand>
        </SectionContainer>
      </Section>

      <SectionDivider />

      <Section>
        <MarketingFinalCTA
          eyebrow="Next steps"
          title="Three ways to deploy"
          description="Hosted, hybrid, or self-hosted. Video infrastructure that fits how you work."
          variant="band"
          primaryAction={{ label: "Start Free", href: config.appUrl, external: true }}
          secondaryAction={[
            { label: "See the full roadmap", href: `${config.docsUrl}/roadmap`, external: true },
          ]}
        />
      </Section>

      <MarketingScrollProgress />
    </div>
  );
};

export default Showcase;
