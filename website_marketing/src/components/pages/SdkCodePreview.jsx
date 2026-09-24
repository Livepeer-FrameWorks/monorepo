import { useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import {
  PlayIcon,
  ArrowUpTrayIcon,
  ServerStackIcon,
  CodeBracketIcon,
  ClipboardDocumentCheckIcon,
  ClipboardDocumentIcon,
} from "@heroicons/react/24/outline";
import { cn } from "@/lib/utils";
import config from "../../config";

const sdkGuideUrl = `${config.docsUrl.replace(/\/+$/, "")}/builders/sdks`;

const snippetPlayerReact = `import { Player } from '@livepeer-frameworks/player-react'

export const MyStream = ({ playbackId }) => (
  <Player
    contentId={playbackId}
    contentType="live"
    theme="tokyo-night"
    options={{
      autoplay: true,
      muted: true,
      gatewayUrl: "${config.gatewayUrl}"
    }}
  />
)`;

const snippetPlayerSvelte = `<script lang="ts">
  import { Player } from "@livepeer-frameworks/player-svelte";
  import "@livepeer-frameworks/player-svelte/player.css";
</script>

<Player
  contentId="pk_..."
  contentType="live"
  gatewayUrl="${config.gatewayUrl}"
  autoplay={true}
  muted={true}
/>`;

const snippetPlayerWc = `<!-- IIFE via npm CDN, no bundler needed -->
<!-- unpkg -->
<script src="https://unpkg.com/@livepeer-frameworks/player-wc/dist/fw-player.iife.js"></script>
<!-- or jsdelivr -->
<script src="https://cdn.jsdelivr.net/npm/@livepeer-frameworks/player-wc/dist/fw-player.iife.js"></script>

<fw-player
  content-id="pk_..."
  content-type="live"
  gateway-url="${config.gatewayUrl}"
  autoplay
  muted
  controls
></fw-player>`;

const snippetPlayerVanilla = `import { createPlayer } from '@livepeer-frameworks/player-core'

const player = createPlayer({
  target: '#player',
  contentId: 'pk_...',
  contentType: 'live',
  gatewayUrl: '${config.gatewayUrl}',
  theme: 'dracula',
})

player.state.on('playing', (isPlaying) => {
  console.log(isPlaying ? 'Playing' : 'Paused')
})`;

const snippetIngestReact = `import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-react";
import "@livepeer-frameworks/streamcrafter-react/streamcrafter.css";

export function BroadcastPanel() {
  return (
    <StreamCrafter
      gatewayUrl="${config.gatewayUrl}"
      streamKey="sk_live_..."
      initialProfile="broadcast"
    />
  );
}`;

const snippetIngestSvelte = `<script lang="ts">
  import { StreamCrafter } from '@livepeer-frameworks/streamcrafter-svelte'
  import '@livepeer-frameworks/streamcrafter-svelte/streamcrafter.css'
</script>

<StreamCrafter
  gatewayUrl="${config.gatewayUrl}"
  streamKey="sk_live_..."
  initialProfile="broadcast"
/>`;

const snippetIngestVanilla = `import { createStreamCrafter } from '@livepeer-frameworks/streamcrafter-core'

const studio = createStreamCrafter({
  target: '#studio',
  whipUrl: 'https://edge-ingest.eu.example.com/webrtc/stream-key',
  profile: 'broadcast',
  theme: 'dracula',
  locale: 'en',
})

studio.on('stateChange', ({ state }) => {
  console.log(state === 'streaming' ? 'LIVE' : state)
})

await studio.startCamera()
await studio.goLive()`;

const snippetIngestWc = `<!-- IIFE via npm CDN, no bundler needed -->
<!-- unpkg -->
<script src="https://unpkg.com/@livepeer-frameworks/streamcrafter-wc/dist/fw-streamcrafter.iife.js"></script>
<!-- or jsdelivr -->
<script src="https://cdn.jsdelivr.net/npm/@livepeer-frameworks/streamcrafter-wc/dist/fw-streamcrafter.iife.js"></script>

<fw-streamcrafter
  whip-url="https://edge-ingest.eu.example.com/webrtc/your-stream-key"
  initial-profile="broadcast"
></fw-streamcrafter>`;

const snippetBackendTs = `// npm i @livepeer-frameworks/api
import { createClient, CreateStreamDocument, expectResult } from "@livepeer-frameworks/api";

const client = createClient({
  url: "${config.gatewayUrl}",
  token: process.env.FRAMEWORKS_API_TOKEN,
});

const created = await client.request(CreateStreamDocument, {
  input: { name: "launch-event" },
});
const stream = expectResult(created.createStream, "Stream");

console.log("stream key:", stream.streamKey);
console.log("playback ID:", stream.playbackId);`;

const snippetBackendGo = `// go get github.com/Livepeer-FrameWorks/sdk-go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	frameworks "github.com/Livepeer-FrameWorks/sdk-go"
)

func main() {
	ctx := context.Background()
	client, err := frameworks.NewClient(frameworks.ClientOptions{
		URL:   "${config.gatewayUrl}",
		Token: os.Getenv("FRAMEWORKS_API_TOKEN"),
	})
	if err != nil {
		log.Fatal(err)
	}

	resp, err := frameworks.CreateStream(ctx, client, frameworks.CreateStreamInput{Name: "launch-event"})
	if err != nil {
		log.Fatal(err)
	}
	stream, err := frameworks.ExpectResult[*frameworks.CreateStreamCreateStream](resp.CreateStream)
	if err != nil {
		log.Fatal(err)
	}

	if key := stream.GetStreamKey(); key != nil {
		fmt.Println("stream key:", *key)
	}
	fmt.Println("playback ID:", stream.GetPlaybackId())
}`;

const snippetBackendPython = `# pip install livepeer-frameworks
import os

from livepeer_frameworks import FrameWorksClient, expect_result
from livepeer_frameworks.graphql import CreateStreamInput, Stream

with FrameWorksClient(
    "${config.gatewayUrl}",
    token=os.environ["FRAMEWORKS_API_TOKEN"],
) as fw:
    created = fw.create_stream(input=CreateStreamInput(name="launch-event"))
    stream = expect_result(created.create_stream, Stream)

    print("stream key:", stream.stream_key)
    print("playback ID:", stream.playback_id)`;

const snippetGraphql = `query LiveStreams {
  streamsConnection(page: { first: 10 }) {
    edges {
      node {
        name
        playbackId
        streamKey
        metrics {
          status
          isLive
          currentViewers
        }
      }
    }
    pageInfo { hasNextPage endCursor }
  }
}`;

export default function SdkCodePreview({ variant = "default", className }) {
  const [activeProductTab, setActiveProductTab] = useState("player");
  const [activeFrameworkByProduct, setActiveFrameworkByProduct] = useState({
    player: "react",
    ingest: "react",
    backend: "ts",
  });
  const [copied, setCopied] = useState(false);

  const snippets = {
    player: {
      react: snippetPlayerReact,
      svelte: snippetPlayerSvelte,
      wc: snippetPlayerWc,
      vanilla: snippetPlayerVanilla,
    },
    ingest: {
      react: snippetIngestReact,
      svelte: snippetIngestSvelte,
      wc: snippetIngestWc,
      vanilla: snippetIngestVanilla,
    },
    backend: {
      ts: snippetBackendTs,
      go: snippetBackendGo,
      python: snippetBackendPython,
    },
    graphql: snippetGraphql,
  };

  const frameworkLabels = {
    react: "React",
    svelte: "Svelte",
    wc: "Web Components",
    vanilla: "Vanilla",
    ts: "TypeScript",
    go: "Go",
    python: "Python",
  };

  const frameworkLangLabels = {
    react: "React / TSX",
    svelte: "Svelte 5",
    wc: "HTML",
    vanilla: "JavaScript",
    ts: "TypeScript",
    go: "Go",
    python: "Python",
  };

  const hasFrameworkTabs = typeof snippets[activeProductTab] === "object";
  const activeFramework = hasFrameworkTabs ? activeFrameworkByProduct[activeProductTab] : null;
  const activeSnippet = hasFrameworkTabs
    ? snippets[activeProductTab][activeFramework]
    : snippets[activeProductTab];
  const activeLangLabel = hasFrameworkTabs ? frameworkLangLabels[activeFramework] : "GraphQL";

  const handleCopy = () => {
    navigator.clipboard.writeText(activeSnippet);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const productTabs = [
    { id: "player", label: "Player SDK", icon: PlayIcon },
    { id: "ingest", label: "StreamCrafter", icon: ArrowUpTrayIcon },
    { id: "backend", label: "Backend SDK", icon: ServerStackIcon },
    { id: "graphql", label: "GraphQL", icon: CodeBracketIcon },
  ];

  return (
    <div
      className={cn(
        "marketing-code-panel w-full h-full min-h-[320px] flex flex-col",
        variant === "flush" && "marketing-code-panel--flush",
        className
      )}
    >
      {/* Header / Tabs */}
      <div className="marketing-code-panel__header">
        <div className="flex flex-col gap-1.5">
          <div className="flex flex-wrap gap-1">
            {productTabs.map((tab) => (
              <button
                key={tab.id}
                onClick={() => setActiveProductTab(tab.id)}
                className={cn(
                  "flex items-center gap-2 px-3 py-1.5 text-xs font-medium rounded-md transition-all outline-none",
                  activeProductTab === tab.id
                    ? "bg-primary/10 text-primary border border-primary/20 shadow-sm"
                    : "text-muted-foreground hover:text-foreground hover:bg-white/5 border border-transparent"
                )}
              >
                <tab.icon className="w-3.5 h-3.5" />
                {tab.label}
              </button>
            ))}
          </div>
          {hasFrameworkTabs ? (
            <div className="flex flex-wrap gap-1">
              {Object.entries(frameworkLabels)
                .filter(([id]) => snippets[activeProductTab]?.[id])
                .map(([id, label]) => (
                  <button
                    key={id}
                    onClick={() =>
                      setActiveFrameworkByProduct((prev) => ({
                        ...prev,
                        [activeProductTab]: id,
                      }))
                    }
                    className={cn(
                      "px-2.5 py-1 text-[11px] font-medium rounded-md transition-all outline-none border",
                      activeFramework === id
                        ? "bg-white/10 text-foreground border-white/20"
                        : "text-muted-foreground hover:text-foreground hover:bg-white/5 border-transparent"
                    )}
                  >
                    {label}
                  </button>
                ))}
            </div>
          ) : null}
        </div>
        <div className="marketing-code-panel__actions">
          <span className="text-[10px] font-bold tracking-widest uppercase text-muted-foreground/60 hidden sm:inline-block">
            {activeLangLabel}
          </span>
          <a
            href={sdkGuideUrl}
            className="text-[11px] font-medium text-muted-foreground hover:text-foreground transition-colors px-1.5 py-1 rounded-md hover:bg-white/5 whitespace-nowrap"
          >
            SDK guide
          </a>
          <button
            onClick={handleCopy}
            className="text-muted-foreground hover:text-foreground transition-colors p-1 rounded-md hover:bg-white/5"
            title="Copy to clipboard"
          >
            {copied ? (
              <ClipboardDocumentCheckIcon className="w-4 h-4 text-green-400" />
            ) : (
              <ClipboardDocumentIcon className="w-4 h-4" />
            )}
          </button>
        </div>
      </div>

      {/* Code Body */}
      <div className="marketing-code-panel__body flex-1 relative font-mono text-sm overflow-hidden">
        <AnimatePresence mode="wait">
          <motion.div
            key={`${activeProductTab}:${activeFramework ?? "single"}`}
            initial={{ opacity: 0, y: 5 }}
            animate={{ opacity: 1, y: 0 }}
            exit={{ opacity: 0, y: -5 }}
            transition={{ duration: 0.15 }}
            className="absolute inset-0 p-6 overflow-auto custom-scrollbar"
          >
            <pre className="text-blue-100/90 leading-relaxed [tab-size:4]">
              <code>{activeSnippet}</code>
            </pre>
          </motion.div>
        </AnimatePresence>
      </div>
    </div>
  );
}
