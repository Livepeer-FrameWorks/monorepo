import { useState } from "react";
import config from "../../config";
import { parsePlaybackUrl, parseWhipUrl } from "../../lib/streamTest";
import { Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Section, SectionContainer } from "@/components/ui/section";
import {
  MarketingHero,
  MarketingBand,
  HeadlineStack,
  MarketingFinalCTA,
  SectionDivider,
} from "@/components/marketing";

const loadPlayer = async () => {
  const [module] = await Promise.all([
    import("@livepeer-frameworks/player-react"),
    import("@livepeer-frameworks/player-react/player.css"),
  ]);
  return module.Player;
};

const loadStudio = async () => {
  const [module] = await Promise.all([
    import("@livepeer-frameworks/streamcrafter-react"),
    import("@livepeer-frameworks/streamcrafter-react/streamcrafter.css"),
  ]);
  return module.StreamCrafter;
};

const UrlForm = ({ id, label, placeholder, action, onSubmit, error }) => {
  const [value, setValue] = useState("");

  return (
    <form
      className="space-y-2"
      onSubmit={(event) => {
        event.preventDefault();
        onSubmit(value);
      }}
    >
      <label htmlFor={id} className="text-sm font-medium text-foreground">
        {label}
      </label>
      <div className="flex flex-col gap-3 sm:flex-row">
        <Input
          id={id}
          type="url"
          required
          placeholder={placeholder}
          autoComplete="off"
          spellCheck={false}
          value={value}
          onChange={(event) => setValue(event.target.value)}
        />
        <Button type="submit">{action}</Button>
      </div>
      {error ? (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      ) : null}
    </form>
  );
};

const PlayerTest = () => {
  const [Player, setPlayer] = useState(null);
  const [source, setSource] = useState(null);
  const [error, setError] = useState("");

  const play = async (value) => {
    const parsed = parsePlaybackUrl(value, window.location.protocol);
    if (parsed.error) {
      setError(parsed.error);
      return;
    }
    setError("");
    try {
      const component = await loadPlayer();
      setPlayer(() => component);
      setSource({ key: value, ...parsed });
    } catch {
      setError("The player could not be loaded. Reload the page and try again.");
    }
  };

  return (
    <div className="space-y-4 p-6">
      <UrlForm
        id="playback-url"
        label="Media or MistServer URL"
        placeholder="https://example.com/live/index.m3u8"
        action="Play"
        onSubmit={play}
        error={error}
      />
      {Player && source ? (
        <div className="aspect-video w-full overflow-hidden rounded-md bg-black">
          <Player
            key={source.key}
            contentId={source.contentId}
            contentType={source.contentType}
            endpoints={source.endpoints}
            options={{ mistUrl: source.mistUrl, autoplay: true, muted: true, controls: true }}
          />
        </div>
      ) : null}
    </div>
  );
};

const StudioTest = () => {
  const [Studio, setStudio] = useState(null);
  const [whipUrl, setWhipUrl] = useState("");
  const [error, setError] = useState("");

  const open = async (value) => {
    const parsed = parseWhipUrl(value, window.location.protocol);
    if (parsed.error) {
      setError(parsed.error);
      return;
    }
    setError("");
    try {
      const component = await loadStudio();
      setStudio(() => component);
      setWhipUrl(parsed.whipUrl);
    } catch {
      setError("The studio could not be loaded. Reload the page and try again.");
    }
  };

  return (
    <div className="space-y-4 p-6">
      <UrlForm
        id="whip-url"
        label="WHIP URL"
        placeholder="https://example.com/webrtc/my-stream-key"
        action="Open studio"
        onSubmit={open}
        error={error}
      />
      {Studio && whipUrl ? (
        <div className="w-full overflow-hidden rounded-md bg-black">
          <Studio
            key={whipUrl}
            whipUrl={whipUrl}
            initialProfile="broadcast"
            autoStartCamera={false}
            enableCompositor
          />
        </div>
      ) : null}
    </div>
  );
};

const StreamTest = () => (
  <div className="pt-16">
    <MarketingHero
      seed="/stream-test"
      eyebrow="Stream test"
      title="Test a stream in your browser"
      description="Play a media URL with the FrameWorks player, or publish to any WHIP endpoint with StreamCrafter. Your URLs stay in your browser."
      align="center"
      surface="gradient"
      surfaceTone="accent"
      surfaceIntensity="raised"
    />

    <SectionDivider />

    <Section>
      <SectionContainer>
        <MarketingBand preset="quiet">
          <HeadlineStack
            eyebrow="Player"
            title="Play a stream"
            subtitle="Paste an HLS, DASH, MP4, WebM, or WHEP URL. A MistServer stream URL also works, and the player then chooses the best protocol."
            align="left"
            underlineAlign="start"
          />
          <PlayerTest />
        </MarketingBand>
      </SectionContainer>
    </Section>

    <SectionDivider />

    <Section>
      <SectionContainer>
        <MarketingBand preset="quiet">
          <HeadlineStack
            eyebrow="StreamCrafter"
            title="Publish to a WHIP endpoint"
            subtitle="Paste a WHIP URL, add your camera or screen, and go live. Your WHIP URL can contain a stream key, so treat it as a password."
            align="left"
            underlineAlign="start"
          />
          <StudioTest />
        </MarketingBand>
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
