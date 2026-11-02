import { useEffect, useState } from "react";
import config from "../../config";
import { docsLink } from "../../lib/siteLinks";
import { Section, SectionContainer } from "@/components/ui/section";
import { MarketingHero, MarketingFinalCTA, SectionDivider } from "@/components/marketing";

// The tester pulls in the player and StreamCrafter, so it loads in the browser
// after the page renders instead of in the prerendered HTML.
const loadTester = async () => {
  const [module] = await Promise.all([
    import("@livepeer-frameworks/player-tester"),
    import("@livepeer-frameworks/player-tester/tester.css"),
    import("@livepeer-frameworks/player-react/player.css"),
    import("@livepeer-frameworks/streamcrafter-react/streamcrafter.css"),
  ]);
  return module.StreamTester;
};

const demoFixtures =
  config.demoFixtures && config.demoFixtures.length > 0
    ? config.demoFixtures
    : [{ id: config.demoStreamName, label: "Demo" }];

const samples = demoFixtures.flatMap((fixture) => [
  {
    id: fixture.id,
    label: `FrameWorks ${fixture.label.toLowerCase()} stream`,
    source: { mode: "playback", playbackId: fixture.id },
  },
  {
    id: `${fixture.id}-whep`,
    label: `FrameWorks ${fixture.label.toLowerCase()} stream over WHEP`,
    source: { mode: "playback", playbackId: fixture.id, protocol: "WHEP" },
  },
]);

const links = [
  { label: "Player guide", href: docsLink(config.docsUrl, "/builders/playback") },
  {
    label: "Go live from the browser",
    href: docsLink(config.docsUrl, "/builders/go-live-browser"),
  },
  { label: "StreamCrafter SDK", href: docsLink(config.docsUrl, "/builders/ingest") },
  { label: "What ships next", href: "/showcase" },
];

const Tester = () => {
  const [StreamTester, setStreamTester] = useState(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let active = true;
    loadTester()
      .then((component) => {
        if (active) setStreamTester(() => component);
      })
      .catch(() => {
        if (active) setFailed(true);
      });
    return () => {
      active = false;
    };
  }, []);

  if (failed) {
    return (
      <p role="alert" className="text-sm text-destructive">
        The stream tester could not be loaded. Reload the page and try again.
      </p>
    );
  }
  if (!StreamTester) {
    return <p className="text-sm text-muted-foreground">Loading the stream tester…</p>;
  }
  return (
    <StreamTester
      gatewayUrl={config.gatewayUrl}
      samples={samples}
      initialSource={samples[0]?.source}
      links={links}
      tlsHelpUrl={docsLink(config.docsUrl, "/operators/selfhosted-edge-rollout")}
      whipHelpUrl={docsLink(config.docsUrl, "/builders/ingest#where-to-find-your-whip-url")}
    />
  );
};

const StreamTest = () => (
  <div className="pt-16">
    <MarketingHero
      seed="/stream-test"
      eyebrow="Stream test"
      title="Test a stream in your browser"
      description="Play a FrameWorks playback ID, a MistServer stream or any HLS, DASH, MP4, WebM or WHEP URL, and see the protocol, bitrate, buffer, latency and tracks as it plays. Publish to a WHIP endpoint with StreamCrafter. Your URLs and stream keys stay in your browser."
      align="center"
      surface="gradient"
      surfaceTone="accent"
      surfaceIntensity="raised"
    />

    <SectionDivider />

    <Section>
      <SectionContainer>
        <Tester />
      </SectionContainer>
    </Section>

    <SectionDivider />

    <Section>
      <MarketingFinalCTA
        eyebrow="Next steps"
        title="Run your streams on FrameWorks"
        description="Start free, or ask the engineers about your setup."
        variant="band"
        primaryAction={{ label: "Start Free", href: config.appUrl, external: true }}
        secondaryAction={[{ label: "Talk to the engineers", href: "/contact" }]}
      />
    </Section>
  </div>
);

export default StreamTest;
