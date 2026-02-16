import { useState } from "react";
import { motion, AnimatePresence } from "framer-motion";
import {
  PlayIcon,
  ArrowUpTrayIcon,
  CodeBracketIcon,
  ClipboardDocumentCheckIcon,
  ClipboardDocumentIcon,
} from "@heroicons/react/24/outline";
import { cn } from "@/lib/utils";
import config from "../../config";

const snippetPlayerReact = `import { Player } from '@livepeer-frameworks/player-react'

export const MyStream = ({ playbackId }) => (
  <Player
    contentId={playbackId}
    contentType="live"
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

const snippetPlayerWc = `<!-- IIFE via npm CDN — no bundler needed -->
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

const snippetIngestWc = `<!-- IIFE via npm CDN — no bundler needed -->
<!-- unpkg -->
<script src="https://unpkg.com/@livepeer-frameworks/streamcrafter-wc/dist/fw-streamcrafter.iife.js"></script>
<!-- or jsdelivr -->
<script src="https://cdn.jsdelivr.net/npm/@livepeer-frameworks/streamcrafter-wc/dist/fw-streamcrafter.iife.js"></script>

<fw-streamcrafter
  whip-url="https://edge-ingest.example.com/webrtc/your-stream-key"
  initial-profile="broadcast"
></fw-streamcrafter>`;

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
  });
  const [copied, setCopied] = useState(false);

  const snippets = {
    player: {
      react: snippetPlayerReact,
      svelte: snippetPlayerSvelte,
      wc: snippetPlayerWc,
    },
    ingest: {
      react: snippetIngestReact,
      svelte: snippetIngestSvelte,
      wc: snippetIngestWc,
    },
    graphql: snippetGraphql,
  };

  const frameworkLabels = {
    react: "React",
    svelte: "Svelte",
    wc: "Web Components",
  };

  const frameworkLangLabels = {
    react: "React / TSX",
    svelte: "Svelte 5",
    wc: "HTML",
  };

  const hasFrameworkTabs = activeProductTab === "player" || activeProductTab === "ingest";
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
              {Object.entries(frameworkLabels).map(([id, label]) => (
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
            <pre className="text-blue-100/90 leading-relaxed">
              <code>{activeSnippet}</code>
            </pre>
          </motion.div>
        </AnimatePresence>
      </div>
    </div>
  );
}
